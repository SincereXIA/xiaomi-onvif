package xiaomi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
)

func TestParseCameraPresets(t *testing.T) {
	for _, tc := range []struct {
		name, list string
		want       []xiaomiPreset
		bad        bool
	}{
		{"zero and sparse IDs", `[{"idx":0,"name":"Home"},{"location":7,"name":"SG9tZQ=="}]`, []xiaomiPreset{{"0", "Home"}, {"7", "Home"}}, false},
		{"explicit zero wins over idx", `[{"location":0,"idx":4,"name":"Home"}]`, []xiaomiPreset{{"0", "Home"}}, false},
		{"plain and encoded unicode", `[{"idx":1,"name":"客厅"},{"idx":64,"name":"5a625bqt"}]`, []xiaomiPreset{{"1", "客厅"}, {"64", "家庭"}}, false},
		{"empty", `[]`, []xiaomiPreset{}, false},
		{"missing", `[{"name":"Home"}]`, nil, true},
		{"duplicate", `[{"idx":1},{"location":1}]`, nil, true},
		{"negative", `[{"idx":-1}]`, nil, true},
		{"out of range", `[{"location":65}]`, nil, true},
		{"wrong shape", `{}`, nil, true},
		{"invalid JSON", `private-device-data`, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, _ := json.Marshal(tc.list)
			got, err := parseCameraPresets(value)
			if (err != nil) != tc.bad || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("presets=%v err=%v; want %v bad=%t", got, err, tc.want, tc.bad)
			}
		})
	}
}

func TestPresetMembership(t *testing.T) {
	presets := []xiaomiPreset{{"0", "Home"}, {"7", "Window"}}
	for _, token := range []string{"0", "7"} {
		if !hasPreset(presets, token) {
			t.Fatalf("discovered token %s rejected", token)
		}
	}
	for _, token := range []string{"1", "07", "-1", "65", ""} {
		if hasPreset(presets, token) {
			t.Fatalf("undiscovered token %s accepted", token)
		}
	}
}

func TestPresetNamePreservesPlainText(t *testing.T) {
	for _, name := range []string{"Home", "Test", "客厅", "AA==", "not-base64"} {
		if got := presetName(name); got != name {
			t.Fatalf("name=%q; want %q", got, name)
		}
	}
}

func TestPresetAPIUsesDiscoveredLocation(t *testing.T) {
	streams.HandleFunc("xiaomi", func(string) (core.Producer, error) {
		t.Fatal("presets must use the cloud without opening video")
		return nil, errors.New("unexpected dial")
	})
	name := t.Name()
	_, err := streams.New(name, "xiaomi://test-user:de@example.invalid?did=test-camera")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { streams.Delete(name) })
	original := presetCloudRequest
	t.Cleanup(func() { presetCloudRequest = original })
	var requests []string
	presetCloudRequest = func(userID, region, endpoint, params string) ([]byte, error) {
		if userID != "test-user" || region != "de" {
			t.Errorf("wrong cloud account or region")
		}
		requests = append(requests, endpoint)
		if endpoint == "/miotspec/prop/get" {
			property, _ := json.Marshal(`[{"location":0,"idx":9,"name":"Home"},{"idx":7,"name":"V2luZG93"}]`)
			return json.Marshal([]miotResult{{Code: 0, Value: property}})
		}
		var request struct {
			Params []struct {
				SIID  int `json:"siid"`
				PIID  int `json:"piid"`
				Value int `json:"value"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(params), &request); err != nil {
			t.Fatal(err)
		}
		if len(request.Params) != 1 || request.Params[0].SIID != 10 || request.Params[0].PIID != 2 || request.Params[0].Value != 0 {
			t.Fatalf("wrong activation payload: %s", params)
		}
		return []byte(`[{"code":0}]`), nil
	}
	get := httptest.NewRecorder()
	apiPresets(get, httptest.NewRequest(http.MethodGet, "/api/xiaomi/presets?src="+name, nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"token":"0","name":"Home"`) {
		t.Fatalf("discovery status=%d body=%s", get.Code, get.Body.String())
	}
	for _, tc := range []struct {
		token string
		code  int
	}{
		{"0", http.StatusOK}, {"1", http.StatusBadRequest}, {"9", http.StatusBadRequest},
		{"00", http.StatusBadRequest}, {"-1", http.StatusBadRequest}, {"65", http.StatusBadRequest},
	} {
		requests = nil
		response := httptest.NewRecorder()
		apiPresets(response, httptest.NewRequest(http.MethodPost, "/api/xiaomi/presets?src="+name+"&token="+tc.token, nil))
		if response.Code != tc.code {
			t.Errorf("token=%s status=%d body=%s", tc.token, response.Code, response.Body.String())
		}
		sets := 0
		for _, endpoint := range requests {
			if endpoint == "/miotspec/prop/set" {
				sets++
			}
		}
		if (sets == 1) != (tc.code == http.StatusOK) {
			t.Fatalf("token=%s cloud requests=%v", tc.token, requests)
		}
	}
}
