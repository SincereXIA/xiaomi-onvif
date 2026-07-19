# Xiaomi Mi Home

[`new in v1.9.13`](https://github.com/AlexxIT/go2rtc/releases/tag/v1.9.13)

This source allows you to view cameras from the [Xiaomi Mi Home](https://home.mi.com/) ecosystem.

Since 2020, Xiaomi has introduced a unified protocol for cameras called `miss`. I think it means **Mi Secure Streaming**. Until this point, the camera protocols were in chaos. Almost every model had different authorization, encryption, command lists, and media packet formats.

go2rtc supports two formats: `xiaomi/mess` and `xiaomi/legacy`.
And multiple P2P protocols: `cs2+udp`, `cs2+tcp`, several versions of `tutk+udp`.

Almost all cameras in the `xiaomi/mess` format and the `cs2` protocol work well.
Older `xiaomi/legacy` format cameras may have support issues.
The `tutk` protocol is the worst thing that's ever happened to the P2P world. It works terribly.

**Important:**

1. **Not all cameras are supported**. The list of supported cameras is collected in [this issue](https://github.com/AlexxIT/go2rtc/issues/1982).
2. Each time you connect to the camera, you need Internet access to obtain encryption keys.
3. Connection to the camera is local only.

**Features:**

- Multiple Xiaomi accounts supported
- Cameras from multiple regions are supported for a single account
- Two-way audio is supported
- Cameras with multiple lenses are supported

## Setup

1. Go to go2rtc WebUI > Add > Xiaomi > Login with username and password
2. Receive verification code by email or phone if required.
3. Complete the captcha if required.
4. If everything is OK, your account will be added, and you can load cameras from it.

**Example**

```yaml
xiaomi:
  1234567890: V1:***

streams:
  xiaomi1: xiaomi://1234567890:cn@192.168.1.123?did=9876543210&model=isa.camera.hlc7
```

## Configuration

Quality in the `miss` protocol is specified by a number from 0 to 5. Usually 0 means auto, 1 - sd, 2 - hd.
Go2rtc by default sets quality to 2. But some new cameras have HD quality at number 3.
Old cameras may have broken codec settings at number 3, so this number should not be set for all cameras.

You can change camera quality: `subtype=hd/sd/auto/0-5`.

```yaml
streams:
  xiaomi1: xiaomi://***&subtype=sd
```

You can use a second channel for dual cameras: `channel=2`.

```yaml
streams:
xiaomi1: xiaomi://***&channel=2
```

## Stable recording timestamps

Some Xiaomi cameras emit irregular source timestamps that cause fragmented or
stalled recordings after RTSP restreaming. This distribution can normalize
video timestamps to a configured frame rate:

```yaml
streams:
  xiaomi1: xiaomi://***&subtype=2&fps=20
```

The `fps` value must match the camera stream and may be between 1 and 120. If it
is omitted, the original camera timestamp conversion is retained.

## PTZ API in this distribution

The active Xiaomi `miss` connection exposes a local control API:

```text
POST /api/xiaomi/ptz?src=xiaomi1&direction=left&duration=300
```

Supported directions are `left`, `right`, `up`, `down` and `stop`. Movement
duration is expressed in milliseconds and is limited to two seconds per
request. The endpoint reuses an active stream variant for the same camera and
always sends a stop command after a timed movement.

Saved Xiaomi Home positions are available through:

```text
GET  /api/xiaomi/presets?src=xiaomi1
POST /api/xiaomi/presets?src=xiaomi1&token=1
```

These APIs are consumed by the bundled `xiaomi-onvif` bridge. Keep the go2rtc
API on a trusted local or container network.
