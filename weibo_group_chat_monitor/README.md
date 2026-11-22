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

长期运行时，每次 `/meta/connect` 都会根据服务端 `advice.timeout` 加 15 秒宽限设置请求截止时间；服务端未给出建议时使用 `subscription.connect_timeout_seconds`（默认 90 秒）。超时会重建订阅并执行历史补漏，避免 TCP 仍显示连接但应用层已经假死。程序另按 `subscription.auth_check_interval_hours`（默认 6 小时）验证登录态并访问主站进行 SSO 刷新。启动时检查 ALF、SUB、SUBP 中最早的到期时间；进入 `subscription.proactive_refresh_before_expiry_hours`（默认 96 小时）窗口就尝试预刷新。尝试记录保存在 `subscription.auth_refresh_state_file`，调度使用持久化时间，断线重连不会重置等待期限；6 小时内限频，之后允许重试同一凭据；不会因为一次失败永久跳过刷新。主站加载后等待 SSO，再访问聊天页，自动恢复最多额外等待 20 秒。主动刷新失败但旧凭据仍有效时保留旧连接。新 Cookie 必须同时通过登录接口和 Bayeux handshake 才会写入缓存。

认证恢复失败后，程序在进程内按 1、2、4 分钟递增退避，最长 30 分钟，不再不断重启浏览器进程。通过 `subscription.runtime_alert_state_file` 持久化告警，首次失败通知、每 24 小时最多提醒一次，认证恢复后通知并清除状态。`subscription.processed_state_file + ".health.json"` 记录最近认证、订阅和收信阶段及时间，不能只用 systemd running 判断业务健康。

程序在启动、连接中断重建以及服务端要求重新 handshake 后，通过纯 Go 调用群历史接口，并从最新消息向前翻页直到本地 `last_message_id`，将断线期间消息重新送入同一套去重、媒体下载和每日 JSONL 流程。首次没有边界时默认只记录最新位置，不倒灌旧历史；由 `subscription.backfill_on_first_start` 控制。若达到 `backfill_max_pages` 仍未找到边界，程序会保留旧边界并重试补漏，不会继续接收实时消息而覆盖缺口。长时间离线时应适当提高 `backfill_max_pages` 并确认日志中的 `reached_boundary=true`。

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

## 同账号登录恢复与推送容错

微博通过 Playwright 校验成功后，将完整域作用域的 Cookie 快照原子写入 `weibo.user_data_dir/weibo_session_cookies.json`，权限为 `0600`。群聊可配置：

```yaml
subscription:
  fallback_cookie_file: "weibo_user_data/weibo_session_cookies.json"
  expected_uid: "你的登录账号 UID（不是被监控用户 UID）"
```

群聊使用快照前必须验证登录 UID 与 WebIM 握手；两个浏览器仍使用独立目录，不能同时打开同一 Chromium profile。快照仅在本机保存，不能提交到仓库。若两个登录态都被服务端撤销，仍需人工扫码，不能保证永久免登录。

仅检查/刷新认证、不发送消息：

```bash
./weibo_monitor weibo-auth -config config.weibo.yaml
./weibo_monitor groupchat-auth -config config.groupchat.yaml
./weibo_monitor groupchat-auth -refresh -config config.groupchat.yaml
```

认证命令与使用同一浏览器目录的常驻任务应串行运行。微博可使用已有 `.weibo_poll.lock`；群聊先停止订阅服务。

微博将历史 JSONL 作为待发送内容存储，在状态文件中按微博 ID 记录发送确认、失败次数和下次重试时间。升级时固定原推送水位作为基线，后续单条失败不会阻塞其他微博，也不会被最新水位跳过。抓取失败时仍尝试发送本地积压；状态文件原子替换，保存失败则停止后续发送。单条消息含多个 Telegram 请求时仍为至少一次交付：网络结果不明或发送成功后进程退出可能导致部分重复。

超过 49,000,000 字节的微博媒体在上传前降级成正文和原微博链接；服务端仍返回 HTTP 413 时立即降级，不再重复上传同一文件。降级不将未上传媒体标记成已发送媒体。HTTP 429 按服务端 `retry_after` 等待后重试；其他推送失败按 15 分钟至 6 小时退避，持久化后继续其他消息。

systemd 部署模板通过 duty 用户的 shell 打开日志，避免 `StandardOutput=append:` 创建 root 所有的日志而与 logrotate 的 `su duty duty` 冲突。既有部署需确保当前日志可由 duty 写入，保留旧日志后预建 duty 所有的新日志，再重启打开文件；使用 `logrotate -f -s <临时状态路径> /etc/logrotate.d/weibo_monitor` 验证。
