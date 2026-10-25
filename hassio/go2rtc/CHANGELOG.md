# Changelog

## 0.1.1

- Allow each camera to override ONVIF width, height, frame rate and codec.
- Inherit omitted camera profile fields from the existing global defaults.
- Verify mixed profiles and legacy defaults through SOAP container tests.

## 0.1.0

- Add an experimental amd64/aarch64 Home Assistant Add-on.
- Run matched go2rtc and optional Xiaomi ONVIF processes with shared lifecycle.
- Preserve existing go2rtc configuration and expose configurable ONVIF cameras.
- Add configuration regression tests and isolated container smoke tests.
- HAOS installation and Ingress validation are pending.
