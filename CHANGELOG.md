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
- Version metadata, public examples and release automation.

### Changed

- Normalized Xiaomi video and audio RTP sequence/timestamp generation to avoid
  broken recordings caused by irregular camera timestamps.

### Security

- Added bounded SOAP request bodies, HTTP server timeouts and validated
  forwarded host handling.
