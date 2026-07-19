# Upstream policy

This repository is an integrated distribution based on
[`AlexxIT/go2rtc`](https://github.com/AlexxIT/go2rtc).

## Current base

- Upstream commit: `a71f29545c8ceefa4224b201d1d34ff937300fa8`
- Upstream commit date: 2026-05-27
- Original license: MIT

## Local patch groups

Changes should remain separable in the following groups:

1. Xiaomi `miss` RTP timestamp and sequence normalization.
2. Xiaomi motor command transport and go2rtc PTZ API.
3. Xiaomi Home saved-position discovery and preset API.
4. ONVIF Device, Media and PTZ bridge.
5. Optional compatibility adapters for incomplete third-party ONVIF cameras.
6. Packaging, tests and documentation for this distribution.

Patches that are useful outside this distribution should be proposed upstream
where practical. Until the required PTZ and preset interfaces are available in
an upstream release, published `xiaomi-onvif` versions must use the bundled
go2rtc binary from the same tag.

## Updating the go2rtc base

The public checkout only requires its own `origin` remote. A persistent remote
for the official go2rtc repository is not part of this project's setup.

When adopting a newer go2rtc snapshot, record the exact source commit here,
import it on a temporary integration branch, and resolve the local patch groups
in a reviewable pull request. Do not rebase or rewrite already published release
tags. After an update, run the full Go test suite, race tests, container builds
and at least one physical-camera smoke test.

## Baseline test limitation

At the recorded base commit, `go test ./...` is not platform-independent and
does not pass unchanged on Go 1.26.5. Known upstream failures include
OS-specific FFmpeg command expectations, mDNS tests requiring a suitable
interface, incomplete HomeKit test fixtures and older assertions affected by
newer Go behavior. CI therefore requires `go build ./...` plus tests, race tests
and vet for the modified Xiaomi/ONVIF components. Full-suite failures must still
be reviewed whenever the upstream base changes.
