package main

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestCameraProfilesPreserveGlobalDefaults(t *testing.T) {
	defaults := videoProfile{2560, 1440, 25, " h265 "}
	profiles, err := resolveCameraProfiles([]string{"living=:8891", "door=:8892,door_hd"}, nil, defaults)
	if err != nil {
		t.Fatal(err)
	}
	for name, profile := range profiles {
		if want := (videoProfile{2560, 1440, 25, "H265"}); profile != want {
			t.Fatalf("camera %s profile=%+v, want %+v", name, profile, want)
		}
	}
}

func TestCameraProfileFieldsInheritIndependently(t *testing.T) {
	defaults := videoProfile{3840, 2160, 20, "H264"}
	for _, test := range []struct {
		fields string
		want   videoProfile
	}{
		{"width=1920", videoProfile{1920, 2160, 20, "H264"}},
		{"height=1080", videoProfile{3840, 1080, 20, "H264"}},
		{"fps=15", videoProfile{3840, 2160, 15, "H264"}},
		{"codec=h265", videoProfile{3840, 2160, 20, "H265"}},
		{"codec=H265,fps=30,height=720,width=1280", videoProfile{1280, 720, 30, "H265"}},
	} {
		t.Run(test.fields, func(t *testing.T) {
			profiles, err := resolveCameraProfiles([]string{"living=:8891", "door=:8892"}, []string{"door=" + test.fields}, defaults)
			if err != nil {
				t.Fatal(err)
			}
			if profiles["door"] != test.want || profiles["living"] != defaults {
				t.Fatalf("resolved profiles=%+v", profiles)
			}
		})
	}
}

func TestCameraProfileValidation(t *testing.T) {
	defaults := videoProfile{3840, 2160, 20, "H264"}
	for _, value := range []string{
		"living", "=width=1920", "living=", "missing=width=1920",
		"living=width=0", "living=height=-1", "living=fps=0", "living=fps=121",
		"living=width=text", "living=fps=1.5", "living=codec=AV1", "living=codec=",
		"living=width=1920,width=1280", "living=codec=H264,codec=H265",
		"living=bitrate=4096", "living=width=1920,", "living=fps=20,codec",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := resolveCameraProfiles([]string{"living=:8891"}, []string{value}, defaults); err == nil {
				t.Fatal("invalid camera profile accepted")
			}
		})
	}
	if _, err := resolveCameraProfiles([]string{"living=:8891"}, []string{"living=width=1920", "living=fps=15"}, defaults); err == nil {
		t.Fatal("duplicate camera overrides accepted")
	}
	if _, err := resolveCameraProfiles([]string{"invalid"}, nil, defaults); err == nil {
		t.Fatal("invalid camera mapping accepted")
	}
	for _, profile := range []videoProfile{{0, 2160, 20, "H264"}, {3840, 0, 20, "H264"}, {3840, 2160, 121, "H264"}, {3840, 2160, 20, "AV1"}} {
		if _, err := resolveCameraProfiles([]string{"living=:8891"}, []string{"living=width=1920,height=1080,fps=15,codec=H265"}, profile); err == nil {
			t.Fatal("invalid global defaults accepted even though a camera overrides them")
		}
	}
}

func TestCameraProfilesRemainIndependentInSOAPResponses(t *testing.T) {
	profiles, err := resolveCameraProfiles(
		[]string{"living=:8891", "door=:8892", "garage=:8893"},
		[]string{"living=width=3840,height=2160,fps=20,codec=H265", "door=fps=15"},
		videoProfile{1920, 1080, 30, "H264"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for name, profile := range profiles {
		server := httptest.NewServer(&proxy{name: name, video: profile})
		t.Cleanup(server.Close)
		for _, action := range []string{"GetProfiles", "GetProfile", "GetVideoSources"} {
			t.Run(name+"/"+action, func(t *testing.T) {
				t.Parallel()
				body := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><` + action + ` xmlns="http://www.onvif.org/ver10/media/wsdl"/></s:Body></s:Envelope>`
				response, err := http.Post(server.URL+"/onvif/media_service", "application/soap+xml", strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("SOAP status=%d", response.StatusCode)
				}
				decoder := xml.NewDecoder(response.Body)
				values := make(map[string]string)
				for {
					token, err := decoder.Token()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					element, ok := token.(xml.StartElement)
					if !ok {
						continue
					}
					switch element.Name.Local {
					case "Width", "Height", "FrameRateLimit", "Framerate", "Encoding":
						var value string
						if err := decoder.DecodeElement(&value, &element); err != nil {
							t.Fatal(err)
						}
						values[element.Name.Local] = value
					case "Bounds":
						for _, attr := range element.Attr {
							values["Bounds/"+attr.Name.Local] = attr.Value
						}
					}
				}
				want := map[string]string{"Width": strconv.Itoa(profile.width), "Height": strconv.Itoa(profile.height)}
				if action == "GetVideoSources" {
					want["Framerate"] = strconv.Itoa(profile.fps)
				} else {
					want["FrameRateLimit"] = strconv.Itoa(profile.fps)
					want["Encoding"] = profile.codec
					want["Bounds/width"] = strconv.Itoa(profile.width)
					want["Bounds/height"] = strconv.Itoa(profile.height)
				}
				for key, expected := range want {
					if values[key] != expected {
						t.Errorf("%s=%q, want %q", key, values[key], expected)
					}
				}
			})
		}
	}
}
