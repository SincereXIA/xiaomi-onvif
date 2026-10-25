# Changelog

All notable changes to this project are documented in this file.

The format is based on Keep a Changelog, and the project uses Semantic
Versioning after the first public beta.

## [Unreleased]

### Added

- Xiaomi motor control over the active go2rtc `miss` connection.
- Xiaomi Home saved-position discovery and preset recall.
- ONVIF Device, Media and PTZ compatibility service.
- Frigate FOV-relative movement and movement-status compatibility.
- Optional adapter for incomplete upstream ONVIF implementations.
- Multi-camera command-line configuration and stream-name overrides.
- Per-camera ONVIF video metadata overrides with global defaults for legacy
  command-line and Home Assistant Add-on configurations.
- Version metadata, public examples and release automation.

### Changed

- Normalized Xiaomi video and audio RTP sequence/timestamp generation to avoid
  broken recordings caused by irregular camera timestamps.

### Security

- Validate CS2 UDP peers and transport headers, bound reassembled and queued
  frame data, and reject short command, media and encrypted payloads safely.
- Cover Xiaomi transport and crypto subpackages in CI tests and race checks.
- Build with Go 1.26.10 to include current Go standard library security fixes.
- Added bounded SOAP request bodies, HTTP server timeouts and validated
  forwarded host handling.
