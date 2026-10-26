# 服务架构

## 进程与模块

`cmd/arcana-toc` 装配配置、存储、平台适配器、播放器和 HTTP 服务，负责信号处理与有序关闭。终端界面是独立 HTTP 客户端，不直接访问数据库与凭据。

服务唯一维护业务队列。`internal/server` 负责调度，`internal/store` 负责持久化，`internal/platform` 负责平台账号和临近播放时的授权音源解析，`internal/player` 负责下载、解码与宿主音频输出。

## 配置与存储

默认配置位于 `~/arcana/toc/config.yaml`，首次启动生成；显式 `-config` 路径须已存在。配置严格拒绝未知字段，相对 `data_dir` 以配置目录为基准。

SQLite 使用 `modernc.org/sqlite`、WAL 与单连接，数据库名保留为 `arcanatoc.db`。服务持有 `service.lock`，保证每个数据目录只有一个写入进程。队列只保存平台与稳定曲目身份，不保存临时签名 URL。启动时将上次遗留的播放条目标为中断。

平台 Cookie 会话分别保存在 `credentials/netease.json` 与 `credentials/qqmusic.json`。目录权限为 `0700`，配置、数据库与凭据为 `0600`。

## 播放

Beep/Oto 在进程内解码和输出 MP3、FLAC、Ogg Vorbis、WAV。音频设备首次播放时初始化；播放前下载授权音源至受限临时文件，停止和关闭时释放解码器与临时资源。

服务按顺序或随机模式从待播条目中选择下一首，完成条目进入历史。歌单播放先完整读取目录，再替换活动队列并开始首曲；读取失败保留旧队列。音频停止与数据库事务相互独立，数据库回滚无法恢复已停止的音频。

平台适配器在播放前解析音源并执行 CDN 范围预检，403 时同曲重新解析一次。平台拒绝、账号权限与设备错误按真实结果返回。

## HTTP 与日志

标准库 `net/http` 提供本机 JSON API。监听地址必须是字面量 loopback 地址，拒绝浏览器 `Origin`，响应设置 `no-store`。API 不要求控制令牌，本机进程可操作播放与平台账号；外部面板负责自己的用户鉴权。

`slog` 提供文本 / JSON 日志与级别选择。日志不记录 Cookie、扫码私密标识或签名音源查询串。
