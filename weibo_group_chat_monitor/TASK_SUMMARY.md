# 阶段任务总结

更新时间：2026-04-02

## 已完成

### 1. 项目落地

- 已在项目目录下落地独立 Go 项目 `weibo_group_chat_monitor`
- 单一二进制支持两种模式：`weibo` 和 `groupchat`
- 入口文件为 [main.go](main.go)

### 2. 微博模式

- 保留了微博抓取、Cookie 刷新、状态推进、Telegram 推送能力
- 主要实现位于 [weibo_mode.go](weibo_mode.go)

### 3. 群聊模式

- 已改为单次执行模式，不再内置自动调度，适合由 Linux `cron` 触发
- 摘要推送支持按 `filters.target_senders` 过滤，只汇总指定 sender 的消息
- 支持基于 `state` 的增量抓取
- 历史消息写盘前按时间排序
- `state` 保存的是当前抓取边界上的最新消息，而不是最后写入磁盘的最旧消息
- 历史消息按天输出为 `YYYY-MM-DD.jsonl`
- 若当天文件已存在则追加，不存在则创建
- 保留了对旧单文件历史格式的兼容读取
- 已确认提醒模块只基于本次运行的 `NewRecords` 发送
- 已确认若本地历史文件中已存在同一消息 ID，则该消息不会再次进入摘要提醒

### 4. 群聊摘要与 Telegram 媒体同步

- 已补上 group chat 任务后摘要媒体同步到 Telegram 的能力
- 摘要仍保持“一行一条消息”的文本样式，不会改成每条群聊消息单独发一条
- 发送时按 Telegram `caption` 容量切分摘要块，而不是按媒体出现位置切分
- 每个摘要块会收集本块内涉及到的全部媒体文件并一并发送
- 已支持先发送媒体、再编辑首个媒体 caption 的两阶段流程
- 摘要文本中对应媒体的那一行，现会补成可跳转到本次发送媒体消息的 Telegram 链接
- 当块内摘要文本超出 `caption` 限制时，超出的前半部分会先作为普通文本消息发送，最后一段再写入媒体 caption
- 当本地媒体文件缺失时，该条会退化为纯文本摘要，不会阻断整批摘要发送

### 5. 本地历史筛选与 Telegram 推送

- 已新增独立运行模式 `groupchat-history`
- 该模式不会启动浏览器抓取，只读取本地聊天记录历史
- 支持读取按天输出的 `YYYY-MM-DD.jsonl` 历史目录
- 也兼容读取旧的单文件历史格式
- 支持按 `filters.target_senders` 过滤指定 sender 的历史消息
- 支持通过 `local_history_push.start_date` / `end_date` 限定日期范围
- 支持通过 `local_history_push.max_records` 仅推送最近命中的若干条
- 命中的历史记录会沿用现有群聊摘要与媒体同步链路推送到 Telegram

### 6. 停止条件

- 增加了基于时间的停止条件 `stop_condition.target_time`
- 当配置为 `YYYY-MM-DD HH:MM:SS` 时，消息时间小于等于该时间即停止
- 当配置为 `YYYY-MM-DD` 时，按该日期 `00:00:00` 处理
- 已修复批次内停止逻辑，命中停止边界后不会继续把更早的消息写入结果

### 7. 媒体下载

- 图片或附件下载支持三层回退：
- 响应监听下载
- 浏览器内 `fetch`
- Go HTTP 客户端直连并携带浏览器 Cookie
- 当前只有在三层都失败时，才会把失败消息写入失败记录文件
- 如果前两层失败但第三层成功，现在会打印明确的成功日志

### 8. 失败消息原始 JSON 保存

- 已支持在媒体最终下载失败时，将对应消息原始 JSON 追加写入 `failed_media_messages.jsonl`
- 记录内容包括消息 ID、时间、sender、媒体引用、错误信息和完整 `raw_message`

### 9. 引用消息排查

- 已新增诊断命令 [cmd/inspect_quote/main.go](cmd/inspect_quote/main.go)
- 已修复该命令对数值型消息 ID 的解析问题
- 已成功抓到一条引用消息原始 JSON，目标消息 ID 为 `5282330998475900`
- 当前确认结果：
- 该类引用消息没有独立的结构化引用字段
- 引用内容直接编码在 `content` 中
- 样本消息的 `type` 为 `321`
- 样本中未发现单独的被引用消息 ID、发送者、时间或正文对象

## 已验证

- 已执行 `GOCACHE=/tmp/weibo_group_chat_monitor_gocache go test -v -count=1 ./telegram`
- 已执行 `GOCACHE=/tmp/weibo_group_chat_monitor_gocache go test -v -count=1 -run 'TestBuildSenderSummaryMessages|TestBuildSenderSummariesCollectsMediaPaths' ./groupchat`
- 已新增本地历史读取测试，覆盖：
- 按天历史目录读取
- 旧单文件历史兼容读取
- sender 过滤
- 日期范围过滤
- `max_records` 限流
- 旧单文件与按天文件重复消息去重
- 新增摘要媒体同步相关测试，覆盖：
- caption 分块
- 媒体发送后编辑 caption
- 媒体行跳转链接生成
- 本地媒体缺失时退化为纯文本
- 已实际检查引用消息原始结构样本

## 当前结论

- 群聊抓取、按天落盘、按 sender 汇总、停止条件、媒体回退下载、失败原始 JSON 记录能力均已落地
- group chat 摘要已支持把本地媒体文件同步发送到 Telegram
- 已支持直接读取本地历史并按 sender 过滤后推送到 Telegram
- 当前摘要发送模型已改为按 `caption` 容量切块，并在摘要文本中插入指向本块媒体消息的跳转链接
- 当前去重逻辑依赖历史文件中的消息 ID；若本地已存在同 ID 消息，则不会再次进入提醒模块
- 引用消息暂时只能通过解析 `content` 中的分隔线文本识别，现阶段不能依赖结构化字段

## 尚未继续做的内容

- 还没有把引用消息进一步拆成结构化字段，例如 `quote_header`、`quote_body`、`quote_type`
- 当前媒体跳转链接对 Telegram 私有超级群 `-100...` chat ID 已可生成真实 `t.me/c/.../...` 链接；若后续需要兼容其他 chat 形态，可以继续补更稳的链接策略
- 如果后续需要，可以在现有清洗输出上继续补这一层解析
