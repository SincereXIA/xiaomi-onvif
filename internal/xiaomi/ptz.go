package xiaomi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/streams"
)

const (
	ptzRepeatInterval  = 100 * time.Millisecond
	ptzDefaultDuration = 300 * time.Millisecond
	ptzMaxDuration     = 2 * time.Second
)

var ptzLocks sync.Map

type directionSetter interface {
	SetDirection(operation int) error
}

var ptzOperations = map[string]int{
	"stop":  0,
	"left":  1,
	"right": 2,
	"up":    3,
	"down":  4,
}

func apiPTZ(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query()
	src := query.Get("src")
	if src == "" {
		http.Error(w, "src is required", http.StatusBadRequest)
		return
	}

	direction := query.Get("direction")
	operation, ok := ptzOperations[direction]
	if !ok {
		http.Error(w, "direction must be stop, left, right, up or down", http.StatusBadRequest)
		return
	}

	duration, err := parsePTZDuration(query.Get("duration"), operation)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	controller, did, err := findPTZController(src)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	ptzMu := cameraPTZMutex(did)
	ptzMu.Lock()
	err = runPTZ(r.Context(), controller, operation, duration)
	ptzMu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	api.ResponseJSON(w, map[string]any{
		"src":       src,
		"direction": direction,
		"duration":  duration.Milliseconds(),
	})
}

func parsePTZDuration(value string, operation int) (time.Duration, error) {
	if operation == 0 {
		return 0, nil
	}
	if value == "" {
		return ptzDefaultDuration, nil
	}

	milliseconds, err := strconv.Atoi(value)
	if err != nil || milliseconds < 1 || milliseconds > int(ptzMaxDuration.Milliseconds()) {
		return 0, fmt.Errorf("duration must be between 1 and %d milliseconds", ptzMaxDuration.Milliseconds())
	}
	return time.Duration(milliseconds) * time.Millisecond, nil
}

func findPTZController(src string) (directionSetter, string, error) {
	stream := streams.Get(src)
	if stream == nil {
		return nil, "", fmt.Errorf("stream %q not found", src)
	}

	did := cameraDID(stream.Sources())
	if did == "" {
		return nil, "", fmt.Errorf("stream %q is not a Xiaomi camera", src)
	}

	// Prefer the requested stream, then any active quality variant for the same
	// camera. Frigate commonly keeps only the detect and record variants open.
	if controller := connectedPTZ(stream); controller != nil {
		return controller, did, nil
	}
	for name, sources := range streams.GetAllSources() {
		if name == src || cameraDID(sources) != did {
			continue
		}
		if controller := connectedPTZ(streams.Get(name)); controller != nil {
			return controller, did, nil
		}
	}

	return nil, "", fmt.Errorf("Xiaomi camera %q has no active connection", src)
}

func cameraPTZMutex(did string) *sync.Mutex {
	value, _ := ptzLocks.LoadOrStore(did, new(sync.Mutex))
	return value.(*sync.Mutex)
}

func cameraDID(sources []string) string {
	for _, source := range sources {
		u, err := url.Parse(source)
		if err == nil && u.Scheme == "xiaomi" {
			return u.Query().Get("did")
		}
	}
	return ""
}

func connectedPTZ(stream *streams.Stream) directionSetter {
	if stream == nil {
		return nil
	}
	for _, connection := range stream.GetConnections() {
		if controller, ok := connection.(directionSetter); ok {
			return controller
		}
	}
	return nil
}

func runPTZ(ctx context.Context, controller directionSetter, operation int, duration time.Duration) (err error) {
	if operation == 0 {
		return controller.SetDirection(0)
	}

	defer func() {
		if stopErr := controller.SetDirection(0); err == nil && stopErr != nil {
			err = stopErr
		}
	}()

	timer := time.NewTimer(duration)
	defer timer.Stop()
	ticker := time.NewTicker(ptzRepeatInterval)
	defer ticker.Stop()

	for {
		if err = controller.SetDirection(operation); err != nil {
			return err
		}
		select {
		case <-timer.C:
			return nil
		case <-ticker.C:
		case <-ctx.Done():
			return errors.New("PTZ request canceled")
		}
	}
}

type xiaomiPreset struct {
	Token string `json:"token"`
	Name  string `json:"name"`
}

type cameraCloud struct {
	did    string
	userID string
	region string
}

type miotResult struct {
	Code  int             `json:"code"`
	Value json.RawMessage `json:"value"`
}

func apiPresets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	src := r.URL.Query().Get("src")
	if src == "" {
		http.Error(w, "src is required", http.StatusBadRequest)
		return
	}
	camera, err := findCameraCloud(src)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if r.Method == http.MethodGet {
		presets, err := getCameraPresets(camera)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		api.ResponseJSON(w, presets)
		return
	}

	token := r.URL.Query().Get("token")
	location, err := strconv.Atoi(token)
	if err != nil || location < 1 {
		http.Error(w, "token must be a positive preset location", http.StatusBadRequest)
		return
	}
	if err = gotoCameraPreset(camera, location); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	api.ResponseJSON(w, map[string]any{"src": src, "token": token})
}

func findCameraCloud(src string) (*cameraCloud, error) {
	stream := streams.Get(src)
	if stream == nil {
		return nil, fmt.Errorf("stream %q not found", src)
	}
	did := cameraDID(stream.Sources())
	if did == "" {
		return nil, fmt.Errorf("stream %q is not a Xiaomi camera", src)
	}

	if camera := parseCameraCloud(stream.Sources(), did); camera != nil {
		return camera, nil
	}
	for name, sources := range streams.GetAllSources() {
		if name != src && cameraDID(sources) == did {
			if camera := parseCameraCloud(sources, did); camera != nil {
				return camera, nil
			}
		}
	}
	return nil, fmt.Errorf("Xiaomi camera %q has no cloud credentials", src)
}

func parseCameraCloud(sources []string, did string) *cameraCloud {
	for _, source := range sources {
		u, err := url.Parse(source)
		if err != nil || u.Scheme != "xiaomi" || u.User == nil || u.Query().Get("did") != did {
			continue
		}
		region, _ := u.User.Password()
		return &cameraCloud{did: did, userID: u.User.Username(), region: region}
	}
	return nil
}

func getCameraPresets(camera *cameraCloud) ([]xiaomiPreset, error) {
	params := fmt.Sprintf(`{"params":[{"did":%q,"siid":10,"piid":1}]}`, camera.did)
	res, err := cloudRequest(camera.userID, camera.region, "/miotspec/prop/get", params)
	if err != nil {
		return nil, err
	}
	results, err := decodeMIoTResults(res)
	if err != nil {
		return nil, fmt.Errorf("decode preset response: %w", err)
	}
	if len(results) == 0 || results[0].Code != 0 {
		return nil, fmt.Errorf("camera returned no presets: %s", res)
	}

	var encoded string
	if err = json.Unmarshal(results[0].Value, &encoded); err != nil {
		return nil, fmt.Errorf("decode preset property: %w", err)
	}
	var areas []struct {
		Index    int    `json:"idx"`
		Location int    `json:"location"`
		Name     string `json:"name"`
	}
	if err = json.Unmarshal([]byte(encoded), &areas); err != nil {
		return nil, fmt.Errorf("decode preset list: %w", err)
	}

	presets := make([]xiaomiPreset, 0, len(areas))
	for _, area := range areas {
		location := area.Location
		if location == 0 {
			location = area.Index
		}
		if location == 0 {
			continue
		}
		name := area.Name
		if decoded, decodeErr := base64.StdEncoding.DecodeString(area.Name); decodeErr == nil {
			name = string(decoded)
		}
		presets = append(presets, xiaomiPreset{Token: strconv.Itoa(location), Name: name})
	}
	return presets, nil
}

func gotoCameraPreset(camera *cameraCloud, location int) error {
	params := fmt.Sprintf(`{"params":[{"did":%q,"siid":10,"piid":2,"value":%d}]}`, camera.did, location)
	res, err := cloudRequest(camera.userID, camera.region, "/miotspec/prop/set", params)
	if err != nil {
		return err
	}
	results, err := decodeMIoTResults(res)
	if err != nil {
		return fmt.Errorf("decode goto preset response: %w", err)
	}
	if len(results) == 0 || results[0].Code != 0 {
		return fmt.Errorf("camera rejected preset %d: %s", location, res)
	}
	return nil
}

func decodeMIoTResults(data []byte) ([]miotResult, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errors.New("empty MIoT response")
	}
	if data[0] == '[' {
		var results []miotResult
		if err := json.Unmarshal(data, &results); err != nil {
			return nil, err
		}
		return results, nil
	}
	var response struct {
		Result []miotResult `json:"result"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	return response.Result, nil
}
