# xiaomi-onvif

一个集成 [go2rtc](https://github.com/AlexxIT/go2rtc) 修改版的发行项目，为米家摄像头增加 ONVIF 云台控制，并让它们能够配合 Frigate 自动追踪。

> [!WARNING]
> 本项目是基于摄像头逆向行为开发的 Beta 软件。云台命令会实际驱动硬件，请在有人看护的情况下测试，切勿将控制端点暴露到公网。

[English documentation](../README.md)

## 为什么提供集成发行版？

ONVIF 网桥依赖米家云台和预置点 API，而这些 API 目前尚未进入 go2rtc 官方版本。因此，本仓库提供一套版本匹配的组件：

- 修改版 `go2rtc`，包含米家时间戳修正、云台和预置点支持；
- `xiaomi-onvif`，通过 ONVIF 暴露这些视频流和控制能力。

两个二进制文件由同一份源码构建，并发布在同一个容器镜像中。上游基线和补丁管理规则见 [UPSTREAM.md](../UPSTREAM.md)。

## 功能

- 修正米家 `miss` 视频流时间戳，提高录像稳定性；
- 通过当前 go2rtc 摄像头连接手动控制水平和垂直云台；
- 发现米家“常看位置”并调用预置点；
- 提供 ONVIF Device、Media 和 PTZ 兼容端点；
- 支持 Frigate 所需的 FOV 相对移动和模拟 `MoveStatus`；
- 单进程支持多个摄像头；
- 可选的第三方 ONVIF 摄像头兼容代理；
- 提供静态 Linux 二进制文件和多架构容器镜像。

## 已验证硬件

| 型号标识 | 视频 | 手动云台 | 预置点 | Frigate 自动追踪 |
| --- | --- | --- | --- | --- |
| `chuangmi.camera.079ac1` | 已测试 | 已测试 | 已测试 | 已测试 |
| `chuangmi.camera.81ac1` | 已测试 | 已测试 | 已测试 | 已测试 |

其他使用 `miss` 协议的米家摄像头也可能正常工作，但不同型号使用的电机操作 ID 和 MIoT 预置点属性可能不同。提交兼容性问题时，请提供型号标识、固件版本、地区和脱敏日志。

## 快速开始

运行要求：

- 支持 Compose 的 Docker；
- 米家摄像头已经能够在 go2rtc 中正常播放；
- Frigate 0.17 或兼容的 ONVIF 客户端；
- go2rtc 能通过本地网络访问摄像头；
- go2rtc 需要刷新米家加密密钥时能够访问互联网。

复制示例目录并替换所有 `CHANGE_ME`：

```shell
cp -R examples/xiaomi-onvif xiaomi-onvif-config
docker compose -f xiaomi-onvif-config/compose.yaml pull
docker compose -f xiaomi-onvif-config/compose.yaml up -d
```

示例默认使用已发布的多架构镜像 `ghcr.io/sincerexia/xiaomi-onvif:0.1.0-beta.2`，并从同一镜像启动两个服务。`go2rtc` 负责维护摄像头连接，`xiaomi-onvif` 在 `8891` 端口为 ONVIF 客户端提供服务。

如需在本地构建并使用镜像：

```shell
docker build -f docker/Dockerfile -t xiaomi-onvif:dev .
XIAOMI_ONVIF_IMAGE=xiaomi-onvif:dev \
  docker compose -f xiaomi-onvif-config/compose.yaml up -d
```

配置示例：

- [go2rtc 示例](../examples/xiaomi-onvif/go2rtc.yaml)
- [Compose 示例](../examples/xiaomi-onvif/compose.yaml)
- [Frigate 示例](../examples/xiaomi-onvif/frigate.yaml)

## 命令行配置

把一个米家视频流暴露为 ONVIF 摄像头：

```shell
xiaomi-onvif \
  -go2rtc=http://go2rtc:1984 \
  -rtsp=rtsp://go2rtc:8554 \
  -camera=living=:8891,living_4k
```

`-camera` 可以重复传入，格式为：

```text
NAME=LISTEN_ADDR[,STREAM]
```

省略 `STREAM` 时默认使用 `NAME_4k`，以兼容本项目早期版本；`STREAM` 也可以是完整 RTSP URL。

无需重新构建即可调整云台校准参数：

```text
-pan-duration-per-fov       默认 900ms
-tilt-duration-per-fov      默认 700ms
-video-settle               默认 400ms
-preset-settle              默认 2.5s
-profile-width              默认 3840
-profile-height             默认 2160
-profile-fps                默认 20
-profile-codec              默认 H264
```

Profile 参数用于向 ONVIF 客户端描述 RTSP 视频流，并不会转码。当摄像头的媒体参数不同时，应为每一种媒体配置运行独立的网桥进程。

运行 `xiaomi-onvif -h` 查看完整参数列表，运行 `xiaomi-onvif -version` 查看构建来源。

### 兼容代理

可选兼容代理能够适配不支持 Frigate 所需 FOV 相对移动或标准化移动状态的现有 ONVIF 摄像头：

```shell
xiaomi-onvif \
  -compat-camera=CAMERA=:8893,http://CAMERA_IP/onvif/device_service,http://CAMERA_IP/onvif/service,1.0,1.0
```

此模式需要针对每个摄像头单独校准，水平和垂直增益以及稳定等待时间都必须实测。旧参数名 `-onvif-camera` 仍保留为兼容别名。已验证行为和校准说明见[兼容代理指南](compatibility-proxy.md)。

## Frigate 校准

首次启动时，在 Frigate 中设置 `calibrate_on_startup: true` 并等待校准完成。Frigate 会把 `movement_weights` 写回配置，然后将 `calibrate_on_startup` 改为 `false` 并重启一次 Frigate。

Frigate 校准或追踪期间，必须关闭摄像头厂商自带的追踪。两个独立控制器会导致校准结果失效，并产生不可预测的云台移动。

## 安全模型

本项目面向可信家庭局域网。由于 Frigate 和网桥通常位于隔离的容器网络，当前 ONVIF 端点默认不验证请求身份。

- 不要将 `1984`、`8891` 或其他控制端口发布到公网；
- 优先使用内部 Docker 网络，只发布 Frigate 必须访问的端口；
- 将米家账户配置和摄像头 URL 作为敏感信息保护；
- 不要在 Issue 中上传真实配置文件或未经脱敏的日志；
- 安全漏洞请按照 [SECURITY.md](../SECURITY.md) 私下报告。

## 开发

```shell
go build ./...
go test ./cmd/xiaomi-onvif ./internal/xiaomi ./pkg/xiaomi/miss
go test -race ./cmd/xiaomi-onvif ./internal/xiaomi ./pkg/xiaomi/miss
go vet ./cmd/xiaomi-onvif ./internal/xiaomi ./pkg/xiaomi/miss
```

提交修改前请阅读 [CONTRIBUTING.md](../CONTRIBUTING.md)。维护者应按照 [RELEASING.md](../RELEASING.md) 完成仓库设置和标签发布。

## 许可证与商标

本仓库继续使用 MIT 许可证，并保留原 go2rtc 版权声明，详见 [LICENSE](../LICENSE) 和 [NOTICE](../NOTICE)。

Xiaomi、Mi Home、TP-Link、Frigate 和 go2rtc 是其各自所有者的商标或项目名称。本项目独立开发，不受这些所有者认可，也与其没有关联。
