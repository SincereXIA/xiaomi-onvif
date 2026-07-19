package xiaomi

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

type fakePTZ struct {
	operations []int
}

func (p *fakePTZ) SetDirection(operation int) error {
	p.operations = append(p.operations, operation)
	return nil
}

func TestParsePTZDuration(t *testing.T) {
	if got, err := parsePTZDuration("", 1); err != nil || got != ptzDefaultDuration {
		t.Fatalf("default duration = %v, %v", got, err)
	}
	if got, err := parsePTZDuration("150", 1); err != nil || got != 150*time.Millisecond {
		t.Fatalf("explicit duration = %v, %v", got, err)
	}
	if got, err := parsePTZDuration("9999", 1); err == nil || got != 0 {
		t.Fatalf("invalid duration = %v, %v", got, err)
	}
	if got, err := parsePTZDuration("9999", 0); err != nil || got != 0 {
		t.Fatalf("stop duration = %v, %v", got, err)
	}
}

func TestRunPTZStops(t *testing.T) {
	controller := new(fakePTZ)
	if err := runPTZ(context.Background(), controller, 1, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 0}; !reflect.DeepEqual(controller.operations, want) {
		t.Fatalf("operations = %v; want %v", controller.operations, want)
	}
}

func TestDecodeMIoTResults(t *testing.T) {
	for _, response := range []string{
		`[{"code":0,"value":"ok"}]`,
		`{"result":[{"code":0,"value":"ok"}]}`,
	} {
		results, err := decodeMIoTResults([]byte(response))
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 1 || results[0].Code != 0 {
			t.Fatalf("results=%v", results)
		}
		var value string
		if err = json.Unmarshal(results[0].Value, &value); err != nil || value != "ok" {
			t.Fatalf("value=%q err=%v", value, err)
		}
	}
}
