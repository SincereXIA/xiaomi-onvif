package main

import (
	"encoding/xml"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVirtualMACIsStableAndCameraSpecific(t *testing.T) {
	first := &proxy{name: "probe", listen: ":8000"}
	if got := first.virtualMAC(); got != "5a:8b:7f:75:fe:2c" {
		t.Fatalf("identity changed: %s", got)
	}
	restarted := &proxy{name: "probe", listen: "127.0.0.1:8891"}
	if restarted.virtualMAC() != first.virtualMAC() {
		t.Fatal("identity changed with listen address")
	}
	other := &proxy{name: "door", listen: ":8000"}
	if other.virtualMAC() == first.virtualMAC() {
		t.Fatal("cameras on the same host share an identity")
	}
	for _, camera := range []*proxy{first, other, {name: "camera <one> & two"}} {
		mac, err := net.ParseMAC(camera.virtualMAC())
		if err != nil || len(mac) != 6 || mac[0]&3 != 2 {
			t.Fatalf("not a locally administered unicast MAC: %s", camera.virtualMAC())
		}
	}
}

func TestGetNetworkInterfacesReturnsEnabledVirtualIdentity(t *testing.T) {
	p := &proxy{name: "living"}
	body := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><GetNetworkInterfaces xmlns="http://www.onvif.org/ver10/device/wsdl"/></s:Body></s:Envelope>`
	recorder := httptest.NewRecorder()
	p.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/onvif/device_service", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("SOAP status=%d: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Body struct {
			Response struct {
				Interfaces []struct {
					Token   string `xml:"token,attr"`
					Enabled bool
					Info    struct{ Name, HwAddress string }
				} `xml:"NetworkInterfaces"`
			} `xml:"http://www.onvif.org/ver10/device/wsdl GetNetworkInterfacesResponse"`
		}
	}
	if err := xml.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	interfaces := envelope.Body.Response.Interfaces
	if len(interfaces) != 1 || !interfaces[0].Enabled || interfaces[0].Token != "onvif0" || interfaces[0].Info.HwAddress != p.virtualMAC() {
		t.Fatalf("invalid virtual interface: %+v", interfaces)
	}
}

func TestServiceCapabilitiesFollowOperationNamespace(t *testing.T) {
	for _, test := range []struct{ namespace, path, want string }{
		{deviceNamespace, "/onvif/service", "tds"},
		{mediaNamespace, "/onvif/service", "trt"},
		{ptzNamespace, "/onvif/service", "tptz"},
		{mediaNamespace, "/onvif/ptz_service", "trt"},
		{"", "/onvif/device_service", "tds"},
		{"", "/onvif/media_service", "trt"},
		{"", "/onvif/ptz_service", "tptz"},
	} {
		t.Run(test.namespace+test.path, func(t *testing.T) {
			operation := `<GetServiceCapabilities/>`
			if test.namespace != "" {
				operation = `<GetServiceCapabilities xmlns="` + test.namespace + `"/>`
			}
			body := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>` + operation + `</s:Body></s:Envelope>`
			recorder := httptest.NewRecorder()
			(&proxy{name: "living"}).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(body)))
			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `<`+test.want+`:GetServiceCapabilitiesResponse>`) {
				t.Fatalf("incorrect service capability response: %s", recorder.Body.String())
			}
			if test.want == "trt" && (!strings.Contains(recorder.Body.String(), `SnapshotUri="false"`) || strings.Contains(recorder.Body.String(), `MoveStatus=`)) {
				t.Fatal("Media advertises unsupported snapshot or PTZ capabilities")
			}
		})
	}
	if _, supported := serviceCapabilities("http://www.onvif.org/ver10/events/wsdl", "/onvif/media_service"); supported {
		t.Fatal("unsupported service namespace accepted")
	}
}
