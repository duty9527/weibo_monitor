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

`weibo` 模式每次运行都会先从配置或浏览器数据目录读取并验证 Cookie；验证成功时不会启动 Playwright，只有 Cookie 无法读取或验证失败时才使用持久化浏览器恢复登录。旧配置项 `weibo.playwright_refresh_hours` 已弃用，不再触发周期性浏览器保活。

微博与群聊配置默认使用 `log.level: error`，因此应用只向标准输出写 ERROR 级别日志；systemd journal 中不会保存 INFO/WARN 运行明细。需要临时排查连接问题时可改成 `info`，排查结束后恢复为 `error`。

`groupchat-subscribe` 会先验证 `subscription.cookie_cache_file` 中的 Cookie。缓存无效时，程序先短暂使用无头 Playwright 恢复 `browser.user_data_dir` 中的登录态；仍未登录才打开可见浏览器等待扫码。登录成功后 Cookie 会以 `0600` 权限保存，Chromium 随即关闭，后续由纯 Go 订阅 `/im/{uid}`。订阅认证失效时会再次执行相同的刷新流程。

长期运行时，每次 `/meta/connect` 都会根据服务端 `advice.timeout` 加 15 秒宽限设置请求截止时间；服务端未给出建议时使用 `subscription.connect_timeout_seconds`（默认 90 秒）。超时会重建订阅并执行历史补漏，避免 TCP 仍显示连接但应用层已经假死。程序另按 `subscription.auth_check_interval_hours`（默认 6 小时）用纯 Go 验证登录态，不会因此启动浏览器。当 `ALF` 距离过期少于 `subscription.proactive_refresh_before_expiry_hours`（默认 96 小时）时，只针对该过期时间尝试一次无头 Playwright 预刷新；尝试记录保存在 `subscription.auth_refresh_state_file`，避免进程重启后重复刷新。新 Cookie 必须同时通过登录接口和 Bayeux handshake 才会写入缓存。

缓存 Cookie、无头 Playwright 恢复和扫码登录全部失败后，程序会向 Telegram 发送一次 `#运行异常` 告警，并使用 `subscription.runtime_alert_state_file` 跨进程重启去重。同一认证故障不会反复刷屏；认证恢复后会清除状态，使下一次独立故障仍能告警。

程序在启动、连接中断重建以及服务端要求重新 handshake 后，通过纯 Go 调用群历史接口，并从最新消息向前翻页直到本地 `last_message_id`，将断线期间消息重新送入同一套去重、媒体下载和每日 JSONL 流程。首次没有边界时默认只记录最新位置，不倒灌旧历史；由 `subscription.backfill_on_first_start` 控制。若达到 `backfill_max_pages` 仍未找到边界，程序不会冒进更新水位线。

订阅使用 Bayeux ACK，并遵循服务端的 `retry`、`handshake` 和 `none` 重连指令。所有原始事件按天写入 `subscription.event_log_file/YYYY-MM-DD.jsonl` 供审计，并按 `subscription.event_log_retention_days`（默认 7 天）自动清理；321 新消息会写入按日期划分的历史 JSONL，重复投递通过 `subscription.processed_state_file` 和历史消息 ID 双重过滤；331 撤回事件会在原消息上增加 `recalled`、`recalled_at` 和 `recall_text`。实时消息中的 `fids` 图片或普通文件附件，以及 `page_info` 图片/视频，会沿用历史抓取模式的地址选择及文件命名规则下载到 `output.media_dir`；普通附件通过 Telegram `sendDocument` 发送。失败详情写入 `failed_media_messages.jsonl`，媒体接口明确返回认证失效时会先刷新 Cookie，再由历史补漏重新处理该消息。

`groupchat-subscribe` 会对 `filters.target_senders` 或 `filters.target_sender_uids` 命中的新消息进行实时 Telegram 推送。实时通知只显示群聊标签、发送者和消息时间/正文，不使用批量摘要中的“发送了 N 条消息”标题。消息先持久化到 `subscription.notification_queue_file`，发送失败按指数退避重试，进程重启后继续处理，避免 Telegram 短暂故障造成漏推；UID 匹配不受用户改名影响。

部署到 `/home/duty/weibo_cron` 时，可使用仓库中的 `deploy/weibo_monitor.service`。服务名称和可执行文件名称均为 `weibo_monitor`：

```bash
go build -o weibo_group_chat_monitor .
sudo cp deploy/weibo_monitor.service /etc/systemd/system/weibo_monitor.service
sudo systemctl daemon-reload
sudo systemctl enable --now weibo_monitor.service
journalctl -u weibo_monitor.service -f
```

首次扫码需要桌面环境；可以先在终端手动运行一次完成扫码，之后 systemd 通常可直接复用 Cookie。Cookie 过期且无法访问图形会话时，服务会持续重试并在日志中报告刷新失败。

`groupchat-history` 不会启动浏览器抓取，只会读取 `output.history_file` 指向的本地历史目录或旧单文件历史，再使用 `filters.target_senders` 或 `filters.target_sender_uids` 过滤并通过 Telegram 推送。可选的 `local_history_push` 配置支持：

- `start_date`：起始日期，格式 `YYYY-MM-DD`
- `end_date`：结束日期，格式 `YYYY-MM-DD`
- `max_records`：最多推送最近多少条命中记录，`0` 表示不限制

`groupchat-media-retry` 会读取历史目录中的 `failed_media_messages.jsonl`，使用当前 Cookie 重试失败媒体，并按消息 ID 原子回写每日历史文件。已存在且文件有效的媒体会跳过；该模式不会发送 Telegram，也不会删除失败审计记录。

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
