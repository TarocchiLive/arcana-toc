# Arcana ToC

Arcana ToC 是一款基于 Go 的本地点歌服务，支持网易云音乐与 QQ 音乐。

ToC 取自 Three of Cups（圣杯三），象征相聚与分享。

## 功能

- 网易云、手机 QQ、微信与 QQ 音乐 APP 扫码登录。
- 跨平台搜索歌曲、歌手与歌单，共享播放队列与历史。
- 顺序 / 随机播放，暂停、切歌、音量与进度控制。
- 独立终端管理界面，以及供本机程序集成的 HTTP API。
- 进程内音频播放，支持 MP3、FLAC、Ogg Vorbis 与 WAV。

## 构建与运行

需要 Go 1.26+；Linux 还需要 ALSA 开发库与 pkg-config。播放需要可用的音频设备。

```sh
make build
./bin/arcana-toc
```

另开终端启动管理界面：

```sh
./bin/arcana-toc --tui
```

使用 Nix：

```sh
nix build
./result/bin/arcana-toc
```

## 使用

按 `4` 进入账号页扫码登录，按 `2` 搜索点歌，按 `?` 查看快捷键。选择歌单后按 Enter 会替换当前队列并播放。

配置文件：`~/arcana/toc/config.yaml`，修改后重启服务。命令行帮助：`arcana-toc -help`。

## 接口

默认地址 `http://127.0.0.1:8974`，使用 JSON。本机 API 无需令牌，拒绝带浏览器 `Origin` 的请求；外部面板需自行鉴权。

| 接口 | 用途 |
| --- | --- |
| `GET /healthz` | 健康检查 |
| `GET /api/status` | 播放状态、曲目、模式、音量与进度（纳秒） |
| `POST /api/search` | 搜索歌曲：`{"keyword":"晴天","platform":"all","page":1,"limit":20}` |
| `POST /api/artists/search` | 搜索歌手，参数同歌曲搜索 |
| `GET /api/artists/{platform}/{id}/songs` | 歌手热门歌曲 |
| `GET /api/playlists/search?platform=all&q=晴天&page=1&limit=20` | 搜索歌单 |
| `POST /api/playlists/play` | 替换队列并播放：`{"platform":"netease","id":"歌单ID"}` |
| `GET /api/queue` / `GET /api/history` | 活动队列 / 播放历史 |
| `POST /api/queue` | 点歌：`{"platform":"netease","track_id":"歌曲ID"}` |
| `DELETE /api/queue/{id}` | 删除队列条目；删除正在播放的条目会切到下一首 |
| `POST /api/playback/start` / `next` / `pause` / `resume` / `stop` | 开始、切歌、暂停、继续、停止 |
| `POST /api/playback/mode` | 模式：`{"mode":"sequential"}` 或 `{"mode":"random"}` |
| `POST /api/playback/seek` / `volume` | `{"value":1000}`：进度为毫秒，音量范围 0–100 |
| `GET /api/{platform}/account` | 平台登录状态 |
| `POST /api/{platform}/refresh` | 刷新平台会话 |
| `POST /api/netease/login` / `GET /api/netease/login?key=…` | 创建 / 轮询网易云二维码 |
| `POST /api/qqmusic/login?channel=…` / `GET /api/qqmusic/login?channel=…&identifier=…` | 创建 / 轮询 QQ 音乐二维码，渠道为 `qq`、`wx`、`mobile` |

`platform` 为 `netease` 或 `qqmusic`；搜索另支持 `all`，每页最多返回每平台 20 条。聚合搜索返回各平台的 `has_more` 与 `errors`，部分平台失败时保留其他平台结果。

## 开发

```sh
make test   # race 测试
make vet    # 静态检查
make lint   # golangci-lint
nix flake check
```

模块边界与开发约定见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 声明

本项目是独立开发的开源工具。使用者应自行遵守适用的法律法规和平台规则。本项目仅用于学习和交流 Go 及相关框架，请勿用于其他用途。

## 许可证与致谢

采用 [GPL-3.0-or-later](LICENSE)。

感谢 [netease-music](https://github.com/go-musicfox/netease-music)、[QQMusicApi](https://github.com/L-1124/QQMusicApi)、[Beep](https://github.com/gopxl/beep) 与 [Bubble Tea](https://github.com/charmbracelet/bubbletea)。
