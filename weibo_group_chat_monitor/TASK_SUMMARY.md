# 阶段任务总结

更新时间：2026-03-31

## 已完成

### 1. 项目落地

- 已在项目目录下落地独立 Go 项目 `weibo_group_chat_monitor`
- 单一二进制支持两种模式：`weibo` 和 `groupchat`
- 入口文件为 [main.go](/Users/duty/perminal/weibo_chat_monitor/weibo_group_chat_monitor/main.go)

### 2. 微博模式

- 保留了微博抓取、Cookie 刷新、状态推进、Telegram 推送能力
- 主要实现位于 [weibo_mode.go](/Users/duty/perminal/weibo_chat_monitor/weibo_group_chat_monitor/weibo_mode.go)

### 3. 群聊模式

- 已改为单次执行模式，不再内置自动调度，适合由 Linux `cron` 触发
- 摘要推送支持按 `filters.target_senders` 过滤，只汇总指定 sender 的消息
- 支持基于 `state` 的增量抓取
- 历史消息写盘前按时间排序
- `state` 保存的是当前抓取边界上的最新消息，而不是最后写入磁盘的最旧消息
- 历史消息按天输出为 `YYYY-MM-DD.jsonl`
- 若当天文件已存在则追加，不存在则创建
- 保留了对旧单文件历史格式的兼容读取

### 4. 停止条件

- 增加了基于时间的停止条件 `stop_condition.target_time`
- 当配置为 `YYYY-MM-DD HH:MM:SS` 时，消息时间小于等于该时间即停止
- 当配置为 `YYYY-MM-DD` 时，按该日期 `00:00:00` 处理
- 已修复批次内停止逻辑，命中停止边界后不会继续把更早的消息写入结果

### 5. 媒体下载

- 图片或附件下载支持三层回退：
- 响应监听下载
- 浏览器内 `fetch`
- Go HTTP 客户端直连并携带浏览器 Cookie
- 当前只有在三层都失败时，才会把失败消息写入失败记录文件
- 如果前两层失败但第三层成功，现在会打印明确的成功日志

### 6. 失败消息原始 JSON 保存

- 已支持在媒体最终下载失败时，将对应消息原始 JSON 追加写入 `failed_media_messages.jsonl`
- 记录内容包括消息 ID、时间、sender、媒体引用、错误信息和完整 `raw_message`

### 7. 引用消息排查

- 已新增诊断命令 [cmd/inspect_quote/main.go](/Users/duty/perminal/weibo_chat_monitor/weibo_group_chat_monitor/cmd/inspect_quote/main.go)
- 已修复该命令对数值型消息 ID 的解析问题
- 已成功抓到一条引用消息原始 JSON，目标消息 ID 为 `5282330998475900`
- 当前确认结果：
- 该类引用消息没有独立的结构化引用字段
- 引用内容直接编码在 `content` 中
- 样本消息的 `type` 为 `321`
- 样本中未发现单独的被引用消息 ID、发送者、时间或正文对象

## 已验证

- 已执行 `GOCACHE=/tmp/weibo_group_chat_monitor_gocache go test ./...`
- 当前项目测试通过
- 已实际检查引用消息原始结构样本

## 当前结论

- 群聊抓取、按天落盘、按 sender 汇总、停止条件、媒体回退下载、失败原始 JSON 记录能力均已落地
- 引用消息暂时只能通过解析 `content` 中的分隔线文本识别，现阶段不能依赖结构化字段

## 尚未继续做的内容

- 还没有把引用消息进一步拆成结构化字段，例如 `quote_header`、`quote_body`、`quote_type`
- 如果后续需要，可以在现有清洗输出上继续补这一层解析
