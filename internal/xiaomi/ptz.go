package xiaomi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/streams"
)

const (
	ptzRepeatInterval  = 100 * time.Millisecond
	ptzDefaultDuration = 300 * time.Millisecond
	ptzMaxDuration     = 2 * time.Second
)

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

	started := time.Now()
	stream, did, err := ptzStream(src)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	camera := cameraPTZ(did)
	var connectElapsed time.Duration
	if operation == 0 {
		// Stop cancels both pending connections and active timed movements. It
		// must not open a new camera connection merely to stop an idle camera.
		camera.cancelCommands()
		if err = camera.lock(r.Context()); err == nil {
			if controller := connectedPTZ(stream); controller != nil {
				err = controller.SetDirection(0)
			}
			camera.unlock()
		}
	} else {
		ctx, done := camera.command(r.Context())
		defer done()
		var controller directionSetter
		var release func()
		controller, release, err = camera.acquire(ctx, stream)
		connectElapsed = time.Since(started)
		if err == nil {
			defer release()
			if err = camera.lock(ctx); err == nil {
				err = runPTZ(ctx, controller, operation, duration)
				camera.unlock()
			}
		}
	}
	log.Debug().Str("src", src).Str("direction", direction).
		Dur("connect_elapsed", connectElapsed).Dur("elapsed", time.Since(started)).Bool("success", err == nil).Msg("Xiaomi PTZ request")
	if err != nil {
		// Do not include source URLs or cloud/dial errors containing credentials.
		http.Error(w, "Xiaomi PTZ connection or movement failed", http.StatusBadGateway)
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
	if err := ctx.Err(); err != nil {
		return err
	}
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
		if err = ctx.Err(); err != nil {
			return err
		}
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
		log.Debug().Str("src", src).Int("count", len(presets)).Msg("Xiaomi presets discovered")
		api.ResponseJSON(w, presets)
		return
	}

	token := r.URL.Query().Get("token")
	location, err := strconv.Atoi(token)
	if err != nil || location < 0 || location > 64 {
		http.Error(w, "token must be a preset location between 0 and 64", http.StatusBadRequest)
		return
	}
	presets, err := getCameraPresets(camera)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if !hasPreset(presets, token) {
		http.Error(w, "preset token was not discovered for this camera", http.StatusBadRequest)
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

var presetCloudRequest = cloudRequest

func getCameraPresets(camera *cameraCloud) ([]xiaomiPreset, error) {
	params := fmt.Sprintf(`{"params":[{"did":%q,"siid":10,"piid":1}]}`, camera.did)
	res, err := presetCloudRequest(camera.userID, camera.region, "/miotspec/prop/get", params)
	if err != nil {
		return nil, err
	}
	results, err := decodeMIoTResults(res)
	if err != nil {
		return nil, fmt.Errorf("decode preset response: %w", err)
	}
	if len(results) == 0 || results[0].Code != 0 {
		return nil, errors.New("camera rejected preset discovery")
	}

	return parseCameraPresets(results[0].Value)
}

func gotoCameraPreset(camera *cameraCloud, location int) error {
	params := fmt.Sprintf(`{"params":[{"did":%q,"siid":10,"piid":2,"value":%d}]}`, camera.did, location)
	res, err := presetCloudRequest(camera.userID, camera.region, "/miotspec/prop/set", params)
	if err != nil {
		return err
	}
	results, err := decodeMIoTResults(res)
	if err != nil {
		return fmt.Errorf("decode goto preset response: %w", err)
	}
	if len(results) == 0 || results[0].Code != 0 {
		return fmt.Errorf("camera rejected preset %d", location)
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
