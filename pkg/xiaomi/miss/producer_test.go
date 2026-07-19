package miss

import (
	"encoding/binary"
	"testing"
)

func TestMotorCommand(t *testing.T) {
	for _, operation := range []int{0, 1, 2, 3, 4} {
		got, err := motorCommand(operation)
		if err != nil {
			t.Fatalf("motorCommand(%d): %v", operation, err)
		}
		if cmd := binary.BigEndian.Uint32(got); cmd != cmdMotorReq {
			t.Fatalf("motorCommand(%d) command = %#x; want %#x", operation, cmd, cmdMotorReq)
		}
		want := `{"operation":` + string(rune('0'+operation)) + `}`
		if payload := string(got[4:]); payload != want {
			t.Fatalf("motorCommand(%d) payload = %q; want %q", operation, payload, want)
		}
	}

	for _, operation := range []int{-1, 5} {
		if _, err := motorCommand(operation); err == nil {
			t.Fatalf("motorCommand(%d) expected error", operation)
		}
	}
}

func TestParseFPS(t *testing.T) {
	tests := []struct {
		value string
		want  uint32
		err   bool
	}{
		{"", 0, false},
		{"20", 20, false},
		{"120", 120, false},
		{"0", 0, true},
		{"121", 0, true},
		{"bad", 0, true},
	}

	for _, tt := range tests {
		got, err := parseFPS(tt.value)
		if (err != nil) != tt.err || got != tt.want {
			t.Errorf("parseFPS(%q) = %d, %v; want %d, error=%v", tt.value, got, err, tt.want, tt.err)
		}
	}
}

func TestVideoTimestampNormalizerDisabled(t *testing.T) {
	n := videoTimestampNormalizer{}
	for _, source := range []uint64{1000, 1041, 1084, 1146} {
		if got, want := n.normalize(source), TimeToRTP(source, videoClockRate); got != want {
			t.Fatalf("normalize(%d) = %d; want %d", source, got, want)
		}
	}
}

func TestVideoTimestampNormalizer(t *testing.T) {
	n := videoTimestampNormalizer{fps: 20}
	sources := []uint64{1000, 1000, 1041, 1084, 1146, 1191}
	wants := []uint32{90000, 90000, 94500, 99000, 103500, 108000}

	for i, source := range sources {
		if got := n.normalize(source); got != wants[i] {
			t.Fatalf("normalize(%d) = %d; want %d", source, got, wants[i])
		}
	}
}

func TestVideoTimestampNormalizerPreservesDroppedFrames(t *testing.T) {
	n := videoTimestampNormalizer{fps: 20}
	if got := n.normalize(1000); got != 90000 {
		t.Fatalf("first timestamp = %d; want 90000", got)
	}
	if got := n.normalize(1101); got != 99000 {
		t.Fatalf("timestamp after two frame intervals = %d; want 99000", got)
	}
	if got := n.normalize(1090); got != 103500 {
		t.Fatalf("timestamp after camera clock rollback = %d; want 103500", got)
	}
}

func TestOpusPacketSamples(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		want    uint32
	}{
		{"empty", nil, 1920},
		{"silk 40ms", []byte{2 << 3}, 1920},
		{"silk two 60ms frames", []byte{3<<3 | 1}, 5760},
		{"celt two 20ms frames", []byte{19<<3 | 2}, 1920},
		{"code 3 two 20ms frames", []byte{19<<3 | 3, 2}, 1920},
		{"invalid code 3", []byte{19<<3 | 3}, 1920},
		{"over 120ms", []byte{3<<3 | 3, 3}, 1920},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := opusPacketSamples(tt.payload); got != tt.want {
				t.Fatalf("opusPacketSamples(%v) = %d; want %d", tt.payload, got, tt.want)
			}
		})
	}
}
