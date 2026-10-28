package main

import (
	"crypto/sha256"
	"fmt"
	"net"
	"strings"
)

const (
	deviceNamespace = "http://www.onvif.org/ver10/device/wsdl"
	mediaNamespace  = "http://www.onvif.org/ver10/media/wsdl"
	ptzNamespace    = "http://www.onvif.org/ver20/ptz/wsdl"
)

// virtualMAC identifies this ONVIF endpoint, not a physical network interface.
// Camera names survive host, port and process changes, while separate cameras
// on the same host must not share the host's MAC in Home Assistant's registry.
func (p *proxy) virtualMAC() string {
	digest := sha256.Sum256([]byte("xiaomi-onvif:" + p.name))
	digest[0] = (digest[0] | 2) & 0xfe // Locally administered, unicast address.
	return net.HardwareAddr(digest[:6]).String()
}

func (p *proxy) getNetworkInterfaces() string {
	return fmt.Sprintf(`<tds:GetNetworkInterfacesResponse><tds:NetworkInterfaces token="onvif0"><tt:Enabled>true</tt:Enabled><tt:Info><tt:Name>onvif0</tt:Name><tt:HwAddress>%s</tt:HwAddress></tt:Info></tds:NetworkInterfaces></tds:GetNetworkInterfacesResponse>`, p.virtualMAC())
}

// serviceCapabilities uses the SOAP operation namespace because clients may
// send different services to one URL. Unqualified legacy requests use the path.
func serviceCapabilities(namespace, path string) (string, bool) {
	if namespace == "" {
		switch strings.TrimRight(path, "/") {
		case "/onvif/device_service":
			namespace = deviceNamespace
		case "/onvif/media_service":
			namespace = mediaNamespace
		default:
			namespace = ptzNamespace
		}
	}
	switch namespace {
	case deviceNamespace:
		return `<tds:GetServiceCapabilitiesResponse><tds:Capabilities><tds:Network IPFilter="false" ZeroConfiguration="false" IPVersion6="false" DynDNS="false"/><tds:Security UsernameToken="false" HttpDigest="false"/><tds:System DiscoveryResolve="false" DiscoveryBye="false" RemoteDiscovery="false" SystemBackup="false" SystemLogging="false" FirmwareUpgrade="false"/></tds:Capabilities></tds:GetServiceCapabilitiesResponse>`, true
	case mediaNamespace:
		return `<trt:GetServiceCapabilitiesResponse><trt:Capabilities SnapshotUri="false" Rotation="false" VideoSourceMode="false" OSD="false"><trt:ProfileCapabilities MaximumNumberOfProfiles="1"/><trt:StreamingCapabilities RTPMulticast="false" RTP_TCP="false" RTP_RTSP_TCP="true"/></trt:Capabilities></trt:GetServiceCapabilitiesResponse>`, true
	case ptzNamespace:
		return `<tptz:GetServiceCapabilitiesResponse><tptz:Capabilities EFlip="false" Reverse="false" GetCompatibleConfigurations="false" MoveStatus="true" StatusPosition="false"/></tptz:GetServiceCapabilitiesResponse>`, true
	default:
		return "", false
	}
}
