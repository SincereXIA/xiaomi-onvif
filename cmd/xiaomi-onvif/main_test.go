package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRequestAction(t *testing.T) {
	body := []byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tptz:Stop xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"/></s:Body></s:Envelope>`)
	action, err := requestAction(body)
	if err != nil || action != "Stop" {
		t.Fatalf("action=%q err=%v", action, err)
	}
}

func TestParseCameraMapping(t *testing.T) {
	for _, test := range []struct {
		value  string
		name   string
		listen string
		stream string
	}{
		{"xm=:8891", "xm", ":8891", "xm_4k"},
		{"living=127.0.0.1:8892,living_hd", "living", "127.0.0.1:8892", "living_hd"},
		{"door=:8893,rtsp://media:8554/door", "door", ":8893", "rtsp://media:8554/door"},
	} {
		name, listen, stream, err := parseCameraMapping(test.value)
		if err != nil || name != test.name || listen != test.listen || stream != test.stream {
			t.Fatalf("parseCameraMapping(%q)=(%q,%q,%q,%v)", test.value, name, listen, stream, err)
		}
	}
	if _, _, _, err := parseCameraMapping("missing"); err == nil {
		t.Fatal("invalid mapping should fail")
	}
}

func TestPublicHostValidation(t *testing.T) {
	for _, host := range []string{"127.0.0.1:8891", "camera.local:8891", "[::1]:8891"} {
		if !validPublicHost(host) {
			t.Fatalf("expected valid host %q", host)
		}
	}
	for _, host := range []string{"", "camera.local/path", "camera.local<bad>", "camera.local\r\nInjected: true"} {
		if validPublicHost(host) {
			t.Fatalf("expected invalid host %q", host)
		}
	}
}

func TestMovementDirection(t *testing.T) {
	tests := []struct {
		xml  string
		want string
	}{
		{`<s:Envelope><s:Body><ContinuousMove><Velocity><PanTilt x="-0.5" y="0"/></Velocity></ContinuousMove></s:Body></s:Envelope>`, "left"},
		{`<s:Envelope><s:Body><ContinuousMove><Velocity><PanTilt x="0.5" y="0"/></Velocity></ContinuousMove></s:Body></s:Envelope>`, "right"},
		{`<s:Envelope><s:Body><ContinuousMove><Velocity><PanTilt x="0" y="0.5"/></Velocity></ContinuousMove></s:Body></s:Envelope>`, "up"},
		{`<s:Envelope><s:Body><ContinuousMove><Velocity><PanTilt x="0" y="-0.5"/></Velocity></ContinuousMove></s:Body></s:Envelope>`, "down"},
	}
	for _, test := range tests {
		got, err := movementDirection([]byte(test.xml))
		if err != nil || got != test.want {
			t.Fatalf("got=%q want=%q err=%v", got, test.want, err)
		}
	}
}

func TestContinuousMoveAndStop(t *testing.T) {
	var mu sync.Mutex
	var directions []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		directions = append(directions, r.URL.Query().Get("direction"))
		mu.Unlock()
		if r.URL.Query().Get("direction") != "stop" {
			time.Sleep(10 * time.Millisecond)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	p := &proxy{name: "xm", go2rtcURL: backend.URL, httpClient: backend.Client()}
	server := httptest.NewServer(p)
	defer server.Close()

	move := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tptz:ContinuousMove xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"><tptz:Velocity><tt:PanTilt xmlns:tt="http://www.onvif.org/ver10/schema" x="-0.5" y="0"/></tptz:Velocity></tptz:ContinuousMove></s:Body></s:Envelope>`
	response, err := http.Post(server.URL+"/onvif/ptz_service", "application/soap+xml", strings.NewReader(move))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	time.Sleep(30 * time.Millisecond)

	stop := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tptz:Stop xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"/></s:Body></s:Envelope>`
	response, err = http.Post(server.URL+"/onvif/ptz_service", "application/soap+xml", strings.NewReader(stop))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(directions) < 2 || directions[0] != "left" || directions[len(directions)-1] != "stop" {
		t.Fatalf("directions=%v", directions)
	}
}

func TestGetProfilesIncludesPTZ(t *testing.T) {
	p := &proxy{name: "xm"}
	response := p.getProfiles()
	for _, required := range []string{"VideoEncoderConfiguration", "PTZConfiguration", "DefaultContinuousPanTiltVelocitySpace"} {
		if !strings.Contains(response, required) {
			t.Fatalf("profile response missing %s", required)
		}
	}
}

func TestAutotrackingCapabilities(t *testing.T) {
	p := &proxy{name: "xm2"}
	for name, response := range map[string]string{
		"configuration options": p.getConfigurationOptions(),
		"node":                  p.getNodes(),
	} {
		if !strings.Contains(response, "TranslationSpaceFov") {
			t.Fatalf("%s does not advertise FOV relative movement", name)
		}
	}
}

func TestRelativeMoveStatus(t *testing.T) {
	var mu sync.Mutex
	var directions []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		directions = append(directions, r.URL.Query().Get("direction"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	p := &proxy{name: "xm2", go2rtcURL: backend.URL, httpClient: backend.Client(), status: "IDLE"}
	body := []byte(`<RelativeMove><Translation><PanTilt x="0.05" y="-0.05"/></Translation></RelativeMove>`)
	x, y, err := panTilt(body)
	if err != nil {
		t.Fatal(err)
	}
	p.startRelativeMove(x, y)
	if status := p.moveStatus(); status != "MOVING" {
		t.Fatalf("status immediately after move = %s", status)
	}
	time.Sleep(videoSettleDuration + 150*time.Millisecond)
	if status := p.moveStatus(); status != "IDLE" {
		t.Fatalf("status after settle = %s", status)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(directions) != 2 || directions[0] != "right" || directions[1] != "down" {
		t.Fatalf("directions=%v", directions)
	}
}

func TestPresetsBridge(t *testing.T) {
	var gotoToken string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/xiaomi/ptz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodPost {
			gotoToken = r.URL.Query().Get("token")
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode([]preset{{Token: "1", Name: "default"}, {Token: "2", Name: "default2"}})
	}))
	defer backend.Close()

	p := &proxy{name: "xm2", go2rtcURL: backend.URL, httpClient: backend.Client(), presetClient: backend.Client()}
	response, err := p.getPresets()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`token="1"`, `default`, `token="2"`, `default2`} {
		if !strings.Contains(response, value) {
			t.Fatalf("preset response missing %s: %s", value, response)
		}
	}
	if err = p.gotoPreset("1"); err != nil {
		t.Fatal(err)
	}
	if status := p.moveStatus(); status != "MOVING" {
		t.Fatalf("status after goto preset=%s", status)
	}
	if gotoToken != "1" {
		t.Fatalf("goto token=%q", gotoToken)
	}
}

func TestPresetToken(t *testing.T) {
	body := []byte(`<GotoPreset><ProfileToken>xm2</ProfileToken><PresetToken>1</PresetToken></GotoPreset>`)
	token, err := presetToken(body)
	if err != nil || token != "1" {
		t.Fatalf("token=%q err=%v", token, err)
	}
}

func TestRewriteRelativeMoveForGenericSpace(t *testing.T) {
	body := []byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema"><s:Body><tptz:RelativeMove><tptz:Translation><tt:PanTilt x="0.05" y="-0.05" space="http://www.onvif.org/ver10/tptz/PanTiltSpaces/TranslationSpaceFov"/></tptz:Translation><tptz:Speed><tt:PanTilt x="1" y="1"/></tptz:Speed></tptz:RelativeMove></s:Body></s:Envelope>`)
	rewritten, err := rewriteRelativeMove(body, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	result := string(rewritten)
	for _, value := range []string{`x="0.100000"`, `y="-0.150000"`, `TranslationGenericSpace`, `<tptz:Speed><tt:PanTilt x="1" y="1"`} {
		if !strings.Contains(result, value) {
			t.Fatalf("rewritten request missing %q: %s", value, result)
		}
	}
	if strings.Contains(result, "TranslationSpaceFov") {
		t.Fatalf("rewritten request still advertises FOV space: %s", result)
	}
}

func TestNormalizeTPLinkMoveStatus(t *testing.T) {
	body := []byte(`<tptz:GetStatusResponse><tptz:PTZStatus><tt:Position><tt:Zoom x="0"/></tt:Position><tt:MoveStatus><tt:PanTilt>idle</tt:PanTilt><tt:Zoom>unknown</tt:Zoom></tt:MoveStatus></tptz:PTZStatus></tptz:GetStatusResponse>`)
	result := string(normalizeMoveStatus(body))
	for _, value := range []string{`<tt:PanTilt>IDLE</tt:PanTilt>`, `<tt:Zoom>IDLE</tt:Zoom>`, `<tt:Zoom x="0"/>`} {
		if !strings.Contains(result, value) {
			t.Fatalf("normalized response missing %q: %s", value, result)
		}
	}
}

func TestONVIFCompatProxyCapabilitiesAndForwarding(t *testing.T) {
	var forwardedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		forwardedBody = string(body)
		w.Header().Set("Content-Type", "application/soap+xml")
		_, _ = io.WriteString(w, envelope(`<tptz:GetStatusResponse><tptz:PTZStatus><tt:MoveStatus><tt:PanTilt>moving</tt:PanTilt><tt:Zoom>unknown</tt:Zoom></tt:MoveStatus></tptz:PTZStatus></tptz:GetStatusResponse>`))
	}))
	defer upstream.Close()

	p := &onvifCompatProxy{
		name:       "tplink",
		deviceURL:  upstream.URL,
		serviceURL: upstream.URL,
		panGain:    1,
		tiltGain:   1,
		httpClient: upstream.Client(),
	}
	server := httptest.NewServer(p)
	defer server.Close()

	statusRequest := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tptz:GetStatus xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"/></s:Body></s:Envelope>`
	response, err := http.Post(server.URL+"/onvif/ptz_service", "application/soap+xml", strings.NewReader(statusRequest))
	if err != nil {
		t.Fatal(err)
	}
	statusBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(statusBody), `<tt:PanTilt>MOVING</tt:PanTilt>`) || !strings.Contains(string(statusBody), `<tt:Zoom>IDLE</tt:Zoom>`) {
		t.Fatalf("unexpected normalized status: %s", statusBody)
	}
	if !strings.Contains(forwardedBody, "GetStatus") {
		t.Fatalf("request was not forwarded: %s", forwardedBody)
	}

	capabilitiesRequest := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tptz:GetServiceCapabilities xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"/></s:Body></s:Envelope>`
	response, err = http.Post(server.URL+"/onvif/ptz_service", "application/soap+xml", strings.NewReader(capabilitiesRequest))
	if err != nil {
		t.Fatal(err)
	}
	capabilitiesBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(capabilitiesBody), `MoveStatus="true"`) {
		t.Fatalf("compatibility capabilities missing MoveStatus: %s", capabilitiesBody)
	}
}

func TestONVIFCompatSyntheticMovementWindow(t *testing.T) {
	p := &onvifCompatProxy{}
	p.markMoving(40 * time.Millisecond)
	if !p.syntheticMoving() {
		t.Fatal("movement window should be active")
	}
	body := []byte(`<tt:MoveStatus><tt:PanTilt>IDLE</tt:PanTilt><tt:Zoom>IDLE</tt:Zoom></tt:MoveStatus>`)
	result := string(setPanTiltMoveStatus(body, "MOVING"))
	if !strings.Contains(result, `<tt:PanTilt>MOVING</tt:PanTilt>`) {
		t.Fatalf("movement status was not overridden: %s", result)
	}
	time.Sleep(50 * time.Millisecond)
	if p.syntheticMoving() {
		t.Fatal("movement window should have expired")
	}
	p.markMoving(time.Second)
	p.clearMoving()
	if p.syntheticMoving() {
		t.Fatal("movement window should be cleared by Stop")
	}
}

func TestONVIFCompatZeroRelativeMove(t *testing.T) {
	forwarded := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	p := &onvifCompatProxy{deviceURL: upstream.URL, serviceURL: upstream.URL, panGain: 0.48, tiltGain: 1.23, httpClient: upstream.Client()}
	server := httptest.NewServer(p)
	defer server.Close()
	body := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tptz:RelativeMove xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"><tptz:Translation><tt:PanTilt xmlns:tt="http://www.onvif.org/ver10/schema" x="0" y="0"/></tptz:Translation></tptz:RelativeMove></s:Body></s:Envelope>`
	response, err := http.Post(server.URL+"/onvif/ptz_service", "application/soap+xml", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(responseBody), "RelativeMoveResponse") {
		t.Fatalf("unexpected zero move response: status=%d body=%s", response.StatusCode, responseBody)
	}
	if forwarded {
		t.Fatal("zero RelativeMove should not be forwarded to TP-Link")
	}
	if !p.syntheticMoving() {
		t.Fatal("zero RelativeMove should create the calibration baseline window")
	}
}
