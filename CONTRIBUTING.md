# 开发指南

## 环境与命令

需要 Go 1.26+。Linux 音频构建需要 ALSA 开发库与 pkg-config，也可通过 `nix develop` 进入开发环境。

| 命令 | 用途 |
| --- | --- |
| `make build` | 构建 `bin/arcana-toc` |
| `make test` | 全部测试，启用 race 检测 |
| `make vet` / `make lint` | 静态检查 |
| `make fmt` | Go 格式化 |
| `make tools` | 安装固定版本 golangci-lint |
| `nix build` | 构建 Nix 软件包 |
| `nix flake check` | 构建软件包并运行检查 |

## 代码层次

- `cmd/arcana-toc`：命令行、配置读取与进程生命周期。
- `internal/config`：配置默认值、严格解析与校验。
- `internal/server`：本机 HTTP API、队列调度和播放状态机。
- `internal/store`：SQLite 队列、历史与状态持久化。
- `internal/platform/{netease,qqmusic}`：平台协议、账号会话与音源解析。
- `internal/player`：音源下载、格式校验、解码与音频输出。
- `internal/tui`：独立 HTTP 客户端与终端界面。
- `internal/version`：构建版本信息。

## 贡献约定

提交使用 Conventional Commits，通过 Pull Request 审核。行为变更同步更新使用说明；测试覆盖实际行为和错误边界，保持离线、确定性与相互隔离。

默认配置和运行数据写入 `~/arcana/toc/`。凭据仅由平台适配器管理，敏感文件权限为 `0600`；日志与 API 响应不输出 Cookie 或签名音源 URL。

更新 Go 依赖后运行 `go mod tidy`，同步更新 `flake.nix` 的 `vendorHash`，通过 Go 检查与 Nix 构建。架构约定见 [架构说明](docs/architecture.md)。
