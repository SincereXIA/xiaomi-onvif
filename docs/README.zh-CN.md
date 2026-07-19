# xiaomi-onvif

`xiaomi-onvif` 是一个集成修改版 go2rtc 的发行项目，为米家摄像头提供：

- 录像时间戳修正；
- 手动水平、垂直云台控制；
- 米家“常看位置”读取与返回；
- ONVIF Device、Media、PTZ 兼容接口；
- Frigate 自动追踪所需的 FOV 相对移动与 `MoveStatus`；
- 多摄像头和可选第三方 ONVIF 兼容代理。

## 为什么集成 go2rtc

ONVIF 代理依赖新增的 `/api/xiaomi/ptz` 和
`/api/xiaomi/presets`。这些接口尚未进入官方 go2rtc，因此本项目在同一源码版本和镜像中同时提供修改后的 `go2rtc` 与 `xiaomi-onvif`，避免用户组合到不兼容版本。

上游基线及同步规则见 [`UPSTREAM.md`](../UPSTREAM.md)。

## 快速开始

复制示例并替换所有 `CHANGE_ME`：

```shell
cp -R examples/xiaomi-onvif xiaomi-onvif-config
docker compose -f xiaomi-onvif-config/compose.yaml pull
docker compose -f xiaomi-onvif-config/compose.yaml up -d
```

示例默认使用已发布的多架构镜像
`ghcr.io/sincerexia/xiaomi-onvif:0.1.0-beta.2`，并用同一个镜像启动两个容器：go2rtc 维护摄像头连接，ONVIF 容器把视频地址、云台和预置点转换为 Frigate 可识别的接口。

如需在本地构建并使用镜像：

```shell
docker build -f docker/Dockerfile -t xiaomi-onvif:dev .
XIAOMI_ONVIF_IMAGE=xiaomi-onvif:dev \
  docker compose -f xiaomi-onvif-config/compose.yaml up -d
```

## 摄像头映射

```shell
xiaomi-onvif \
  -go2rtc=http://go2rtc:1984 \
  -rtsp=rtsp://go2rtc:8554 \
  -camera=living=:8891,living_4k
```

格式为：

```text
名称=监听地址[,go2rtc流名称]
```

可以重复传入 `-camera`。省略流名称时使用 `名称_4k`，也可以直接填写完整 RTSP URL。

## Frigate 首次校准

首次启动时设置：

```yaml
calibrate_on_startup: true
```

等待 Frigate 完成校准并把 `movement_weights` 写回配置，然后改为
`false` 并重启。校准和追踪期间必须关闭摄像头厂商自带的追踪，否则两个控制器会互相干扰。

## 安全说明

当前 ONVIF 兼容端点面向可信家庭局域网，默认不进行身份认证：

- 不要把 go2rtc API 或 ONVIF 控制端口暴露到互联网；
- 优先让 Frigate 与本项目使用隔离的 Docker 网络；
- 不要在 Issue 中上传真实米家凭据、设备 ID、视频或完整配置；
- 漏洞请按照 [`SECURITY.md`](../SECURITY.md) 私下报告。

完整参数使用 `xiaomi-onvif -h` 查看，构建版本使用
`xiaomi-onvif -version` 查看。
