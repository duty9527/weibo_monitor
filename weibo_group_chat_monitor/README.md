# weibo_group_chat_monitor

统一的 Go 版微博监控项目，支持以下运行模式：

- `weibo`：保持 `weibo_monitor_go` 的微博抓取和 Telegram 推送能力
- `groupchat`：按上次抓取点增量回溯微博群聊、规整输出、按配置里的目标发送者聚合并推送到 Telegram
- `groupchat-subscribe`：临时使用 Playwright 获取登录 Cookie，关闭浏览器后由纯 Go 常驻订阅微博实时事件
- `groupchat-history`：直接读取本地群聊历史文件，按指定 sender 过滤后推送到 Telegram

## 运行

```bash
cd weibo_group_chat_monitor
go run . weibo -config config.weibo.yaml
go run . groupchat -config config.groupchat.yaml
go run . groupchat-subscribe -config config.groupchat.yaml
go run . groupchat-history -config config.groupchat.yaml
```

也支持：

```bash
go run . -mode=weibo
go run . -mode=groupchat
go run . -mode=groupchat-history
```

`groupchat` 模式每次只执行一轮，适合由 Linux `cron` 或其他外部调度器触发。

`groupchat-subscribe` 会先验证 `subscription.cookie_cache_file` 中的 Cookie。缓存无效时，程序先短暂使用无头 Playwright 恢复 `browser.user_data_dir` 中的登录态；仍未登录才打开可见浏览器等待扫码。登录成功后 Cookie 会以 `0600` 权限保存，Chromium 随即关闭，后续由纯 Go 订阅 `/im/{uid}`。订阅认证失效时会再次执行相同的刷新流程。

订阅使用 Bayeux ACK，并遵循服务端的 `retry`、`handshake` 和 `none` 重连指令。所有原始事件写入 `subscription.event_log_file` 供审计；321 新消息会写入按日期划分的历史 JSONL，重复投递通过 `subscription.processed_state_file` 和历史消息 ID 双重过滤；331 撤回事件会在原消息上增加 `recalled`、`recalled_at` 和 `recall_text`。实时消息中的 `fids` 图片和 `page_info` 图片/视频会沿用历史抓取模式的地址选择及文件命名规则，下载到 `output.media_dir`，失败详情写入 `failed_media_messages.jsonl`。

常驻运行可使用仓库中的 `weibo-groupchat-subscribe.service.example`。先构建并替换示例中的用户和绝对路径：

```bash
go build -o weibo_group_chat_monitor .
sudo cp weibo-groupchat-subscribe.service.example /etc/systemd/system/weibo-groupchat-subscribe.service
sudo systemctl daemon-reload
sudo systemctl enable --now weibo-groupchat-subscribe.service
journalctl -u weibo-groupchat-subscribe.service -f
```

首次扫码需要桌面环境；可以先在终端手动运行一次完成扫码，之后 systemd 通常可直接复用 Cookie。Cookie 过期且无法访问图形会话时，服务会持续重试并在日志中报告刷新失败。

`groupchat-history` 不会启动浏览器抓取，只会读取 `output.history_file` 指向的本地历史目录或旧单文件历史，再使用 `filters.target_senders` 做 sender 过滤并通过 Telegram 推送。可选的 `local_history_push` 配置支持：

- `start_date`：起始日期，格式 `YYYY-MM-DD`
- `end_date`：结束日期，格式 `YYYY-MM-DD`
- `max_records`：最多推送最近多少条命中记录，`0` 表示不限制

群聊历史会按天写成 `YYYY-MM-DD.jsonl`。`output.history_file` 只用来确定输出目录：

- 配置为 `clean_history.jsonl` 时，会在当前目录生成每天一个 JSONL 文件
- 配置为 `history/clean_history.jsonl` 时，会在 `history/` 目录下生成每天一个 JSONL 文件

## 纯 Go 实时订阅探针

`cmd/groupchat_subscribe_probe` 不启动 Playwright。它直接与微博 WebIM 的 CometD/Bayeux 服务握手，并通过长连接订阅当前账号的消息频道：

```bash
# 无需登录，只验证 Bayeux 服务和支持的传输类型
go run ./cmd/groupchat_subscribe_probe -handshake-only -insecure

# 使用 Netscape 格式的 Cookie 文件监听两分钟
go run ./cmd/groupchat_subscribe_probe -cookie-file /path/to/cookies.txt -duration 2m -insecure

# 或通过环境变量提供 Cookie，避免将 Cookie 写进命令行历史
WEIBO_COOKIE='SUB=...; SUBP=...' go run ./cmd/groupchat_subscribe_probe -duration 2m -insecure
```

默认启用 TLS 校验。只有在测试机无法验证 `web.im.weibo.com` 当前证书链时才使用 `-insecure`。订阅探针只打印收到的原始事件，不读取群聊历史、不写状态文件，也不发送 Telegram 消息。
