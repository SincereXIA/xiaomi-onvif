# Home Assistant ONVIF client regression tests

These tests use `onvif-zeep-async==4.3.0`, the client version pinned by
[Home Assistant Core 2026.10.4](https://github.com/home-assistant/core/blob/2026.10.4/homeassistant/components/onvif/manifest.json).
The request sequence follows
[`config_flow.async_setup_profiles`](https://github.com/home-assistant/core/blob/2026.10.4/homeassistant/components/onvif/config_flow.py)
and [`ONVIFDevice.async_setup`](https://github.com/home-assistant/core/blob/2026.10.4/homeassistant/components/onvif/device.py).
CI uses Python 3.14. Update the pin and audit those call sites together when
changing the reference HA version.

The suite starts the real bridge binary on loopback and a mock go2rtc backend.
It covers service discovery, typed SOAP parsing, network identity, device info,
time, Device/Media/PTZ capabilities, profiles, PTZ configurations/options/nodes,
presets, status, stream URIs and mocked movement/preset commands. It also verifies
identity isolation and stability across process/port changes, blank and placeholder
credentials, unsupported snapshots/events/imaging, and HA's H264 profile filter.
No camera credentials or physical hardware are used.

The preset tests also execute an unchanged copy of
`ONVIFDevice.async_perform_ptz` from the pinned HA release, stored in
`fixtures/ha_perform_ptz.py.txt` under Apache-2.0 (see `fixtures/HA_LICENSE.md`).
The fixture records its source URL and the SHA256 of the original source file.
Its real empty-list and token-membership guards run against discovered presets
and the real ONVIF client/bridge. Tests cover explicit zero and non-contiguous
tokens, discovery faults and refreshing the list after a favorite is added.
Update this fixture with the reference HA version, rather than editing its logic.

Go tests in `internal/xiaomi` separately exercise the cloud response parser,
API activation payloads, consumer sharing, on-demand connections, cancellation
and cleanup with fake producers. Neither layer proves physical camera behavior.

```sh
python3 -m venv /tmp/xiaomi-onvif-client-tests
/tmp/xiaomi-onvif-client-tests/bin/pip install -r tests/onvif/requirements.txt
go build -trimpath -o /tmp/xiaomi-onvif-ha-test ./cmd/xiaomi-onvif
/tmp/xiaomi-onvif-client-tests/bin/python -W error::ResourceWarning -m unittest discover -s tests/onvif -v
```

Set `ONVIF_BRIDGE_BINARY` to test another native binary. The initialization test
fails on the unmodified Add-on 0.1.1 bridge with
`unsupported operation GetNetworkInterfaces`.

This checks the protocol calls and profile selection used by HA, not a running
HA Core instance, its entity registry/UI, real video decoding, or physical PTZ.
An H265-only profile still fails HA's H264 gate. The bridge advertises
`SnapshotUri=false`; HA can use its FFmpeg snapshot fallback. No ONVIF snapshot
URI or event service is advertised. Real HAOS and client integration still need
separate validation.
