# Arcana ToC

Arcana ToC 是一款基于 Go 的本地点歌服务，支持网易云音乐与 QQ 音乐。

ToC 取自 Three of Cups（圣杯三），象征相聚与分享。

## 功能

- 网易云、手机 QQ、微信与 QQ 音乐 APP 扫码登录。
- 跨平台搜索歌曲、歌手与歌单，共享播放队列与历史。
- 顺序 / 随机播放，暂停、切歌、音量与进度控制。
- 独立终端管理界面，以及供本机程序集成的 HTTP API。
- 进程内音频播放，支持 MP3、FLAC、Ogg Vorbis 与 WAV。

## 0.1.1

- 新增试听、不可用曲目与播放错误处理策略，以及有总时限的音源准备重试。
- 默认配置目录统一为 `~/.arcana/toc/`，同步修正文档与命令行帮助。
- 修复测试代码的 lint 检查错误，Nix 软件包版本更新为 `0.1.1`。

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

配置文件：`~/.arcana/toc/config.yaml`，首次启动自动生成，与 Arcana World 的 `~/.arcana/world/` 采用同一用户目录约定。可用 `-config /path/to/config.yaml` 指定已有配置；旧路径 `~/arcana/toc/config.yaml` 不会自动迁移或回退读取，可通过 `-config` 显式指定。相对 `data_dir` 以配置文件所在目录为基准，默认数据目录为 `~/.arcana/toc/data`。修改配置后重启服务。命令行帮助：`arcana-toc -help`。

```yaml
player:
  backend: beep
  skip_unavailable: true
  allow_preview: true
  skip_on_error: false
  source_timeout: 15s
```

已有配置省略上述字段时使用这些默认值：

- `player.allow_preview`：默认 `true`，允许播放平台明确标记的试听源；设为 `false` 时按权限不足处理。此限制同时适用于网易云音乐和 QQ 音乐，不根据付费标识、时长或文件大小猜测试听。
- `player.skip_unavailable`：默认 `true`，平台明确返回版权、会员权限不足或因关闭试听而拒绝播放时，将曲目归档为 `skipped` 并继续队列。设为 `false` 时停止；`skip_on_error` 不会覆盖这一选择。
- `player.skip_on_error`：默认 `false`。开启后，其他不可重试的单曲准备错误或播放中的错误会将曲目归档为 `failed` 并继续队列；播放中断不会从头重试同一首。关闭时遇到这些错误停播。停止时，手动准备失败保留待播条目，自动准备失败归档为 `failed`。
- `player.source_timeout`：默认 `15s`，限制每次完整准备尝试，包括音源解析、可播放检查、完整音频下载与准备。必须为带单位的正时长，如 `250ms`、`15s`；零、负值、裸数字和非法文本均拒绝加载。它不是已开始播放的歌曲的时长限制。

暂时性网络故障（包括 HTTP 408、429、5xx、传输或响应读取中断）、过期音源和单次准备超时，会对同一首重新解析并重试完整准备，按固定的 1s、2s、4s、8s、8s……间隔退避。从首次准备起最多使用 **30 秒**，包含尝试和等待；单次时限还受剩余总预算约束，配置大于 30 秒也不能延长总预算。重试预算和间隔不可配置。耗尽预算后停止整个队列，即使 `skip_on_error: true` 也不跳过：当前及后续条目保留待播，状态为 `idle`，错误为 `playback preparation timed out after 30s`。请求取消、父期限到期或服务关闭会提前终止，不继续尝试下一首。

这些策略统一适用于开始播放、手动下一首、歌单首曲、自然结束续播和删除当前曲目后的续播。全部候选因跳过策略归档后停止，启动请求返回 HTTP 409；准备的 30 秒重试预算耗尽时，开始播放、下一首及歌单播放请求返回 HTTP 504。删除接口仍返回 HTTP 204，可随后通过 `GET /api/status` 查看超时错误。

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
