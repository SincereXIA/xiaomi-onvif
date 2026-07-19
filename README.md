# xiaomi-onvif

An integrated [go2rtc](https://github.com/AlexxIT/go2rtc) distribution that adds
ONVIF PTZ support for Xiaomi Home cameras and makes them usable with Frigate
autotracking.

> [!WARNING]
> This project is beta software built from reverse-engineered camera behavior.
> PTZ commands can move physical hardware. Test with supervision and do not
> expose the control endpoints to the public Internet.

[简体中文文档](docs/README.zh-CN.md)

## Why an integrated distribution?

The ONVIF bridge needs Xiaomi PTZ and preset APIs that are not currently part
of upstream go2rtc. This repository therefore ships a matched pair:

- a patched `go2rtc` binary with Xiaomi timestamp, PTZ and preset support;
- a `xiaomi-onvif` binary that exposes those streams and controls through ONVIF.

Both binaries are built from the same source revision and published in the same
container image. See [UPSTREAM.md](UPSTREAM.md) for the upstream base and patch
policy.

## Features

- Xiaomi `miss` stream timestamp normalization for stable recordings.
- Manual pan/tilt through the active go2rtc camera connection.
- Xiaomi Home saved-position discovery and preset recall.
- ONVIF Device, Media and PTZ compatibility endpoints.
- Frigate FOV-relative movement and synthetic `MoveStatus` support.
- Multiple cameras from one process.
- Optional compatibility proxy for cameras with incomplete ONVIF behavior.
- Static Linux binaries and multi-architecture container builds.

## Verified hardware

| Model identifier | Video | Manual PTZ | Presets | Frigate autotracking |
| --- | --- | --- | --- | --- |
| `chuangmi.camera.079ac1` | Tested | Tested | Tested | Tested |
| `chuangmi.camera.81ac1` | Tested | Tested | Tested | Tested |

Other Xiaomi `miss` cameras may work, but motor operation IDs and MIoT preset
properties can differ by model. Please include the model identifier, firmware,
region and sanitized logs in compatibility reports.

## Quick start

Requirements:

- Docker with Compose support;
- a Xiaomi camera already working in go2rtc;
- Frigate 0.17 or a compatible ONVIF client;
- local network access from go2rtc to the camera;
- Internet access when go2rtc needs to refresh Xiaomi encryption keys.

Build the integrated image:

```shell
docker build -f docker/Dockerfile -t xiaomi-onvif:dev .
```

Copy the example directory and replace every `CHANGE_ME` value:

```shell
cp -R examples/xiaomi-onvif xiaomi-onvif-config
docker compose -f xiaomi-onvif-config/compose.yaml up -d
```

The example starts two services from the same image. `go2rtc` owns the camera
connection; `xiaomi-onvif` exposes port `8891` for ONVIF clients.

See:

- [go2rtc example](examples/xiaomi-onvif/go2rtc.yaml)
- [Compose example](examples/xiaomi-onvif/compose.yaml)
- [Frigate example](examples/xiaomi-onvif/frigate.yaml)

## Command-line configuration

Expose a Xiaomi stream as an ONVIF camera:

```shell
xiaomi-onvif \
  -go2rtc=http://go2rtc:1984 \
  -rtsp=rtsp://go2rtc:8554 \
  -camera=living=:8891,living_4k
```

`-camera` may be repeated. Its syntax is:

```text
NAME=LISTEN_ADDR[,STREAM]
```

When `STREAM` is omitted it defaults to `NAME_4k`, preserving compatibility
with early versions of this project. `STREAM` may also be a complete RTSP URL.

Movement calibration can be tuned without rebuilding:

```text
-pan-duration-per-fov       default 900ms
-tilt-duration-per-fov      default 700ms
-video-settle               default 400ms
-preset-settle              default 2.5s
-profile-width              default 3840
-profile-height             default 2160
-profile-fps                default 20
-profile-codec              default H264
```

Profile values describe the RTSP stream to ONVIF clients; they do not transcode
video. Run one bridge process per metadata profile when cameras differ.

Run `xiaomi-onvif -h` for the complete option list and
`xiaomi-onvif -version` for build provenance.

### Compatibility proxy

The optional compatibility proxy adapts an existing ONVIF camera that lacks
Frigate's required FOV-relative movement or normalized movement status:

```shell
xiaomi-onvif \
  -compat-camera=CAMERA=:8893,http://CAMERA_IP/onvif/device_service,http://CAMERA_IP/onvif/service,1.0,1.0
```

This mode is camera-specific. Gains and settle durations must be measured for
each model. The legacy flag name `-onvif-camera` remains available as an alias.
See [the compatibility proxy guide](docs/compatibility-proxy.md) for verified
behavior and calibration notes.

## Frigate calibration

For the first startup, set `calibrate_on_startup: true` in Frigate and wait for
calibration to finish. Frigate writes `movement_weights` back to its config.
Then set `calibrate_on_startup: false` and restart Frigate once.

Keep the camera manufacturer's native tracking disabled while Frigate calibrates
or tracks. Two independent controllers will invalidate calibration and produce
unpredictable movement.

## Security model

The bridge is intended for a trusted home network. Its ONVIF endpoint currently
does not authenticate requests because Frigate and the bridge normally share an
isolated container network.

- Do not publish ports `1984`, `8891` or other control ports to the Internet.
- Prefer an internal Docker network; publish only the ports Frigate needs.
- Protect Xiaomi account configuration and camera URLs as secrets.
- Do not attach real configuration files or unsanitized logs to issues.
- Report vulnerabilities according to [SECURITY.md](SECURITY.md).

## Development

```shell
go build ./...
go test ./cmd/xiaomi-onvif ./internal/xiaomi ./pkg/xiaomi/miss
go test -race ./cmd/xiaomi-onvif ./internal/xiaomi ./pkg/xiaomi/miss
go vet ./cmd/xiaomi-onvif ./internal/xiaomi ./pkg/xiaomi/miss
```

See [CONTRIBUTING.md](CONTRIBUTING.md) before submitting changes.
Maintainers should follow [RELEASING.md](RELEASING.md) for repository setup and
tagged releases.

## License and trademarks

This repository remains licensed under the MIT License and retains the original
go2rtc copyright notice. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

Xiaomi, Mi Home, TP-Link, Frigate and go2rtc are trademarks or project names of
their respective owners. This project is independent and is not endorsed by or
affiliated with those owners.
