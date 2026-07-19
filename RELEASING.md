# Release process

## One-time repository setup

1. Create an empty public GitHub repository named `xiaomi-onvif`.
2. Keep the Go module path as `github.com/AlexxIT/go2rtc`; changing it would
   break the inherited `internal` package layout and complicate upstream sync.
3. Configure only the new `xiaomi-onvif` repository as `origin`. The official
   go2rtc repository does not need to remain configured as a remote.
4. Enable Dependabot alerts, secret scanning, push protection, code scanning
   and private vulnerability reporting in repository settings.
5. Protect `main` and require the `CI / test` and `CI / container` checks.

## Preparing a release

1. Confirm the upstream base recorded in `UPSTREAM.md`.
2. Update `CHANGELOG.md` and remove resolved beta limitations.
3. Run the same checks as CI:

   ```shell
   go build ./...
   go test ./cmd/xiaomi-onvif ./internal/xiaomi ./pkg/xiaomi/miss
   go test -race ./cmd/xiaomi-onvif ./internal/xiaomi ./pkg/xiaomi/miss
   go vet ./cmd/xiaomi-onvif ./internal/xiaomi ./pkg/xiaomi/miss
   go run golang.org/x/vuln/cmd/govulncheck@latest ./cmd/xiaomi-onvif ./internal/xiaomi ./pkg/xiaomi/miss
   docker build -f docker/Dockerfile -t xiaomi-onvif:release-test .
   ```

4. Test manual PTZ, preset recall, Frigate calibration, return-to-preset and a
   recorded playback segment on supported physical hardware.
5. Create and push an annotated beta tag:

   ```shell
   git tag -a v0.1.0-beta.1 -m "xiaomi-onvif v0.1.0-beta.1"
   git push origin main v0.1.0-beta.1
   ```

The Release workflow builds checksummed binaries, a multi-architecture GHCR
image, build provenance attestations and the GitHub release. Verify the release
assets and pull the image by digest before announcing it.
