# weibo_group_chat_monitor

统一的 Go 版微博监控项目，支持两种运行模式：

- `weibo`：保持 `weibo_monitor_go` 的微博抓取和 Telegram 推送能力
- `groupchat`：按上次抓取点增量回溯微博群聊、规整输出、按配置里的目标发送者聚合并推送到 Telegram

## 运行

```bash
cd weibo_group_chat_monitor
go run . weibo -config config.weibo.yaml
go run . groupchat -config config.groupchat.yaml
```

也支持：

```bash
go run . -mode=weibo
go run . -mode=groupchat
```

`groupchat` 模式每次只执行一轮，适合由 Linux `cron` 或其他外部调度器触发。

群聊历史会按天写成 `YYYY-MM-DD.jsonl`。`output.history_file` 只用来确定输出目录：

- 配置为 `clean_history.jsonl` 时，会在当前目录生成每天一个 JSONL 文件
- 配置为 `history/clean_history.jsonl` 时，会在 `history/` 目录下生成每天一个 JSONL 文件
