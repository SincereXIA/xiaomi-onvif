package xiaomi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
)

type testPTZProducer struct {
	core.Connection
	mu         sync.Mutex
	operations []int
	closed     chan struct{}
	once       sync.Once
}

func newTestPTZProducer() *testPTZProducer {
	return &testPTZProducer{closed: make(chan struct{}), Connection: core.Connection{
		Medias: []*core.Media{{Kind: core.KindVideo, Direction: core.DirectionRecvonly,
			Codecs: []*core.Codec{{Name: core.CodecH265}}}},
	}}
}

func (p *testPTZProducer) Start() error { <-p.closed; return nil }
func (p *testPTZProducer) Stop() error {
	p.once.Do(func() { close(p.closed); _ = p.Connection.Stop() })
	return nil
}
func (p *testPTZProducer) SetDirection(operation int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.operations = append(p.operations, operation)
	return nil
}

func TestPTZLeaseSharesDialAndExpires(t *testing.T) {
	var dials, closes atomic.Int32
	started, finish, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	controller := newTestPTZProducer()
	camera := newPTZCamera(100*time.Millisecond, func(*streams.Stream) (*ptzLease, error) {
		dials.Add(1)
		close(started)
		<-finish
		return &ptzLease{func() directionSetter { return controller }, func() { closes.Add(1); close(closed) }}, nil
	})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			got, release, err := camera.acquire(t.Context(), nil)
			if err != nil || got != controller {
				t.Errorf("controller=%v err=%v", got, err)
				return
			}
			release()
		})
	}
	<-started
	close(finish)
	wg.Wait()
	// Another button press extends the same lease rather than reconnecting.
	_, release, err := camera.acquire(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("idle lease was not released")
	}
	if dials.Load() != 1 || closes.Load() != 1 {
		t.Fatalf("dials=%d closes=%d", dials.Load(), closes.Load())
	}
}

func TestPTZCanceledDialClosesWithoutMovement(t *testing.T) {
	started, finish, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	controller := newTestPTZProducer()
	camera := newPTZCamera(time.Second, func(*streams.Stream) (*ptzLease, error) {
		close(started)
		<-finish
		return &ptzLease{func() directionSetter { return controller }, func() { close(closed) }}, nil
	})
	ctx, done := camera.command(t.Context())
	defer done()
	result := make(chan error, 1)
	go func() { _, _, err := camera.acquire(ctx, nil); result <- err }()
	<-started
	camera.cancelCommands()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire err=%v", err)
	}
	close(finish)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("late dial leaked its connection")
	}
	if len(controller.operations) != 0 {
		t.Fatal("canceled connection executed a movement")
	}
}

func TestPTZFailedDialCanRetry(t *testing.T) {
	calls := 0
	camera := newPTZCamera(0, func(*streams.Stream) (*ptzLease, error) {
		calls++
		return nil, errors.New("dial failed")
	})
	for range 2 {
		if _, _, err := camera.acquire(t.Context(), nil); err == nil {
			t.Fatal("failed dial accepted")
		}
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestPTZTimeoutDoesNotCancelOtherWaiter(t *testing.T) {
	started, finish, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	controller := newTestPTZProducer()
	camera := newPTZCamera(time.Hour, func(*streams.Stream) (*ptzLease, error) {
		close(started)
		<-finish
		return &ptzLease{func() directionSetter { return controller }, func() { close(closed) }}, nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	first := make(chan error, 1)
	go func() { _, _, err := camera.acquire(ctx, nil); first <- err }()
	<-started
	second := make(chan func(), 1)
	go func() {
		_, release, err := camera.acquire(t.Context(), nil)
		if err != nil {
			t.Error(err)
		}
		second <- release
	}()
	if err := <-first; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout err=%v", err)
	}
	close(finish)
	release := <-second
	if release == nil {
		t.Fatal("healthy waiter lost its connection")
	}
	release()
	camera.mu.Lock()
	camera.timer.Stop()
	camera.clearLocked()
	camera.mu.Unlock()
	<-closed
}

func TestPTZCanceledQueueDoesNotMove(t *testing.T) {
	camera := newPTZCamera(0, nil)
	if err := camera.lock(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, done := camera.command(t.Context())
	defer done()
	camera.cancelCommands()
	camera.unlock()
	if err := camera.lock(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled command acquired gate: %v", err)
	}
	controller := newTestPTZProducer()
	if err := runPTZ(ctx, controller, 1, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("run err=%v", err)
	}
	if len(controller.operations) != 0 {
		t.Fatal("canceled command moved camera")
	}
}

func TestPTZAPIColdConnectionAndViewerIsolation(t *testing.T) {
	producer := newTestPTZProducer()
	var dials atomic.Int32
	streams.HandleFunc("xiaomi", func(string) (core.Producer, error) {
		dials.Add(1)
		return producer, nil
	})
	name := t.Name()
	var fallbackDials atomic.Int32
	streams.HandleFunc("ffmpeg", func(string) (core.Producer, error) {
		fallbackDials.Add(1)
		return nil, errors.New("must not start transcoding")
	})
	stream, err := streams.New(name, "ffmpeg:unused#video=h264", "xiaomi://example.invalid?did="+name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { streams.Delete(name); ptzCameras.Delete(name) })
	camera := newPTZCamera(time.Hour, connectPTZ)
	ptzCameras.Store(name, camera)
	request := httptest.NewRequest(http.MethodPost, "/api/xiaomi/ptz?src="+name+"&direction=left&duration=1", nil)
	response := httptest.NewRecorder()
	apiPTZ(response, request)
	if response.Code != http.StatusOK || dials.Load() != 1 || fallbackDials.Load() != 0 {
		t.Fatalf("status=%d dials=%d body=%s", response.Code, dials.Load(), response.Body.String())
	}
	producer.mu.Lock()
	if len(producer.operations) < 2 || producer.operations[0] != 1 || producer.operations[len(producer.operations)-1] != 0 {
		t.Errorf("operations=%v", producer.operations)
	}
	producer.mu.Unlock()
	// A real viewer arriving during the lease must survive the lease's removal.
	viewer := &ptzConsumer{did: name, Connection: core.Connection{Medias: []*core.Media{{Kind: core.KindVideo,
		Direction: core.DirectionSendonly, Codecs: []*core.Codec{{Name: core.CodecAny}}}}}}
	if err = stream.AddConsumer(viewer); err != nil {
		t.Fatal(err)
	}
	camera.mu.Lock()
	camera.timer.Stop()
	camera.clearLocked()
	camera.mu.Unlock()
	select {
	case <-producer.closed:
		t.Fatal("lease removal stopped another viewer")
	default:
	}
	stream.RemoveConsumer(viewer)
	select {
	case <-producer.closed:
	case <-time.After(time.Second):
		t.Fatal("unused producer was not stopped")
	}
}

func TestPTZStopDoesNotDial(t *testing.T) {
	var dials atomic.Int32
	streams.HandleFunc("xiaomi", func(string) (core.Producer, error) {
		dials.Add(1)
		return nil, errors.New("must not dial")
	})
	name := t.Name()
	_, _ = streams.New(name, "xiaomi://example.invalid?did="+name)
	t.Cleanup(func() { streams.Delete(name); ptzCameras.Delete(name) })
	response := httptest.NewRecorder()
	apiPTZ(response, httptest.NewRequest(http.MethodPost, "/api/xiaomi/ptz?src="+name+"&direction=stop", nil))
	if response.Code != http.StatusOK || dials.Load() != 0 {
		t.Fatalf("status=%d dials=%d", response.Code, dials.Load())
	}
}

func TestPTZStopCancelsPendingAPIConnection(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	producer := newTestPTZProducer()
	streams.HandleFunc("xiaomi", func(string) (core.Producer, error) {
		close(started)
		<-finish
		return producer, nil
	})
	name := t.Name()
	_, _ = streams.New(name, "xiaomi://example.invalid?did="+name)
	t.Cleanup(func() { streams.Delete(name); ptzCameras.Delete(name) })
	moved := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		apiPTZ(response, httptest.NewRequest(http.MethodPost, "/api/xiaomi/ptz?src="+name+"&direction=right&duration=1", nil))
		moved <- response
	}()
	<-started
	stopped := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		apiPTZ(response, httptest.NewRequest(http.MethodPost, "/api/xiaomi/ptz?src="+name+"&direction=stop", nil))
		stopped <- response
	}()
	select {
	case response := <-stopped:
		if response.Code != http.StatusOK {
			t.Errorf("stop status=%d", response.Code)
		}
	case <-time.After(time.Second):
		close(finish)
		t.Fatal("stop blocked behind camera dial")
	}
	if response := <-moved; response.Code == http.StatusOK {
		t.Error("canceled movement reported success")
	}
	close(finish)
	select {
	case <-producer.closed:
	case <-time.After(time.Second):
		t.Fatal("late connection was not released")
	}
	producer.mu.Lock()
	defer producer.mu.Unlock()
	if len(producer.operations) != 0 {
		t.Fatalf("canceled movement executed operations=%v", producer.operations)
	}
}

func TestPTZSourceFilter(t *testing.T) {
	consumer := &ptzConsumer{did: "camera"}
	for _, source := range []string{"ffmpeg:camera#video=h264", "xiaomi://host?did=other", "rtsp://host/camera"} {
		if consumer.AcceptSource(source) {
			t.Fatalf("accepted unrelated source %q", source)
		}
	}
	if !consumer.AcceptSource("xiaomi://host?did=camera") {
		t.Fatal("rejected camera source")
	}
}

func TestPTZReusesActiveQualityVariant(t *testing.T) {
	producer := newTestPTZProducer()
	var dials atomic.Int32
	streams.HandleFunc("xiaomi", func(string) (core.Producer, error) {
		dials.Add(1)
		return producer, nil
	})
	name := t.Name()
	_, _ = streams.New(name, "xiaomi://example.invalid?did="+name+"&subtype=2")
	variant, _ := streams.New(name+"_sd", "xiaomi://example.invalid?did="+name+"&subtype=1")
	viewer := &ptzConsumer{did: name, Connection: core.Connection{Medias: []*core.Media{{Kind: core.KindVideo,
		Direction: core.DirectionSendonly, Codecs: []*core.Codec{{Name: core.CodecAny}}}}}}
	if err := variant.AddConsumer(viewer); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		camera := cameraPTZ(name)
		camera.mu.Lock()
		if camera.timer != nil {
			camera.timer.Stop()
		}
		camera.clearLocked()
		camera.mu.Unlock()
		variant.RemoveConsumer(viewer)
		streams.Delete(name)
		streams.Delete(name + "_sd")
		ptzCameras.Delete(name)
	})
	response := httptest.NewRecorder()
	apiPTZ(response, httptest.NewRequest(http.MethodPost, "/api/xiaomi/ptz?src="+name+"&direction=up&duration=1", nil))
	if response.Code != http.StatusOK || dials.Load() != 1 {
		t.Fatalf("status=%d dials=%d", response.Code, dials.Load())
	}
}
