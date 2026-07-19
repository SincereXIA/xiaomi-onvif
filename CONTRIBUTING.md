# Contributing

Thank you for helping improve Xiaomi camera compatibility.

## Before opening an issue

- Search existing issues.
- Reproduce with the latest release.
- Remove credentials, account IDs, device IDs, tokens and public addresses.
- Include camera model identifier, firmware, Xiaomi region, host architecture,
  go2rtc/xiaomi-onvif version and Frigate version when relevant.

## Pull requests

Keep changes focused and preserve the patch groups described in `UPSTREAM.md`.
New device behavior should be configurable or guarded by a model-specific
capability; do not silently change movement behavior for all cameras.

Before submitting:

```shell
gofmt -w PATHS_YOU_CHANGED
go build ./...
go test ./cmd/xiaomi-onvif ./internal/xiaomi ./pkg/xiaomi/miss
go test -race ./cmd/xiaomi-onvif ./internal/xiaomi ./pkg/xiaomi/miss
go vet ./cmd/xiaomi-onvif ./internal/xiaomi ./pkg/xiaomi/miss
git diff --check
```

The recorded upstream base does not currently have a clean platform-independent
`go test ./...` result. Failures include OS-specific FFmpeg expectations,
environment-dependent mDNS tests and incomplete upstream test fixtures. Run the
full suite when changing upstream components, but use the required build and
focused tests above as this distribution's release gate until the baseline is
updated.

Hardware-dependent changes must describe the tested model, firmware and a safe
rollback path. Do not commit camera captures or configuration containing user
data.
