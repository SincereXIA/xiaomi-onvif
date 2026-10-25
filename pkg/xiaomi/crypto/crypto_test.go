package crypto

import (
	"bytes"
	"testing"
)

func TestDecodeRejectsShortPayloads(t *testing.T) {
	for size := 0; size < 8; size++ {
		if _, err := Decode(make([]byte, size), make([]byte, 32)); err == nil {
			t.Fatalf("Decode accepted a %d-byte payload", size)
		}
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	for _, payload := range [][]byte{nil, []byte("audio payload"), bytes.Repeat([]byte{0xAB}, 4096)} {
		encoded, err := Encode(payload, key)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Decode(encoded, key)
		if err != nil || !bytes.Equal(decoded, payload) {
			t.Fatalf("Decode: %v", err)
		}
	}
}
