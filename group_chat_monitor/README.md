# group_chat_monitor

使用 Go + Playwright-Go 抓取微博群聊历史，并尽量复现 `monitor_with_images.py` 的行为：

- 进入指定群聊页面并等待 `query_messages.json` 返回
- 通过 `max_mid` 继续向前翻取历史消息
- 处理文本、`page_info` 图片/视频，以及 `fids` 对应的媒体下载
- 将结果追加写入 `clean_history.jsonl`

## 运行

1. 准备配置文件：

```bash
cd group_chat_monitor
cp config.example.yaml config.yaml
```

2. 首次运行：

```bash
go run . -config config.yaml
```

如果 `user_data_dir` 里还没有微博登录态，程序会拉起 Chromium 持久化上下文。保持页面打开并完成登录后，程序会继续等待群聊接口响应。

## 输出

- 历史记录：`output.history_file`
- 下载媒体：`output.media_dir`

输出 JSONL 字段与现有 Python 脚本对齐：

- `id`
- `time`
- `sender`
- `message`
- `downloaded_media`
