# Installation and configuration

## Status and requirements

This is an experimental Add-on for Home Assistant OS or a supported Supervisor
installation on amd64 or aarch64. Home Assistant Container cannot install
Add-ons. HAOS installation, Ingress, Supervisor watchdog, backup/restore and
upgrade behavior have not yet been validated on a real HAOS system.

The initial image `ghcr.io/sincerexia/xiaomi-onvif-addon:0.1.0` must be published
and made public before the following store installation can work. No image is
published merely by opening or merging this implementation PR.

## Migrate from AlexxIT's go2rtc Add-on

1. Back up the original Add-on options and your Home Assistant configuration,
   including `go2rtc.yaml`. Record its API, RTSP and WebRTC ports and stream names.
2. Stop the original go2rtc Add-on and disable its start-on-boot and watchdog.
   Keep it installed for rollback. Both Add-ons use host networking and cannot
   listen on the same ports simultaneously.
3. After the image is published, add
   `https://github.com/SincereXIA/xiaomi-onvif` in **Settings > Add-ons > Add-on
   Store > Repositories**, refresh the store and install **go2rtc with Xiaomi
   ONVIF**. For development, Supervisor also supports a `#branch` repository URL.
4. Select the existing configuration file with `config_path`. The default
   `/config/go2rtc.yaml` is the file in Home Assistant's configuration directory.
   Keep the existing streams and credentials. This Add-on never rewrites an
   existing selected file. If it is absent, it creates `streams: {}` with private
   file permissions. It does not copy a private `/data` file from another Add-on.
5. Keep go2rtc's HTTP API on port **1984** for Ingress and the health checks.
   Existing RTSP ports such as 8554 or 8654 can stay in your YAML. The Add-on's
   `api_url` must reach the local API; include percent-encoded credentials in it
   if your existing configuration requires HTTP Basic authentication locally.
6. Start this Add-on manually, inspect its logs and verify the existing RTSP
   streams. Then enable the optional ONVIF bridge. Enable start-on-boot and the
   Supervisor watchdog only after successful validation.

Home Assistant maps its configuration directory to `/config`, media to `/media`
and certificates to `/ssl`. The latter two mappings are read-only. Ingress uses
go2rtc's web interface on port 1984. A custom API bind address must still allow
the Supervisor Ingress connection and local health probes. The supplied defaults
match go2rtc's normal ports. TLS reverse-proxy URLs are not RTSP addresses.

## Options

```yaml
config_path: /config/go2rtc.yaml
api_url: http://127.0.0.1:1984
onvif_enabled: true
rtsp_base: rtsp://192.168.1.10:8554
cameras:
  - stream: c700
    rtsp_stream: c700_h264
    port: 8891
profile_width: 3840
profile_height: 2160
profile_fps: 20
profile_codec: H264
```

- `config_path`: absolute path to the go2rtc YAML inside `/config`. Parent
  directories are created if needed. Paths and symlinks outside it are rejected.
- `api_url`: this container's go2rtc API on localhost port 1984. The launcher uses
  it for readiness and the bridge uses it for Xiaomi PTZ. It is never printed by
  the launcher. Match your local API authentication if configured.
- `onvif_enabled`: defaults to `false`. go2rtc can run without opening any ONVIF
  listeners. Enabling it requires at least one camera and a valid `rtsp_base`.
- `rtsp_base`: the **LAN-accessible HAOS host address** and the actual go2rtc RTSP
  port. ONVIF returns this URL to clients. Do not use loopback, an Ingress URL or
  a public HTTPS proxy address. RTSP credentials, if needed, belong in this URL.
- `cameras[].stream`: an existing Xiaomi stream name in go2rtc. This is the name
  used for PTZ control, not a new display name or a Xiaomi model identifier.
- `cameras[].rtsp_stream`: optional stream name returned in the ONVIF RTSP URI,
  defaulting to `stream`. Use an existing H264 rendition for Home Assistant's
  ONVIF integration when the camera's native stream is H265. This option does
  not create a stream or transcode it automatically.
- `cameras[].port`: a distinct host TCP port, 1024-65535, for that camera's ONVIF
  service, for example 8891 and 8892. Avoid all existing host services and any
  custom go2rtc listeners. Default go2rtc ports and the configured RTSP port are
  rejected automatically. Stream names cannot contain commas, equals signs or
  control characters because the bridge CLI uses those as separators.
- `profile_width`, `profile_height`, `profile_fps`, `profile_codec`: metadata
  advertised by the bridge, shared by all configured cameras. Set these to the
  actual returned stream properties. They do not change encoding. Codec is
  `H264` or `H265`; defaults are 3840x2160, 20 fps, H264.

Add each camera to Home Assistant using the HAOS host address and its configured
ONVIF port. This version implements the Xiaomi bridge, not the separate generic
ONVIF compatibility-proxy mode. No model allowlist is added; other Xiaomi MISS
models can be tried, but their video, motor operations and presets need testing.

## Network access and lifecycle

Host networking is necessary for the camera connections and advertised ONVIF
endpoints. API, RTSP, WebRTC and enabled ONVIF ports may be accessible to other
LAN devices. Ingress authentication does not protect direct host ports. This
bridge has no ONVIF authentication. Keep it on a trusted network; restrict
untrusted clients with firewall/VLAN rules and never forward these ports to the
Internet. Protect the existing go2rtc API with its supported access controls.
See the repository's [access restrictions](../../README.md#security-model).

Both binaries come from the same source revision. The launcher validates options,
starts go2rtc, waits for its API, then starts ONVIF when enabled. A failure to
become ready or an unexpected exit of either required process stops the whole
container with an error so Supervisor can restart it. A normal stop terminates
both processes. Docker health checks probe go2rtc and every enabled ONVIF
`/healthz`; they do not open a camera stream or verify camera connectivity.
The Supervisor watchdog checks the go2rtc TCP listener separately, so it also
works when HTTP Basic authentication is enabled.

## Rollback

Stop this Add-on and disable its start-on-boot and watchdog before restarting
the original Add-on. Restore the backed-up YAML only if you edited it after
migration. Stream names and RTSP ports can stay unchanged, so existing clients
can reconnect to the original go2rtc instance.

## Development and release

Run from the repository root:

```sh
python3 -m venv /tmp/xiaomi-addon-tests
/tmp/xiaomi-addon-tests/bin/pip install -r hassio/go2rtc/tests/requirements.txt
/tmp/xiaomi-addon-tests/bin/python -m unittest discover -s hassio/go2rtc/tests -v
docker build -f docker/Dockerfile.addon -t xiaomi-onvif-addon:test .
python3 hassio/go2rtc/tests/smoke.py xiaomi-onvif-addon:test
```

Smoke tests use isolated Docker bridge networks, temporary options and an empty
stream configuration. They check API/bridge readiness, SOAP stream URIs, disabled
ONVIF, preservation of existing YAML, graceful stop, failure propagation and
redacted option errors. They never connect to real cameras or require Supervisor.
Run Docker commands on the machine hosting the Docker daemon so fixture bind
mounts refer to the correct filesystem. CI repeats these tests on native amd64
and arm64 runners.

The `Home Assistant Add-on` workflow publishes a separate multi-architecture
image only for tags `addon-v<VERSION>` matching `config.yaml`. Publish the image
and make its GHCR package public before making that manifest version available
in the store. Do not overwrite a released version; update the manifest and
changelog for the next release. The normal project image has an independent
release workflow.

For the initial HAOS test, use the PR branch repository URL only after the first
image is available. Check installation, Ingress HTTP/WebSocket/media paths,
manual migration, start-on-boot, watchdog, restart/stop, ONVIF integration,
RTSP/PTZ, backup/restore and an upgrade before declaring HAOS support verified.
