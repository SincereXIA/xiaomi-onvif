package cs2

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func frame(payload []byte) []byte {
	b := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(b, uint32(len(payload)))
	copy(b[4:], payload)
	return b
}

func packet(channel byte, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	b[0], b[1], b[4], b[5] = magic, msgDrw, magicDrw, channel
	binary.BigEndian.PutUint16(b[2:], uint16(len(b)-4))
	copy(b[8:], payload)
	return b
}

func TestUDPReadChecksPeerAndPacketLength(t *testing.T) {
	for _, name := range []string{"wrong IP", "wrong port", "short packet", "valid peer"} {
		t.Run(name, func(t *testing.T) {
			receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer receiver.Close()
			sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close()
			peer := *sender.LocalAddr().(*net.UDPAddr)
			// Exercise equality between four-byte and IPv4-mapped IP addresses.
			peer.IP = net.ParseIP("127.0.0.1")
			data := packet(0, frame([]byte{1, 2, 3, 4}))
			switch name {
			case "wrong IP":
				peer.IP = net.IPv4(192, 0, 2, 10)
			case "wrong port":
				peer.Port = 0
			case "short packet":
				data = []byte{magic}
			}
			conn := &udpConn{UDPConn: receiver, addr: &peer}
			if _, err := sender.WriteToUDP(data, receiver.LocalAddr().(*net.UDPAddr)); err != nil {
				t.Fatal(err)
			}
			if err := receiver.SetReadDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, maxPacketSize)
			n, err := conn.Read(buf)
			if name == "valid peer" {
				if err != nil || !bytes.Equal(buf[:n], data) {
					t.Fatalf("Read() = %x, %v", buf[:n], err)
				}
			} else {
				var timeout net.Error
				if !errors.As(err, &timeout) || !timeout.Timeout() {
					t.Fatalf("unexpected packet accepted: %x, %v", buf[:n], err)
				}
			}
		})
	}
}

func TestFrameLengthValidation(t *testing.T) {
	for _, size := range []uint32{0, 1, 3, maxFrameSize + 1, ^uint32(0)} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			c := newDataChannel(0, 10, 4)
			header := make([]byte, 4)
			binary.BigEndian.PutUint32(header, size)
			if err := c.Push(header[:2]); err != nil {
				t.Fatal(err)
			}
			if err := c.Push(header[2:]); err == nil {
				t.Fatal("invalid length accepted")
			}
			if c.waitSize != 0 || c.bufferedSize.Load() != 0 || cap(c.waitData) > 8 {
				t.Fatal("allocated an invalid frame")
			}
		})
	}
	c := newDataChannel(0, 1, hdrSize)
	if err := c.Push(frame(make([]byte, hdrSize-1))); err == nil {
		t.Fatal("short media frame accepted")
	}
}

func TestFragmentedAndCoalescedFrames(t *testing.T) {
	first := []byte("command one")
	second := []byte("command two")
	wire := append(frame(first), frame(second)...)
	for split := 0; split <= len(wire); split++ {
		c := newDataChannel(0, 2, 4)
		if err := c.Push(wire[:split]); err != nil {
			t.Fatalf("split %d: %v", split, err)
		}
		if err := c.Push(wire[split:]); err != nil {
			t.Fatalf("split %d: %v", split, err)
		}
		for _, want := range [][]byte{first, second} {
			got, ok := c.Pop()
			if !ok || !bytes.Equal(got, want) {
				t.Fatalf("split %d: got %x, want %x", split, got, want)
			}
		}
		if c.bufferedSize.Load() != 0 {
			t.Fatal("consumed frames retained their byte reservation")
		}
	}
}

func TestFrameBufferBudget(t *testing.T) {
	c := newDataChannel(0, 10, 4)
	c.frameLimit, c.bufferLimit = 64, 64
	if err := c.Push(frame(make([]byte, 40))); err != nil {
		t.Fatal(err)
	}
	if err := c.Push(frame(make([]byte, 40))[:4]); err == nil {
		t.Fatal("queued frames did not count toward the byte limit")
	}
	if c.bufferedSize.Load() != 40 || c.waitSize != 0 {
		t.Fatal("over-budget frame reserved memory")
	}
	if _, ok := c.Pop(); !ok {
		t.Fatal("missing queued frame")
	}
	if c.bufferedSize.Load() != 0 {
		t.Fatal("Pop did not release the reservation")
	}
}

func TestPopReleasesBudgetForNextFrame(t *testing.T) {
	c := newDataChannel(0, 2, 4)
	c.frameLimit, c.bufferLimit = 64, 64
	for range 3 {
		if err := c.Push(frame(make([]byte, 64))); err != nil {
			t.Fatal(err)
		}
		if _, ok := c.Pop(); !ok {
			t.Fatal("missing frame")
		}
	}
}

func TestCloseReleasesIncompleteFrame(t *testing.T) {
	c := newDataChannel(250, 2, 4)
	wire := frame(make([]byte, 64))
	if err := c.Push(wire[:7]); err != nil {
		t.Fatal(err)
	}
	if c.bufferedSize.Load() != 64 {
		t.Fatal("incomplete frame did not reserve its declared size")
	}
	c.Close()
	if c.bufferedSize.Load() != 0 || c.waitData != nil || c.pushBuf != nil {
		t.Fatal("Close retained incomplete data")
	}
	if _, ok := c.Pop(); ok {
		t.Fatal("closed channel returned data")
	}
}

func TestTCPBackpressureResumesAndCancels(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint(cancel), func(t *testing.T) {
			c := newDataChannel(0, 1, 4)
			first, second := []byte("first"), []byte("second")
			if err := c.Push(frame(first)); err != nil {
				t.Fatal(err)
			}
			done, result := make(chan struct{}), make(chan error, 1)
			go func() { result <- c.push(frame(second), done) }()
			select {
			case err := <-result:
				t.Fatalf("did not wait for consumer: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			if cancel {
				close(done)
			} else {
				got, _ := c.Pop()
				if !bytes.Equal(got, first) {
					t.Fatal("first frame changed")
				}
			}
			select {
			case err := <-result:
				if cancel && !errors.Is(err, net.ErrClosed) || !cancel && err != nil {
					t.Fatalf("push error: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("push did not finish")
			}
			got, _ := c.Pop()
			want := second
			if cancel {
				want = first
			}
			if !bytes.Equal(got, want) || c.bufferedSize.Load() != 0 {
				t.Fatal("frame order or accounting changed")
			}
		})
	}
}

func TestTCPBackpressureTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("exercises the ten-second stalled consumer timeout")
	}
	c := newDataChannel(0, 1, 4)
	if err := c.Push(frame([]byte("first"))); err != nil {
		t.Fatal(err)
	}
	err := c.push(frame([]byte("second")), make(chan struct{}))
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("push error: %v", err)
	}
	if c.bufferedSize.Load() != int64(len("first")) {
		t.Fatal("timed-out frame retained its reservation")
	}
}

func TestUDPReorderingAndOverflow(t *testing.T) {
	c := newDataChannel(2, 2, 4)
	first, second := []byte("first"), []byte("second")
	if n, err := c.PushSeq(1, frame(second)); n != 0 || err != nil {
		t.Fatalf("PushSeq = %d, %v", n, err)
	}
	if n, err := c.PushSeq(0, frame(first)); n != 2 || err != nil {
		t.Fatalf("PushSeq = %d, %v", n, err)
	}
	for _, want := range [][]byte{first, second} {
		got, _ := c.Pop()
		if !bytes.Equal(got, want) {
			t.Fatal("frames reordered")
		}
	}
	if _, err := c.PushSeq(2, frame(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PushSeq(3, frame(second)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PushSeq(4, frame(first)); err == nil {
		t.Fatal("UDP queue overflow should fail immediately")
	}
	if c.bufferedSize.Load() != int64(len(first)+len(second)) {
		t.Fatal("overflow retained a frame")
	}
}

func TestFrameStorageDoesNotAlias(t *testing.T) {
	c := newDataChannel(0, 2, 4)
	wire := append(frame([]byte("first")), frame([]byte("second"))...)
	if err := c.Push(wire); err != nil {
		t.Fatal(err)
	}
	clear(wire)
	first, _ := c.Pop()
	second, _ := c.Pop()
	if string(first) != "first" || string(second) != "second" || cap(first) != len(first) || cap(second) != len(second) {
		t.Fatal("frames retained shared storage")
	}
}

type packetConn struct {
	packets [][]byte
	writes  bytes.Buffer
	mu      sync.Mutex
	closes  atomic.Int32
}

func (c *packetConn) Read(b []byte) (int, error) {
	if len(c.packets) == 0 {
		return 0, io.EOF
	}
	p := c.packets[0]
	c.packets = c.packets[1:]
	return copy(b, p), nil
}
func (c *packetConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes.Write(b)
}
func (c *packetConn) Close() error                   { c.closes.Add(1); return nil }
func (*packetConn) LocalAddr() net.Addr              { return nil }
func (*packetConn) RemoteAddr() net.Addr             { return nil }
func (*packetConn) SetDeadline(time.Time) error      { return nil }
func (*packetConn) SetReadDeadline(time.Time) error  { return nil }
func (*packetConn) SetWriteDeadline(time.Time) error { return nil }

func testConn(wire net.Conn) *Conn {
	return &Conn{Conn: wire, done: make(chan struct{}), channels: [4]*dataChannel{
		newDataChannel(0, 10, 4), nil, newDataChannel(250, 100, hdrSize), nil,
	}}
}

func TestWorkerRejectsMalformedPackets(t *testing.T) {
	badLength := packet(0, frame([]byte("ignored")))
	badLength[2], badLength[3] = 0, 0
	badMagic := packet(0, frame([]byte("ignored")))
	badMagic[0] = 0
	badDRW := packet(0, frame([]byte("ignored")))
	badDRW[4] = 0
	short := []byte{magic, msgDrw, 0, 0}
	for _, tcp := range []bool{false, true} {
		wire := &packetConn{packets: [][]byte{
			{}, {magic}, short, badLength, badMagic, badDRW,
			packet(1, nil), packet(3, nil), packet(4, nil), packet(255, nil),
			packet(0, frame([]byte("valid"))),
		}}
		c := testConn(wire)
		c.isTCP = tcp
		c.worker()
		got, ok := c.channels[0].Pop()
		if !ok || string(got) != "valid" {
			t.Fatalf("TCP %v: valid packet lost", tcp)
		}
		if _, ok := c.channels[0].Pop(); ok {
			t.Fatal("invalid packet reached the consumer")
		}
		if wire.closes.Load() != 1 {
			t.Fatal("worker did not close the socket")
		}
	}
}

func TestShortFramesReturnErrors(t *testing.T) {
	for size := 0; size < hdrSize; size++ {
		c := testConn(&packetConn{})
		if size < 4 {
			c.channels[0].bufferedSize.Store(int64(size))
			c.channels[0].popBuf <- make([]byte, size)
			if _, _, err := c.ReadCommand(); err == nil {
				t.Fatalf("accepted %d-byte command", size)
			}
		}
		c.channels[2].bufferedSize.Store(int64(size))
		c.channels[2].popBuf <- make([]byte, size)
		if _, _, err := c.ReadPacket(); err == nil {
			t.Fatalf("accepted %d-byte media frame", size)
		}
	}
}

func TestInvalidInnerFrameClosesConnection(t *testing.T) {
	for _, channel := range []byte{0, 2} {
		wire := &packetConn{packets: [][]byte{packet(channel, frame([]byte{1}))}}
		c := testConn(wire)
		c.worker()
		if wire.closes.Load() != 1 || !strings.Contains(c.Error().Error(), "invalid frame size") {
			t.Fatal("invalid frame did not terminate the connection")
		}
		if c.channels[channel].bufferedSize.Load() != 0 {
			t.Fatal("invalid frame retained memory")
		}
	}
}

func TestCloseUnblocksBackpressuredWorker(t *testing.T) {
	wire := &packetConn{packets: [][]byte{packet(2, append(frame(make([]byte, hdrSize)), frame(make([]byte, hdrSize))...))}}
	c := testConn(wire)
	c.isTCP = true
	c.channels[2] = newDataChannel(250, 1, hdrSize)
	exited := make(chan struct{})
	go func() { c.worker(); close(exited) }()
	deadline := time.Now().Add(time.Second)
	for c.channels[2].bufferedSize.Load() != 2*hdrSize {
		if time.Now().After(deadline) {
			t.Fatal("worker did not enter backpressure")
		}
		time.Sleep(time.Millisecond)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("worker did not exit")
	}
	if wire.closes.Load() != 1 || !errors.Is(c.Error(), net.ErrClosed) {
		t.Fatal("connection close was not idempotent")
	}
	if _, ok := c.channels[2].Pop(); !ok {
		t.Fatal("queued media was lost")
	}
	if c.channels[2].bufferedSize.Load() != 0 {
		t.Fatal("closed connection retained reserved bytes")
	}
}

func TestWritePacketPreservesAudioPayload(t *testing.T) {
	wire := &packetConn{}
	c := testConn(wire)
	hdr, payload := bytes.Repeat([]byte{0x11}, hdrSize), bytes.Repeat([]byte{0xAB}, 83)
	if err := c.WritePacket(hdr, payload); err != nil {
		t.Fatal(err)
	}
	got := wire.writes.Bytes()
	if !bytes.Equal(got[12:12+hdrSize], hdr) || !bytes.Equal(got[12+hdrSize:], payload) {
		t.Fatal("talk-back header or payload changed")
	}
}

func TestTCPReadValidatesEnvelope(t *testing.T) {
	for _, test := range []struct {
		size  int
		magic byte
		valid bool
	}{
		{0, magicTCP, false}, {3, magicTCP, false}, {maxPacketSize + 1, magicTCP, false},
		{4, 0, false}, {4, magicTCP, true},
	} {
		wire := make([]byte, 8+test.size)
		binary.BigEndian.PutUint16(wire, uint16(test.size))
		wire[2] = test.magic
		c := &tcpConn{rd: bufio.NewReader(bytes.NewReader(wire))}
		n, err := c.Read(make([]byte, maxPacketSize))
		if (err == nil) != test.valid || test.valid && n != test.size {
			t.Fatalf("size %d: n=%d, err=%v", test.size, n, err)
		}
	}
}

func FuzzDataChannelFrames(f *testing.F) {
	for _, seed := range [][]byte{nil, {0}, {0, 0, 0, 0}, {255, 255, 255, 255}, frame([]byte("valid frame"))} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, wire []byte) {
		c := newDataChannel(0, 100, 4)
		c.frameLimit, c.bufferLimit = 64<<10, 128<<10
		defer c.Close()
		for len(wire) > 0 {
			n := min(len(wire), 13)
			if err := c.Push(wire[:n]); err != nil {
				return
			}
			wire = wire[n:]
			for len(c.popBuf) > 0 {
				_, _ = c.Pop()
			}
			if c.bufferedSize.Load() > int64(c.bufferLimit) {
				t.Fatal("buffer budget exceeded")
			}
		}
	})
}
