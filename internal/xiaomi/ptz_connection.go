package xiaomi

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
)

const ptzConnectTimeout = 25 * time.Second

// A lease pins the original video producer without transcoding or publishing it.
// AddConsumer uses the existing stream lifecycle, including producer reconnects.
type ptzLease struct {
	controller func() directionSetter
	close      func()
}

type ptzCamera struct {
	mu        sync.Mutex
	ready     chan struct{}
	lease     *ptzLease
	err       error
	users     int
	timer     *time.Timer
	epoch     uint64
	commands  map[uint64]context.CancelFunc
	next      uint64
	gate      chan struct{}
	keepalive time.Duration
	connect   func(*streams.Stream) (*ptzLease, error)
}

var ptzCameras sync.Map

func cameraPTZ(did string) *ptzCamera {
	value, _ := ptzCameras.LoadOrStore(did, newPTZCamera(5*time.Second, connectPTZ))
	return value.(*ptzCamera)
}

func newPTZCamera(keepalive time.Duration, connect func(*streams.Stream) (*ptzLease, error)) *ptzCamera {
	return &ptzCamera{
		gate: make(chan struct{}, 1), commands: make(map[uint64]context.CancelFunc),
		keepalive: keepalive, connect: connect,
	}
}

func connectPTZ(stream *streams.Stream) (*ptzLease, error) {
	consumer := &ptzConsumer{did: cameraDID(stream.Sources()), Connection: core.Connection{
		ID: core.NewID(), FormatName: "xiaomi-ptz",
		Medias: []*core.Media{{Kind: core.KindVideo, Direction: core.DirectionSendonly,
			Codecs: []*core.Codec{{Name: core.CodecAny}}}},
	}}
	if err := stream.AddConsumer(consumer); err != nil {
		return nil, errors.New("could not establish Xiaomi PTZ connection")
	}
	lease := &ptzLease{
		controller: func() directionSetter { return connectedPTZ(stream) },
		close:      func() { stream.RemoveConsumer(consumer) },
	}
	if lease.controller() == nil {
		lease.close()
		return nil, errors.New("camera connection does not support Xiaomi PTZ")
	}
	return lease, nil
}

// Unlike probe.Probe, this consumer has no packet counter mutated concurrently
// with /api/streams serialization. The sender simply drains the original video.
type ptzConsumer struct {
	core.Connection
	did string
}

func (c *ptzConsumer) AcceptSource(source string) bool {
	u, err := url.Parse(source)
	return err == nil && u.Scheme == "xiaomi" && u.Query().Get("did") == c.did
}

func (c *ptzConsumer) AddTrack(media *core.Media, codec *core.Codec, track *core.Receiver) error {
	sender := core.NewSender(media, codec)
	sender.Handler = func(*core.Packet) {}
	sender.HandleRTP(track)
	c.Senders = append(c.Senders, sender)
	return nil
}

func (c *ptzCamera) acquire(ctx context.Context, stream *streams.Stream) (directionSetter, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	c.users++
	c.epoch++
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if c.ready == nil {
		c.ready = make(chan struct{})
		go func() {
			lease, err := c.connect(stream)
			c.mu.Lock()
			c.lease, c.err = lease, err
			close(c.ready)
			if c.users == 0 {
				c.clearLocked()
			}
			c.mu.Unlock()
		}()
	}
	ready := c.ready
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		c.release(false)
		return nil, nil, ctx.Err()
	case <-ready:
	}
	c.mu.Lock()
	controller, err := directionSetter(nil), c.err
	if err == nil {
		controller = c.lease.controller()
		if controller == nil {
			err = errors.New("Xiaomi PTZ connection is unavailable")
		}
	}
	c.mu.Unlock()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		c.release(false)
		return nil, nil, err
	}
	return controller, func() { c.release(true) }, nil
}

func (c *ptzCamera) release(keep bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.users--
	if c.users != 0 {
		return
	}
	select {
	case <-c.ready:
		if !keep || c.err != nil {
			c.clearLocked()
			return
		}
		epoch := c.epoch
		c.timer = time.AfterFunc(c.keepalive, func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.users == 0 && c.epoch == epoch {
				c.clearLocked()
			}
		})
	default:
		// A non-context-aware upstream dial may finish after cancellation.
		// Its completion closes the unused lease and never executes a command.
	}
}

func (c *ptzCamera) clearLocked() {
	if c.lease != nil {
		c.lease.close()
	}
	c.lease, c.err, c.ready, c.timer = nil, nil, nil, nil
}

func (c *ptzCamera) command(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(parent, ptzConnectTimeout)
	c.mu.Lock()
	c.next++
	id := c.next
	c.commands[id] = cancel
	c.mu.Unlock()
	return ctx, func() {
		cancel()
		c.mu.Lock()
		delete(c.commands, id)
		c.mu.Unlock()
	}
}

func (c *ptzCamera) cancelCommands() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cancel := range c.commands {
		cancel()
	}
}

func (c *ptzCamera) lock(ctx context.Context) error {
	select {
	case c.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-c.gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *ptzCamera) unlock() { <-c.gate }

func ptzStream(src string) (*streams.Stream, string, error) {
	stream := streams.Get(src)
	if stream == nil {
		return nil, "", errors.New("Xiaomi PTZ stream not found")
	}
	did := cameraDID(stream.Sources())
	if did == "" {
		return nil, "", errors.New("PTZ stream is not a Xiaomi camera")
	}
	// Reuse an already connected quality variant before dialing the named source.
	if connectedPTZ(stream) == nil {
		for name, sources := range streams.GetAllSources() {
			if cameraDID(sources) == did && connectedPTZ(streams.Get(name)) != nil {
				return streams.Get(name), did, nil
			}
		}
	}
	return stream, did, nil
}
