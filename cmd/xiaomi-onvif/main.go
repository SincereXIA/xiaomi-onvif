package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	segmentDuration      = 150 * time.Millisecond
	requestTimeout       = 2 * time.Second
	presetRequestTimeout = 10 * time.Second
	relativeDeadband     = 0.02
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"

	panDurationPerFOV    = 900 * time.Millisecond
	tiltDurationPerFOV   = 700 * time.Millisecond
	videoSettleDuration  = 400 * time.Millisecond
	presetSettleDuration = 2500 * time.Millisecond
	upstreamPresetSettle = 8 * time.Second
	relativeSettleBase   = 1500 * time.Millisecond
	relativeSettlePerFOV = 750 * time.Millisecond
	profileWidth         = 3840
	profileHeight        = 2160
	profileFPS           = 20
	profileCodec         = "H264"
)

type cameraFlag []string

func (f *cameraFlag) String() string { return strings.Join(*f, ",") }

func (f *cameraFlag) Set(value string) error {
	if !strings.Contains(value, "=") {
		return errors.New("camera must use NAME=LISTEN_ADDR[,STREAM], for example xm=:8891,xm_4k")
	}
	*f = append(*f, value)
	return nil
}

type onvifCameraFlag []string

func (f *onvifCameraFlag) String() string { return strings.Join(*f, ",") }

func (f *onvifCameraFlag) Set(value string) error {
	if !strings.Contains(value, "=") {
		return errors.New("onvif camera must use NAME=LISTEN_ADDR,DEVICE_URL,SERVICE_URL[,PAN_GAIN,TILT_GAIN]")
	}
	*f = append(*f, value)
	return nil
}

type proxy struct {
	name         string
	listen       string
	go2rtcURL    string
	rtspURL      string
	httpClient   *http.Client
	presetClient *http.Client
	moveMu       sync.Mutex
	moveCancel   context.CancelFunc
	moveDone     chan struct{}
	statusMu     sync.RWMutex
	statusID     uint64
	status       string
}

func main() {
	var cameras cameraFlag
	var onvifCameras onvifCameraFlag
	var showVersion bool
	flag.Var(&cameras, "camera", "Xiaomi camera mapping NAME=LISTEN_ADDR[,STREAM]; may be repeated")
	flag.Var(&onvifCameras, "compat-camera", "upstream ONVIF compatibility mapping NAME=LISTEN_ADDR,DEVICE_URL,SERVICE_URL[,PAN_GAIN,TILT_GAIN]; may be repeated")
	flag.Var(&onvifCameras, "onvif-camera", "deprecated alias for -compat-camera")
	go2rtcURL := flag.String("go2rtc", "http://127.0.0.1:1984", "go2rtc HTTP base URL")
	rtspBase := flag.String("rtsp", "rtsp://127.0.0.1:8654", "RTSP base URL returned by ONVIF")
	flag.DurationVar(&panDurationPerFOV, "pan-duration-per-fov", panDurationPerFOV, "Xiaomi horizontal movement duration for one field of view")
	flag.DurationVar(&tiltDurationPerFOV, "tilt-duration-per-fov", tiltDurationPerFOV, "Xiaomi vertical movement duration for one field of view")
	flag.DurationVar(&videoSettleDuration, "video-settle", videoSettleDuration, "Xiaomi relative-move video settle time")
	flag.DurationVar(&presetSettleDuration, "preset-settle", presetSettleDuration, "Xiaomi preset settle time")
	flag.DurationVar(&upstreamPresetSettle, "compat-preset-settle", upstreamPresetSettle, "compatibility proxy preset settle time")
	flag.DurationVar(&relativeSettleBase, "compat-relative-settle-base", relativeSettleBase, "compatibility proxy minimum relative-move time")
	flag.DurationVar(&relativeSettlePerFOV, "compat-relative-settle-per-fov", relativeSettlePerFOV, "compatibility proxy additional settle time per field of view")
	flag.IntVar(&profileWidth, "profile-width", profileWidth, "ONVIF video profile width advertised for Xiaomi cameras")
	flag.IntVar(&profileHeight, "profile-height", profileHeight, "ONVIF video profile height advertised for Xiaomi cameras")
	flag.IntVar(&profileFPS, "profile-fps", profileFPS, "ONVIF video profile frame rate advertised for Xiaomi cameras")
	flag.StringVar(&profileCodec, "profile-codec", profileCodec, "ONVIF video profile codec advertised for Xiaomi cameras (H264 or H265)")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Printf("xiaomi-onvif %s (commit=%s date=%s)\n", version, commit, date)
		return
	}
	profileCodec = strings.ToUpper(strings.TrimSpace(profileCodec))
	if profileWidth < 1 || profileHeight < 1 || profileFPS < 1 || profileFPS > 120 || (profileCodec != "H264" && profileCodec != "H265") {
		log.Fatal("invalid ONVIF profile metadata: width and height must be positive, fps must be 1..120, codec must be H264 or H265")
	}

	if len(cameras) == 0 && len(onvifCameras) == 0 {
		log.Fatal("at least one -camera or -onvif-camera is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	servers := make([]*http.Server, 0, len(cameras)+len(onvifCameras))
	for _, item := range cameras {
		name, listen, stream, err := parseCameraMapping(item)
		if err != nil {
			log.Fatalf("invalid camera mapping %q: %v", item, err)
		}
		rtspURL := stream
		if !strings.Contains(stream, "://") {
			rtspURL = strings.TrimRight(*rtspBase, "/") + "/" + strings.TrimLeft(stream, "/")
		}

		p := &proxy{
			name:         name,
			listen:       listen,
			go2rtcURL:    strings.TrimRight(*go2rtcURL, "/"),
			rtspURL:      rtspURL,
			httpClient:   &http.Client{Timeout: requestTimeout},
			presetClient: &http.Client{Timeout: presetRequestTimeout},
			status:       "IDLE",
		}
		server := &http.Server{
			Addr:              listen,
			Handler:           p,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		servers = append(servers, server)
		go func() {
			log.Printf("Xiaomi ONVIF proxy camera=%s listen=%s", p.name, p.listen)
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("camera=%s server failed: %v", p.name, err)
				stop()
			}
		}()
	}

	for _, item := range onvifCameras {
		p, err := newONVIFCompatProxy(item)
		if err != nil {
			log.Fatalf("invalid ONVIF camera mapping %q: %v", item, err)
		}
		server := &http.Server{
			Addr:              p.listen,
			Handler:           p,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       20 * time.Second,
			WriteTimeout:      20 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		servers = append(servers, server)
		go func() {
			log.Printf("ONVIF compatibility proxy camera=%s listen=%s upstream=%s", p.name, p.listen, p.serviceURL)
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("camera=%s ONVIF compatibility server failed: %v", p.name, err)
				stop()
			}
		}()
	}

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, server := range servers {
		_ = server.Shutdown(shutdownCtx)
	}
}

func parseCameraMapping(value string) (name, listen, stream string, err error) {
	name, raw, ok := strings.Cut(value, "=")
	if !ok {
		return "", "", "", errors.New("missing camera name")
	}
	name = strings.TrimSpace(name)
	parts := strings.SplitN(raw, ",", 2)
	listen = strings.TrimSpace(parts[0])
	if name == "" || listen == "" {
		return "", "", "", errors.New("camera name and listen address are required")
	}
	if len(parts) == 2 {
		stream = strings.TrimSpace(parts[1])
	}
	if stream == "" {
		stream = name + "_4k"
	}
	return name, listen, stream, nil
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"camera": p.name, "status": "ok"})
		return
	}
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/onvif/") {
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	action, err := requestAction(body)
	if err != nil {
		p.soapFault(w, "ter:InvalidArgVal", err.Error())
		return
	}

	host := r.Host
	if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
		host = forwarded
	}

	var response string
	switch action {
	case "GetCapabilities":
		response = p.getCapabilities(host)
	case "GetServices":
		response = p.getServices(host)
	case "GetDeviceInformation":
		response = fmt.Sprintf(`<tds:GetDeviceInformationResponse><tds:Manufacturer>Xiaomi</tds:Manufacturer><tds:Model>go2rtc PTZ proxy</tds:Model><tds:FirmwareVersion>1</tds:FirmwareVersion><tds:SerialNumber>%s</tds:SerialNumber><tds:HardwareId>xiaomi</tds:HardwareId></tds:GetDeviceInformationResponse>`, xmlEscape(p.name))
	case "GetSystemDateAndTime":
		response = systemDateAndTime()
	case "GetProfiles":
		response = p.getProfiles()
	case "GetProfile":
		response = p.getProfile()
	case "GetVideoSources":
		response = p.getVideoSources()
	case "GetStreamUri":
		response = fmt.Sprintf(`<trt:GetStreamUriResponse><trt:MediaUri><tt:Uri>%s</tt:Uri><tt:InvalidAfterConnect>false</tt:InvalidAfterConnect><tt:InvalidAfterReboot>false</tt:InvalidAfterReboot><tt:Timeout>PT0S</tt:Timeout></trt:MediaUri></trt:GetStreamUriResponse>`, xmlEscape(p.rtspURL))
	case "GetPresets":
		response, err = p.getPresets()
		if err != nil {
			p.soapFault(w, "ter:Action", err.Error())
			return
		}
	case "GetServiceCapabilities":
		response = `<tptz:GetServiceCapabilitiesResponse><tptz:Capabilities EFlip="false" Reverse="false" GetCompatibleConfigurations="false" MoveStatus="true" StatusPosition="false"/></tptz:GetServiceCapabilitiesResponse>`
	case "GetConfiguration":
		response = p.getPTZConfiguration("GetConfigurationResponse", "PTZConfiguration")
	case "GetConfigurations":
		response = p.getPTZConfiguration("GetConfigurationsResponse", "PTZConfiguration")
	case "GetConfigurationOptions":
		response = p.getConfigurationOptions()
	case "GetNodes":
		response = p.getNodes()
	case "GetNode":
		response = p.getNode()
	case "GetStatus":
		response = p.getStatus()
	case "ContinuousMove":
		direction, err := movementDirection(body)
		if err != nil {
			p.soapFault(w, "ter:InvalidArgVal", err.Error())
			return
		}
		p.startMove(direction)
		response = `<tptz:ContinuousMoveResponse/>`
	case "RelativeMove":
		x, y, err := panTilt(body)
		if err != nil {
			p.soapFault(w, "ter:InvalidArgVal", err.Error())
			return
		}
		p.startRelativeMove(x, y)
		response = `<tptz:RelativeMoveResponse/>`
	case "GotoPreset":
		token, err := presetToken(body)
		if err != nil {
			p.soapFault(w, "ter:InvalidArgVal", err.Error())
			return
		}
		if err = p.gotoPreset(token); err != nil {
			p.soapFault(w, "ter:Action", err.Error())
			return
		}
		response = `<tptz:GotoPresetResponse/>`
	case "Stop":
		if err := p.stopMove(); err != nil {
			p.soapFault(w, "ter:Action", err.Error())
			return
		}
		response = `<tptz:StopResponse/>`
	default:
		p.soapFault(w, "ter:ActionNotSupported", "unsupported operation "+action)
		return
	}

	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	_, _ = io.WriteString(w, envelope(response))
}

type onvifCompatProxy struct {
	name        string
	listen      string
	deviceURL   string
	serviceURL  string
	panGain     float64
	tiltGain    float64
	httpClient  *http.Client
	statusMu    sync.RWMutex
	movingUntil time.Time
}

func newONVIFCompatProxy(value string) (*onvifCompatProxy, error) {
	name, raw, ok := strings.Cut(value, "=")
	if !ok || strings.TrimSpace(name) == "" {
		return nil, errors.New("missing camera name")
	}
	parts := strings.Split(raw, ",")
	if len(parts) < 3 || len(parts) > 5 {
		return nil, errors.New("expected LISTEN_ADDR,DEVICE_URL,SERVICE_URL[,PAN_GAIN,TILT_GAIN]")
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	for _, rawURL := range parts[1:3] {
		u, err := url.Parse(rawURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("invalid upstream URL %q", rawURL)
		}
	}
	panGain, tiltGain := 1.0, 1.0
	var err error
	if len(parts) >= 4 {
		panGain, err = strconv.ParseFloat(parts[3], 64)
		if err != nil || panGain <= 0 {
			return nil, fmt.Errorf("invalid pan gain %q", parts[3])
		}
	}
	if len(parts) == 5 {
		tiltGain, err = strconv.ParseFloat(parts[4], 64)
		if err != nil || tiltGain <= 0 {
			return nil, fmt.Errorf("invalid tilt gain %q", parts[4])
		}
	}
	return &onvifCompatProxy{
		name:       strings.TrimSpace(name),
		listen:     parts[0],
		deviceURL:  parts[1],
		serviceURL: parts[2],
		panGain:    panGain,
		tiltGain:   tiltGain,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (p *onvifCompatProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"camera": p.name, "status": "ok"})
		return
	}
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/onvif/") {
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	action, err := requestAction(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	compat := &proxy{name: p.name}
	switch action {
	case "GetCapabilities":
		p.writeSOAP(w, compat.getCapabilities(publicHost(r)))
		return
	case "GetServices":
		p.writeSOAP(w, compat.getServices(publicHost(r)))
		return
	case "GetServiceCapabilities":
		if strings.Contains(r.URL.Path, "ptz_service") {
			p.writeSOAP(w, `<tptz:GetServiceCapabilitiesResponse><tptz:Capabilities EFlip="false" Reverse="false" GetCompatibleConfigurations="false" MoveStatus="true" StatusPosition="true"/></tptz:GetServiceCapabilitiesResponse>`)
			return
		}
	}

	minimumMoving := time.Duration(0)
	if action == "RelativeMove" {
		x, y, parseErr := panTilt(body)
		if parseErr != nil {
			http.Error(w, parseErr.Error(), http.StatusBadRequest)
			return
		}
		minimumMoving = relativeSettleBase + time.Duration((math.Abs(x)+math.Abs(y))*float64(relativeSettlePerFOV))
		if math.Abs(x) < 1e-6 && math.Abs(y) < 1e-6 {
			p.markMoving(minimumMoving)
			p.writeSOAP(w, `<tptz:RelativeMoveResponse/>`)
			return
		}
		body, err = rewriteRelativeMove(body, p.panGain, p.tiltGain)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	responseBody, response, err := p.forward(r, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode/100 == 2 {
		switch action {
		case "RelativeMove":
			p.markMoving(minimumMoving)
		case "GotoPreset":
			p.markMoving(upstreamPresetSettle)
		case "Stop":
			p.clearMoving()
		}
	}

	switch action {
	case "GetProfiles", "GetProfile", "GetConfiguration", "GetConfigurations", "GetConfigurationOptions", "GetNodes", "GetNode":
		responseBody = advertiseFOVSpace(responseBody)
	case "GetStatus":
		responseBody = normalizeMoveStatus(responseBody)
		if p.syntheticMoving() {
			responseBody = setPanTiltMoveStatus(responseBody, "MOVING")
		}
	}

	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	} else {
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}

func (p *onvifCompatProxy) markMoving(duration time.Duration) {
	p.statusMu.Lock()
	p.movingUntil = time.Now().Add(duration)
	p.statusMu.Unlock()
}

func (p *onvifCompatProxy) clearMoving() {
	p.statusMu.Lock()
	p.movingUntil = time.Time{}
	p.statusMu.Unlock()
}

func (p *onvifCompatProxy) syntheticMoving() bool {
	p.statusMu.RLock()
	until := p.movingUntil
	p.statusMu.RUnlock()
	return time.Now().Before(until)
}

func publicHost(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
		forwarded = strings.TrimSpace(strings.Split(forwarded, ",")[0])
		if validPublicHost(forwarded) {
			return forwarded
		}
	}
	return r.Host
}

func validPublicHost(host string) bool {
	if host == "" || len(host) > 255 || strings.ContainsAny(host, "<>\"'\\/\r\n\t ") {
		return false
	}
	u, err := url.Parse("http://" + host)
	return err == nil && u.Host == host && u.Hostname() != ""
}

func (p *onvifCompatProxy) writeSOAP(w http.ResponseWriter, response string) {
	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	_, _ = io.WriteString(w, envelope(response))
}

func (p *onvifCompatProxy) forward(r *http.Request, body []byte) ([]byte, *http.Response, error) {
	target := p.serviceURL
	if strings.Contains(r.URL.Path, "device_service") {
		target = p.deviceURL
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	request.Header = r.Header.Clone()
	request.Header.Del("Accept-Encoding")
	response, err := p.httpClient.Do(request)
	if err != nil {
		return nil, nil, err
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		response.Body.Close()
		return nil, nil, err
	}
	return responseBody, response, nil
}

var (
	panTiltTagPattern     = regexp.MustCompile(`<(?:(?:[A-Za-z_][A-Za-z0-9_.-]*):)?PanTilt\b[^>]*>`)
	xAttributePattern     = regexp.MustCompile(`\bx="[^"]*"`)
	yAttributePattern     = regexp.MustCompile(`\by="[^"]*"`)
	spaceAttributePattern = regexp.MustCompile(`\bspace="[^"]*"`)
	zoomStatusPattern     = regexp.MustCompile(`(?i)<((?:[A-Za-z_][A-Za-z0-9_.-]*:)?Zoom)>[^<]*</((?:[A-Za-z_][A-Za-z0-9_.-]*:)?Zoom)>`)
	panTiltStatusPattern  = regexp.MustCompile(`(?i)<((?:[A-Za-z_][A-Za-z0-9_.-]*:)?PanTilt)>[^<]*</((?:[A-Za-z_][A-Za-z0-9_.-]*:)?PanTilt)>`)
)

func rewriteRelativeMove(body []byte, panGain, tiltGain float64) ([]byte, error) {
	x, y, err := panTilt(body)
	if err != nil {
		return nil, err
	}
	x = math.Max(-1, math.Min(1, x*panGain))
	y = math.Max(-1, math.Min(1, y*tiltGain))

	translationIndex := bytes.Index(body, []byte("Translation"))
	if translationIndex < 0 {
		return nil, errors.New("RelativeMove request has no Translation")
	}
	location := panTiltTagPattern.FindIndex(body[translationIndex:])
	if location == nil {
		return nil, errors.New("RelativeMove Translation has no PanTilt")
	}
	start := translationIndex + location[0]
	end := translationIndex + location[1]
	tag := string(body[start:end])
	tag = replaceXMLAttribute(tag, xAttributePattern, "x", strconv.FormatFloat(x, 'f', 6, 64))
	tag = replaceXMLAttribute(tag, yAttributePattern, "y", strconv.FormatFloat(y, 'f', 6, 64))
	tag = replaceXMLAttribute(tag, spaceAttributePattern, "space", "http://www.onvif.org/ver10/tptz/PanTiltSpaces/TranslationGenericSpace")

	result := make([]byte, 0, len(body)+len(tag)-(end-start))
	result = append(result, body[:start]...)
	result = append(result, tag...)
	result = append(result, body[end:]...)
	return bytes.ReplaceAll(result, []byte("TranslationSpaceFov"), []byte("TranslationGenericSpace")), nil
}

func replaceXMLAttribute(tag string, pattern *regexp.Regexp, name, value string) string {
	replacement := name + `="` + value + `"`
	if pattern.MatchString(tag) {
		return pattern.ReplaceAllString(tag, replacement)
	}
	if strings.HasSuffix(tag, "/>") {
		return strings.TrimSuffix(tag, "/>") + " " + replacement + "/>"
	}
	return strings.TrimSuffix(tag, ">") + " " + replacement + ">"
}

func advertiseFOVSpace(body []byte) []byte {
	return bytes.ReplaceAll(body, []byte("TranslationGenericSpace"), []byte("TranslationSpaceFov"))
}

func normalizeMoveStatus(body []byte) []byte {
	result := bytes.ReplaceAll(body, []byte(">idle<"), []byte(">IDLE<"))
	result = bytes.ReplaceAll(result, []byte(">moving<"), []byte(">MOVING<"))
	result = bytes.ReplaceAll(result, []byte(">Idle<"), []byte(">IDLE<"))
	result = bytes.ReplaceAll(result, []byte(">Moving<"), []byte(">MOVING<"))
	// TP-Link reports Zoom=unknown even though this camera has no optical zoom.
	// Frigate waits for both axes to become IDLE, so expose zoom as settled.
	result = zoomStatusPattern.ReplaceAll(result, []byte(`<${1}>IDLE</${2}>`))
	return result
}

func setPanTiltMoveStatus(body []byte, status string) []byte {
	replacement := []byte(`<${1}>` + status + `</${2}>`)
	return panTiltStatusPattern.ReplaceAll(body, replacement)
}

func requestAction(body []byte) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(body)))
	inBody := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("invalid SOAP XML: %w", err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			if element.Name.Local == "Body" {
				inBody = true
				continue
			}
			if inBody {
				return element.Name.Local, nil
			}
		case xml.EndElement:
			if element.Name.Local == "Body" {
				inBody = false
			}
		}
	}
	return "", errors.New("SOAP body has no operation")
}

func movementDirection(body []byte) (string, error) {
	x, y, err := panTilt(body)
	if err != nil {
		return "", err
	}
	if math.Abs(x) < 0.001 && math.Abs(y) < 0.001 {
		return "stop", nil
	}
	if math.Abs(x) >= math.Abs(y) {
		if x < 0 {
			return "left", nil
		}
		return "right", nil
	}
	if y > 0 {
		return "up", nil
	}
	return "down", nil
}

func panTilt(body []byte) (float64, float64, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(body)))
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, 0, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "PanTilt" {
			continue
		}
		var x, y float64
		var hasX, hasY bool
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "x":
				x, err = strconv.ParseFloat(attr.Value, 64)
				hasX = err == nil
			case "y":
				y, err = strconv.ParseFloat(attr.Value, 64)
				hasY = err == nil
			}
		}
		if !hasX && !hasY {
			return 0, 0, errors.New("request has no PanTilt coordinates")
		}
		return x, y, nil
	}
	return 0, 0, errors.New("request has no PanTilt coordinates")
}

func (p *proxy) startMove(direction string) {
	if direction == "stop" {
		_ = p.stopMove()
		return
	}

	p.moveMu.Lock()
	p.stopMoveLocked()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	p.moveCancel = cancel
	p.moveDone = done
	p.moveMu.Unlock()
	moveID := p.beginMoveStatus()

	go func() {
		defer close(done)
		defer p.finishMoveStatus(moveID)
		for ctx.Err() == nil {
			if err := p.sendPTZ(ctx, direction, segmentDuration); err != nil && ctx.Err() == nil {
				log.Printf("camera=%s direction=%s failed: %v", p.name, direction, err)
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()
}

func (p *proxy) startRelativeMove(x, y float64) {
	if math.Abs(x) < relativeDeadband && math.Abs(y) < relativeDeadband {
		_ = p.stopMove()
		return
	}

	p.moveMu.Lock()
	p.stopMoveLocked()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	p.moveCancel = cancel
	p.moveDone = done
	p.moveMu.Unlock()
	moveID := p.beginMoveStatus()

	go func() {
		defer close(done)
		if err := p.runRelativeMove(ctx, x, y); err != nil && ctx.Err() == nil {
			log.Printf("camera=%s relative x=%.3f y=%.3f failed: %v", p.name, x, y, err)
		}
		if ctx.Err() == nil {
			timer := time.NewTimer(videoSettleDuration)
			select {
			case <-timer.C:
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
			}
		}
		p.finishMoveStatus(moveID)
	}()
}

func (p *proxy) runRelativeMove(ctx context.Context, x, y float64) error {
	if math.Abs(x) >= relativeDeadband {
		direction := "right"
		if x < 0 {
			direction = "left"
		}
		duration := time.Duration(math.Abs(x) * float64(panDurationPerFOV))
		if duration < time.Millisecond {
			duration = time.Millisecond
		}
		if err := p.sendPTZ(ctx, direction, duration); err != nil {
			return err
		}
	}
	if math.Abs(y) >= relativeDeadband {
		direction := "up"
		if y < 0 {
			direction = "down"
		}
		duration := time.Duration(math.Abs(y) * float64(tiltDurationPerFOV))
		if duration < time.Millisecond {
			duration = time.Millisecond
		}
		if err := p.sendPTZ(ctx, direction, duration); err != nil {
			return err
		}
	}
	return nil
}

func (p *proxy) stopMove() error {
	p.moveMu.Lock()
	p.stopMoveLocked()
	p.moveMu.Unlock()
	err := p.sendPTZ(context.Background(), "stop", 0)
	p.forceIdleStatus()
	return err
}

func (p *proxy) stopMoveLocked() {
	if p.moveCancel == nil {
		return
	}
	p.moveCancel()
	done := p.moveDone
	p.moveCancel = nil
	p.moveDone = nil
	select {
	case <-done:
	case <-time.After(requestTimeout):
		log.Printf("camera=%s timed out waiting for prior movement", p.name)
	}
}

func (p *proxy) sendPTZ(ctx context.Context, direction string, duration time.Duration) error {
	endpoint, err := url.Parse(p.go2rtcURL + "/api/xiaomi/ptz")
	if err != nil {
		return err
	}
	query := endpoint.Query()
	query.Set("src", p.name)
	query.Set("direction", direction)
	if duration > 0 {
		query.Set("duration", strconv.FormatInt(duration.Milliseconds(), 10))
	}
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return err
	}
	response, err := p.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("go2rtc returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	return nil
}

func (p *proxy) beginMoveStatus() uint64 {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	p.statusID++
	p.status = "MOVING"
	return p.statusID
}

func (p *proxy) finishMoveStatus(id uint64) {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	if p.statusID == id {
		p.status = "IDLE"
	}
}

func (p *proxy) forceIdleStatus() {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	p.statusID++
	p.status = "IDLE"
}

func (p *proxy) moveStatus() string {
	p.statusMu.RLock()
	defer p.statusMu.RUnlock()
	if p.status == "" {
		return "IDLE"
	}
	return p.status
}

func (p *proxy) getCapabilities(host string) string {
	base := "http://" + xmlEscape(host) + "/onvif/"
	return fmt.Sprintf(`<tds:GetCapabilitiesResponse><tds:Capabilities><tt:Device><tt:XAddr>%sdevice_service</tt:XAddr></tt:Device><tt:Media><tt:XAddr>%smedia_service</tt:XAddr><tt:StreamingCapabilities><tt:RTPMulticast>false</tt:RTPMulticast><tt:RTP_TCP>false</tt:RTP_TCP><tt:RTP_RTSP_TCP>true</tt:RTP_RTSP_TCP></tt:StreamingCapabilities></tt:Media><tt:PTZ><tt:XAddr>%sptz_service</tt:XAddr></tt:PTZ></tds:Capabilities></tds:GetCapabilitiesResponse>`, base, base, base)
}

func (p *proxy) getServices(host string) string {
	base := "http://" + xmlEscape(host) + "/onvif/"
	return fmt.Sprintf(`<tds:GetServicesResponse><tds:Service><tds:Namespace>http://www.onvif.org/ver10/device/wsdl</tds:Namespace><tds:XAddr>%sdevice_service</tds:XAddr><tds:Version><tt:Major>2</tt:Major><tt:Minor>5</tt:Minor></tds:Version></tds:Service><tds:Service><tds:Namespace>http://www.onvif.org/ver10/media/wsdl</tds:Namespace><tds:XAddr>%smedia_service</tds:XAddr><tds:Version><tt:Major>2</tt:Major><tt:Minor>5</tt:Minor></tds:Version></tds:Service><tds:Service><tds:Namespace>http://www.onvif.org/ver20/ptz/wsdl</tds:Namespace><tds:XAddr>%sptz_service</tds:XAddr><tds:Version><tt:Major>2</tt:Major><tt:Minor>5</tt:Minor></tds:Version></tds:Service></tds:GetServicesResponse>`, base, base, base)
}

func (p *proxy) getProfiles() string {
	return `<trt:GetProfilesResponse>` + p.profile("Profiles") + `</trt:GetProfilesResponse>`
}

func (p *proxy) getProfile() string {
	return `<trt:GetProfileResponse>` + p.profile("Profile") + `</trt:GetProfileResponse>`
}

func (p *proxy) profile(tag string) string {
	name := xmlEscape(p.name)
	return fmt.Sprintf(`<trt:%s token="%s" fixed="true"><tt:Name>%s</tt:Name><tt:VideoSourceConfiguration token="%s_source"><tt:Name>%s source</tt:Name><tt:UseCount>1</tt:UseCount><tt:SourceToken>%s_source</tt:SourceToken><tt:Bounds x="0" y="0" width="%d" height="%d"/></tt:VideoSourceConfiguration><tt:VideoEncoderConfiguration token="%s_encoder"><tt:Name>%s %s</tt:Name><tt:UseCount>1</tt:UseCount><tt:Encoding>%s</tt:Encoding><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution><tt:Quality>5</tt:Quality><tt:RateControl><tt:FrameRateLimit>%d</tt:FrameRateLimit><tt:EncodingInterval>1</tt:EncodingInterval><tt:BitrateLimit>4096</tt:BitrateLimit></tt:RateControl><tt:SessionTimeout>PT60S</tt:SessionTimeout></tt:VideoEncoderConfiguration>%s</trt:%s>`, tag, name, name, name, name, name, profileWidth, profileHeight, name, name, profileCodec, profileCodec, profileWidth, profileHeight, profileFPS, p.ptzConfiguration("PTZConfiguration"), tag)
}

func (p *proxy) ptzConfiguration(tag string) string {
	name := xmlEscape(p.name)
	return fmt.Sprintf(`<tt:%s token="%s_ptz"><tt:Name>%s PTZ</tt:Name><tt:UseCount>1</tt:UseCount><tt:NodeToken>%s_node</tt:NodeToken><tt:DefaultRelativePanTiltTranslationSpace>http://www.onvif.org/ver10/tptz/PanTiltSpaces/TranslationSpaceFov</tt:DefaultRelativePanTiltTranslationSpace><tt:DefaultContinuousPanTiltVelocitySpace>http://www.onvif.org/ver10/tptz/PanTiltSpaces/VelocityGenericSpace</tt:DefaultContinuousPanTiltVelocitySpace><tt:DefaultPTZSpeed><tt:PanTilt x="0.5" y="0.5" space="http://www.onvif.org/ver10/tptz/PanTiltSpaces/GenericSpeedSpace"/></tt:DefaultPTZSpeed><tt:DefaultPTZTimeout>PT5S</tt:DefaultPTZTimeout><tt:PanTiltLimits><tt:Range><tt:URI>http://www.onvif.org/ver10/tptz/PanTiltSpaces/PositionGenericSpace</tt:URI><tt:XRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:XRange><tt:YRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:YRange></tt:Range></tt:PanTiltLimits></tt:%s>`, tag, name, name, name, tag)
}

func (p *proxy) getPTZConfiguration(responseTag, configTag string) string {
	return fmt.Sprintf(`<tptz:%s>%s</tptz:%s>`, responseTag, p.ptzConfiguration(configTag), responseTag)
}

func (p *proxy) getConfigurationOptions() string {
	return `<tptz:GetConfigurationOptionsResponse><tptz:PTZConfigurationOptions><tt:Spaces><tt:RelativePanTiltTranslationSpace><tt:URI>http://www.onvif.org/ver10/tptz/PanTiltSpaces/TranslationSpaceFov</tt:URI><tt:XRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:XRange><tt:YRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:YRange></tt:RelativePanTiltTranslationSpace><tt:ContinuousPanTiltVelocitySpace><tt:URI>http://www.onvif.org/ver10/tptz/PanTiltSpaces/VelocityGenericSpace</tt:URI><tt:XRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:XRange><tt:YRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:YRange></tt:ContinuousPanTiltVelocitySpace></tt:Spaces><tt:PTZTimeout><tt:Min>PT0S</tt:Min><tt:Max>PT10S</tt:Max></tt:PTZTimeout></tptz:PTZConfigurationOptions></tptz:GetConfigurationOptionsResponse>`
}

func (p *proxy) getNodes() string {
	return `<tptz:GetNodesResponse>` + p.node("PTZNode") + `</tptz:GetNodesResponse>`
}

func (p *proxy) getNode() string {
	return `<tptz:GetNodeResponse>` + p.node("PTZNode") + `</tptz:GetNodeResponse>`
}

func (p *proxy) node(tag string) string {
	name := xmlEscape(p.name)
	return fmt.Sprintf(`<tptz:%s token="%s_node"><tt:Name>%s PTZ</tt:Name><tt:SupportedPTZSpaces><tt:RelativePanTiltTranslationSpace><tt:URI>http://www.onvif.org/ver10/tptz/PanTiltSpaces/TranslationSpaceFov</tt:URI><tt:XRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:XRange><tt:YRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:YRange></tt:RelativePanTiltTranslationSpace><tt:ContinuousPanTiltVelocitySpace><tt:URI>http://www.onvif.org/ver10/tptz/PanTiltSpaces/VelocityGenericSpace</tt:URI><tt:XRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:XRange><tt:YRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:YRange></tt:ContinuousPanTiltVelocitySpace></tt:SupportedPTZSpaces><tt:MaximumNumberOfPresets>16</tt:MaximumNumberOfPresets><tt:HomeSupported>false</tt:HomeSupported></tptz:%s>`, tag, name, name, tag)
}

func (p *proxy) getVideoSources() string {
	name := xmlEscape(p.name)
	return fmt.Sprintf(`<trt:GetVideoSourcesResponse><trt:VideoSources token="%s_source"><tt:Framerate>%d</tt:Framerate><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution></trt:VideoSources></trt:GetVideoSourcesResponse>`, name, profileFPS, profileWidth, profileHeight)
}

func (p *proxy) getStatus() string {
	return fmt.Sprintf(`<tptz:GetStatusResponse><tptz:PTZStatus><tt:Position><tt:PanTilt x="0" y="0" space="http://www.onvif.org/ver10/tptz/PanTiltSpaces/PositionGenericSpace"/></tt:Position><tt:MoveStatus><tt:PanTilt>%s</tt:PanTilt></tt:MoveStatus><tt:UtcTime>%s</tt:UtcTime></tptz:PTZStatus></tptz:GetStatusResponse>`, p.moveStatus(), time.Now().UTC().Format(time.RFC3339))
}

type preset struct {
	Token string `json:"token"`
	Name  string `json:"name"`
}

func (p *proxy) getPresets() (string, error) {
	endpoint, err := url.Parse(p.go2rtcURL + "/api/xiaomi/presets")
	if err != nil {
		return "", err
	}
	query := endpoint.Query()
	query.Set("src", p.name)
	endpoint.RawQuery = query.Encode()

	client := p.presetClient
	if client == nil {
		client = p.httpClient
	}
	response, err := client.Get(endpoint.String())
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return "", fmt.Errorf("go2rtc returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	var presets []preset
	if err = json.NewDecoder(response.Body).Decode(&presets); err != nil {
		return "", fmt.Errorf("decode go2rtc presets: %w", err)
	}

	var result strings.Builder
	result.WriteString(`<tptz:GetPresetsResponse>`)
	for _, item := range presets {
		result.WriteString(fmt.Sprintf(`<tptz:Preset token="%s"><tt:Name>%s</tt:Name><tptz:PTZPosition><tt:PanTilt x="0" y="0"/></tptz:PTZPosition></tptz:Preset>`, xmlEscape(item.Token), xmlEscape(item.Name)))
	}
	result.WriteString(`</tptz:GetPresetsResponse>`)
	return result.String(), nil
}

func presetToken(body []byte) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(body)))
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "PresetToken" {
			continue
		}
		var value string
		if err = decoder.DecodeElement(&value, &start); err != nil {
			return "", err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return "", errors.New("PresetToken is empty")
		}
		return value, nil
	}
	return "", errors.New("GotoPreset has no PresetToken")
}

func (p *proxy) gotoPreset(token string) error {
	p.moveMu.Lock()
	p.stopMoveLocked()
	p.moveMu.Unlock()
	p.forceIdleStatus()

	endpoint, err := url.Parse(p.go2rtcURL + "/api/xiaomi/presets")
	if err != nil {
		return err
	}
	query := endpoint.Query()
	query.Set("src", p.name)
	query.Set("token", token)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequest(http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return err
	}
	client := p.presetClient
	if client == nil {
		client = p.httpClient
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("go2rtc returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	moveID := p.beginMoveStatus()
	go func() {
		time.Sleep(presetSettleDuration)
		p.finishMoveStatus(moveID)
	}()
	return nil
}

func systemDateAndTime() string {
	now := time.Now().UTC()
	return fmt.Sprintf(`<tds:GetSystemDateAndTimeResponse><tds:SystemDateAndTime><tt:DateTimeType>NTP</tt:DateTimeType><tt:DaylightSavings>false</tt:DaylightSavings><tt:UTCDateTime><tt:Time><tt:Hour>%d</tt:Hour><tt:Minute>%d</tt:Minute><tt:Second>%d</tt:Second></tt:Time><tt:Date><tt:Year>%d</tt:Year><tt:Month>%d</tt:Month><tt:Day>%d</tt:Day></tt:Date></tt:UTCDateTime></tds:SystemDateAndTime></tds:GetSystemDateAndTimeResponse>`, now.Hour(), now.Minute(), now.Second(), now.Year(), int(now.Month()), now.Day())
}

func (p *proxy) soapFault(w http.ResponseWriter, code, reason string) {
	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	fault := fmt.Sprintf(`<s:Fault><s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>%s</s:Value></s:Subcode></s:Code><s:Reason><s:Text xml:lang="en">%s</s:Text></s:Reason></s:Fault>`, xmlEscape(code), xmlEscape(reason))
	_, _ = io.WriteString(w, envelope(fault))
}

func envelope(body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema" xmlns:ter="http://www.onvif.org/ver10/error"><s:Body>` + body + `</s:Body></s:Envelope>`
}

func xmlEscape(value string) string {
	var builder strings.Builder
	_ = xml.EscapeText(&builder, []byte(value))
	return builder.String()
}
