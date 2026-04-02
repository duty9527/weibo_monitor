# weibo_group_chat_monitor

统一的 Go 版微博监控项目，支持两种运行模式：

- `weibo`：保持 `weibo_monitor_go` 的微博抓取和 Telegram 推送能力
- `groupchat`：按上次抓取点增量回溯微博群聊、规整输出、按配置里的目标发送者聚合并推送到 Telegram
- `groupchat-history`：直接读取本地群聊历史文件，按指定 sender 过滤后推送到 Telegram

## 运行

```bash
cd weibo_group_chat_monitor
go run . weibo -config config.weibo.yaml
go run . groupchat -config config.groupchat.yaml
go run . groupchat-history -config config.groupchat.yaml
```

也支持：

```bash
go run . -mode=weibo
go run . -mode=groupchat
go run . -mode=groupchat-history
```

`groupchat` 模式每次只执行一轮，适合由 Linux `cron` 或其他外部调度器触发。

`groupchat-history` 不会启动浏览器抓取，只会读取 `output.history_file` 指向的本地历史目录或旧单文件历史，再使用 `filters.target_senders` 做 sender 过滤并通过 Telegram 推送。可选的 `local_history_push` 配置支持：

- `start_date`：起始日期，格式 `YYYY-MM-DD`
- `end_date`：结束日期，格式 `YYYY-MM-DD`
- `max_records`：最多推送最近多少条命中记录，`0` 表示不限制

群聊历史会按天写成 `YYYY-MM-DD.jsonl`。`output.history_file` 只用来确定输出目录：

- 配置为 `clean_history.jsonl` 时，会在当前目录生成每天一个 JSONL 文件
- 配置为 `history/clean_history.jsonl` 时，会在 `history/` 目录下生成每天一个 JSONL 文件
