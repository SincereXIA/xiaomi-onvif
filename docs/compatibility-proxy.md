# ONVIF compatibility proxy

The compatibility proxy is separate from Xiaomi control. It forwards SOAP to
an existing ONVIF camera while adapting behavior required by Frigate:

- advertises `TranslationSpaceFov` while translating requests to the upstream
  generic relative space;
- scales pan and tilt values independently;
- advertises `MoveStatus=true`;
- normalizes lowercase `idle`/`moving` values;
- treats `Zoom=unknown` as settled for cameras without optical zoom;
- supplies minimum movement windows for cameras that report `idle` before the
  video stream has settled;
- accepts Frigate's zero-distance calibration baseline without forwarding an
  invalid command to the camera.

## Configuration

```text
-compat-camera=NAME=LISTEN_ADDR,DEVICE_URL,SERVICE_URL[,PAN_GAIN,TILT_GAIN]
```

Example with placeholders:

```shell
xiaomi-onvif \
  -compat-camera=room=:8893,http://CAMERA_IP/onvif/device_service,http://CAMERA_IP/onvif/service,0.48,1.23 \
  -compat-preset-settle=8s \
  -compat-relative-settle-base=1.5s \
  -compat-relative-settle-per-fov=750ms
```

The gains above are examples, not universal defaults. Determine them from the
observed frame displacement of the exact camera and firmware while all native
tracking is disabled.

## Verified TP-Link behavior

The adapter has been tested with `TL-IPC48AW-PLUS`, firmware
`1.0.3 Build 260119 Rel.38255n`. That firmware exposes generic relative
movement but not FOV-relative movement and reports lowercase movement states.
It also ignores the `Speed` value for `RelativeMove`; `ContinuousMove` honors
speed, but conversion to timed continuous movement is not implemented yet.

Consequently, Frigate autotracking works through the proxy but physical movement
can be slower than the camera's maximum motor speed.

## Safety

Use this mode only on a trusted network. The proxy forwards ONVIF requests and
may move physical hardware. Always configure a return preset and verify pan/tilt
direction manually before enabling automatic tracking.
