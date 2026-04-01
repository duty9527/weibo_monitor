package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"weibo_group_chat_monitor/config"
	"weibo_group_chat_monitor/weibo"
)

const (
	textLimit       = 4096
	captionLimit    = 1024
	mediaGroupLimit = 10
)

// Client 是一个最小可用的 Telegram Bot API 客户端。
type Client struct {
	cfg        config.TelegramConfig
	httpClient *http.Client
	logger     *slog.Logger
}

type apiResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      any    `json:"result"`
}

type mediaItem struct {
	Path  string
	Type  string
	Field string
}

type inputMedia struct {
	Type                  string `json:"type"`
	Media                 string `json:"media"`
	Caption               string `json:"caption,omitempty"`
	ShowCaptionAboveMedia bool   `json:"show_caption_above_media,omitempty"`
}

type GroupChatSummaryEntry struct {
	Text       string
	MediaPaths []string
}

type telegramMessage struct {
	MessageID int `json:"message_id"`
}

// NewClient 创建 Telegram 客户端。
func NewClient(cfg config.TelegramConfig, logger *slog.Logger) *Client {
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second,
		},
		logger: logger,
	}
}

// Enabled 返回通知是否启用。
func (c *Client) Enabled() bool {
	return c.cfg.Enabled
}

// SendText 发送纯文本 Telegram 消息。
func (c *Client) SendText(ctx context.Context, text string) error {
	if !c.cfg.Enabled {
		return nil
	}
	return c.sendText(ctx, text, false)
}

// SendGroupChatSummary 发送群聊摘要及其关联媒体。
func (c *Client) SendGroupChatSummary(ctx context.Context, header string, entries []GroupChatSummaryEntry) error {
	if !c.cfg.Enabled {
		return nil
	}

	blocks := buildGroupChatSummaryBlocks(header, entries, c.estimatedMediaLinkLength())
	for _, block := range blocks {
		if err := c.sendGroupChatSummaryBlock(ctx, block); err != nil {
			return err
		}
	}
	return nil
}

type groupChatSummaryBlock struct {
	Lines   []string
	Entries []GroupChatSummaryEntry
}

func buildGroupChatSummaryBlocks(header string, entries []GroupChatSummaryEntry, estimatedLinkLength int) []groupChatSummaryBlock {
	lines := make([]string, 0, len(entries)+1)
	blockEntries := make([]GroupChatSummaryEntry, 0, len(entries))
	blocks := make([]groupChatSummaryBlock, 0, 1)
	currentLength := 0

	appendLine := func(entry GroupChatSummaryEntry, line string, lineLength int) {
		lines = append(lines, line)
		blockEntries = append(blockEntries, entry)
		if currentLength == 0 {
			currentLength = lineLength
		} else {
			currentLength += 1 + lineLength
		}
	}

	flush := func() {
		if len(lines) == 0 {
			return
		}
		copiedLines := append([]string(nil), lines...)
		copiedEntries := append([]GroupChatSummaryEntry(nil), blockEntries...)
		blocks = append(blocks, groupChatSummaryBlock{
			Lines:   copiedLines,
			Entries: copiedEntries,
		})
		lines = lines[:0]
		blockEntries = blockEntries[:0]
		currentLength = 0
	}

	if header = strings.TrimSpace(header); header != "" {
		headerEntry := GroupChatSummaryEntry{Text: header}
		appendLine(headerEntry, header, len([]rune(header)))
	}

	for _, entry := range entries {
		line := strings.TrimSpace(entry.Text)
		if line == "" {
			continue
		}
		lineLength := len([]rune(line)) + len(buildExistingMediaItems(entry.MediaPaths, nil))*estimatedLinkLength
		if currentLength > 0 && currentLength+1+lineLength > captionLimit {
			flush()
		}
		appendLine(entry, line, lineLength)
	}

	flush()
	return blocks
}

func (c *Client) sendGroupChatSummaryBlock(ctx context.Context, block groupChatSummaryBlock) error {
	if len(block.Lines) == 0 {
		return nil
	}

	mediaRefs := make(map[int][]string)
	mediaMessageID := 0
	for idx, entry := range block.Entries {
		items := buildExistingMediaItems(entry.MediaPaths, c.logger)
		if len(items) == 0 {
			continue
		}

		links, firstMessageID, err := c.sendGroupChatSummaryEntryMedia(ctx, items)
		if err != nil {
			return err
		}
		if mediaMessageID == 0 {
			mediaMessageID = firstMessageID
		}
		mediaRefs[idx] = links
	}

	rendered := renderGroupChatSummaryBlockText(block, mediaRefs)
	if mediaMessageID == 0 {
		return c.sendText(ctx, rendered, false)
	}

	chunks := splitText(rendered, captionLimit)
	if len(chunks) == 0 {
		return nil
	}
	for _, chunk := range chunks[:len(chunks)-1] {
		if err := c.sendText(ctx, chunk, false); err != nil {
			return err
		}
	}
	return c.editMessageCaption(ctx, mediaMessageID, chunks[len(chunks)-1])
}

func (c *Client) sendGroupChatSummaryEntryMedia(ctx context.Context, items []mediaItem) ([]string, int, error) {
	links := make([]string, 0, len(items))
	firstMessageID := 0
	for _, item := range items {
		messageID, err := c.sendSingleMediaWithMessageID(ctx, item, "")
		if err != nil {
			return nil, 0, err
		}
		if firstMessageID == 0 {
			firstMessageID = messageID
		}
		links = append(links, c.messageLink(messageID))
	}
	return links, firstMessageID, nil
}

func renderGroupChatSummaryBlockText(block groupChatSummaryBlock, mediaRefs map[int][]string) string {
	lines := make([]string, 0, len(block.Lines))
	for idx, line := range block.Lines {
		refs := mediaRefs[idx]
		if len(refs) == 0 {
			lines = append(lines, line)
			continue
		}
		lines = append(lines, line+" "+strings.Join(refs, " "))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func (c *Client) estimatedMediaLinkLength() int {
	chatPart := strings.TrimPrefix(strings.TrimSpace(c.cfg.ChatID), "-100")
	if chatPart == "" {
		chatPart = "1234567890123"
	}
	return len([]rune(" https://t.me/c/" + chatPart + "/123456"))
}

// SendRecord 将一条微博发送到指定的 chat / topic。
func (c *Client) SendRecord(ctx context.Context, record *weibo.WeiboRecord) error {
	if !c.cfg.Enabled || record == nil {
		return nil
	}

	body := formatRecordMessage(record)
	items := buildMediaItems(record.LocalMediaPaths)
	enablePreview := len(items) == 0 && strings.TrimSpace(record.SourceURL) != ""

	if len(items) == 0 {
		if err := c.sendText(ctx, body, enablePreview); err != nil {
			return err
		}
		if len(record.FailedMediaURLs) > 0 {
			fallback := "以下媒体未成功下载，原始链接如下：\n" + strings.Join(record.FailedMediaURLs, "\n")
			return c.sendText(ctx, fallback, false)
		}
		return nil
	}

	canUseCaption := len([]rune(body)) <= captionLimit
	if canUseCaption {
		if err := c.sendMediaSet(ctx, items, body); err != nil {
			return err
		}
	} else {
		c.logger.Info("正文超过 Telegram caption 限制，拆分为文本消息 + 媒体消息", "record_id", record.ID)
		if err := c.sendText(ctx, body, false); err != nil {
			return err
		}
		if err := c.sendMediaSet(ctx, items, ""); err != nil {
			return err
		}
	}

	if len(record.FailedMediaURLs) > 0 {
		fallback := "部分媒体未成功下载，原始链接如下：\n" + strings.Join(record.FailedMediaURLs, "\n")
		if err := c.sendText(ctx, fallback, false); err != nil {
			return err
		}
	}

	return nil
}

func (c *Client) sendText(ctx context.Context, text string, enablePreview bool) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	for _, chunk := range splitText(text, textLimit) {
		if err := c.sendTextChunk(ctx, chunk, enablePreview); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) sendTextChunk(ctx context.Context, text string, enablePreview bool) error {
	values := url.Values{}
	values.Set("chat_id", c.cfg.ChatID)
	values.Set("text", text)
	if !enablePreview {
		values.Set("disable_web_page_preview", "true")
	}
	c.applyThreadValues(values)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.apiURL("sendMessage"),
		strings.NewReader(values.Encode()),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送 Telegram 文本消息失败: %w", err)
	}
	defer resp.Body.Close()

	_, err = parseAPIResponse("sendMessage", resp)
	return err
}

func (c *Client) sendMediaSet(ctx context.Context, items []mediaItem, caption string) error {
	if len(items) == 0 {
		return nil
	}

	if len(items) == 1 {
		return c.sendSingleMedia(ctx, items[0], caption)
	}

	if allGroupable(items) {
		captionUsed := false
		for start := 0; start < len(items); start += mediaGroupLimit {
			end := start + mediaGroupLimit
			end = min(len(items), end)

			groupCaption := ""
			if !captionUsed {
				groupCaption = caption
				captionUsed = true
			}

			if err := c.sendMediaGroup(ctx, items[start:end], groupCaption); err != nil {
				return err
			}
		}
		return nil
	}

	captionUsed := false
	for _, item := range items {
		itemCaption := ""
		if !captionUsed {
			itemCaption = caption
			captionUsed = true
		}
		if err := c.sendSingleMedia(ctx, item, itemCaption); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) sendSingleMedia(ctx context.Context, item mediaItem, caption string) error {
	_, err := c.sendSingleMediaWithMessageID(ctx, item, caption)
	return err
}

func (c *Client) sendSingleMediaWithMessageID(ctx context.Context, item mediaItem, caption string) (int, error) {
	file, err := os.Open(item.Path)
	if err != nil {
		return 0, fmt.Errorf("打开媒体文件失败: %w", err)
	}
	defer file.Close()

	method := singleMediaMethod(item.Type)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if err := writer.WriteField("chat_id", c.cfg.ChatID); err != nil {
		return 0, err
	}
	if err := c.applyThreadWriter(writer); err != nil {
		return 0, err
	}
	if caption = strings.TrimSpace(caption); caption != "" {
		if err := writer.WriteField("caption", caption); err != nil {
			return 0, err
		}
		if item.Type == "photo" || item.Type == "video" {
			if err := writer.WriteField("show_caption_above_media", "true"); err != nil {
				return 0, err
			}
		}
	}

	part, err := writer.CreateFormFile(item.Field, filepath.Base(item.Path))
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(part, file); err != nil {
		return 0, err
	}
	if err := writer.Close(); err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL(method), &body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("发送 Telegram 媒体失败: %w", err)
	}
	defer resp.Body.Close()

	result, err := parseAPIResponse(method, resp)
	if err != nil {
		return 0, err
	}
	return result.MessageID, nil
}

func (c *Client) sendMediaGroup(ctx context.Context, items []mediaItem, caption string) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if err := writer.WriteField("chat_id", c.cfg.ChatID); err != nil {
		return err
	}
	if err := c.applyThreadWriter(writer); err != nil {
		return err
	}

	media := make([]inputMedia, 0, len(items))
	files := make([]*os.File, 0, len(items))
	showCaptionAboveMedia := strings.TrimSpace(caption) != ""
	defer func() {
		for _, file := range files {
			file.Close()
		}
	}()

	for i, item := range items {
		file, err := os.Open(item.Path)
		if err != nil {
			return fmt.Errorf("打开媒体文件失败: %w", err)
		}
		files = append(files, file)

		attachName := fmt.Sprintf("file%d", i)
		entry := inputMedia{
			Type:                  item.Type,
			Media:                 "attach://" + attachName,
			ShowCaptionAboveMedia: showCaptionAboveMedia,
		}
		if i == 0 && strings.TrimSpace(caption) != "" {
			entry.Caption = caption
		}
		media = append(media, entry)

		part, err := writer.CreateFormFile(attachName, filepath.Base(item.Path))
		if err != nil {
			return err
		}
		if _, err := io.Copy(part, file); err != nil {
			return err
		}
	}

	mediaJSON, err := json.Marshal(media)
	if err != nil {
		return err
	}
	if err := writer.WriteField("media", string(mediaJSON)); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL("sendMediaGroup"), &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送 Telegram 图集失败: %w", err)
	}
	defer resp.Body.Close()

	_, err = parseAPIResponse("sendMediaGroup", resp)
	return err
}

func (c *Client) applyThreadValues(values url.Values) {
	if c.cfg.MessageThreadID > 0 {
		values.Set("message_thread_id", strconv.Itoa(c.cfg.MessageThreadID))
	}
	if c.cfg.DirectMessagesTopicID > 0 {
		values.Set("direct_messages_topic_id", strconv.Itoa(c.cfg.DirectMessagesTopicID))
	}
}

func (c *Client) applyThreadWriter(writer *multipart.Writer) error {
	if c.cfg.MessageThreadID > 0 {
		if err := writer.WriteField("message_thread_id", strconv.Itoa(c.cfg.MessageThreadID)); err != nil {
			return err
		}
	}
	if c.cfg.DirectMessagesTopicID > 0 {
		if err := writer.WriteField("direct_messages_topic_id", strconv.Itoa(c.cfg.DirectMessagesTopicID)); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) apiURL(method string) string {
	return fmt.Sprintf("https://api.telegram.org/bot%s/%s", c.cfg.BotToken, method)
}

func (c *Client) editMessageCaption(ctx context.Context, messageID int, caption string) error {
	values := url.Values{}
	values.Set("chat_id", c.cfg.ChatID)
	values.Set("message_id", strconv.Itoa(messageID))
	values.Set("caption", strings.TrimSpace(caption))
	values.Set("show_caption_above_media", "true")
	c.applyThreadValues(values)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.apiURL("editMessageCaption"),
		strings.NewReader(values.Encode()),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("编辑 Telegram caption 失败: %w", err)
	}
	defer resp.Body.Close()

	_, err = parseAPIResponse("editMessageCaption", resp)
	return err
}

func (c *Client) messageLink(messageID int) string {
	chatID := strings.TrimSpace(c.cfg.ChatID)
	if strings.HasPrefix(chatID, "-100") && messageID > 0 {
		return fmt.Sprintf("https://t.me/c/%s/%d", strings.TrimPrefix(chatID, "-100"), messageID)
	}
	return fmt.Sprintf("#media-%d", messageID)
}

func buildMediaItems(paths []string) []mediaItem {
	items := make([]mediaItem, 0, len(paths))
	for _, path := range paths {
		itemType, field := detectMediaKind(path)
		items = append(items, mediaItem{
			Path:  path,
			Type:  itemType,
			Field: field,
		})
	}
	return items
}

func buildExistingMediaItems(paths []string, logger *slog.Logger) []mediaItem {
	items := make([]mediaItem, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			if logger != nil {
				logger.Warn("跳过不存在的 Telegram 媒体文件", "path", path, "err", err)
			}
			continue
		}
		itemType, field := detectMediaKind(path)
		items = append(items, mediaItem{
			Path:  path,
			Type:  itemType,
			Field: field,
		})
	}
	return items
}

func detectMediaKind(path string) (itemType string, field string) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return "photo", "photo"
	case ".mp4", ".mov", ".m4v":
		return "video", "video"
	default:
		return "document", "document"
	}
}

func singleMediaMethod(itemType string) string {
	switch itemType {
	case "photo":
		return "sendPhoto"
	case "video":
		return "sendVideo"
	default:
		return "sendDocument"
	}
}

func allGroupable(items []mediaItem) bool {
	for _, item := range items {
		if item.Type != "photo" && item.Type != "video" {
			return false
		}
	}
	return true
}

func formatRecordMessage(record *weibo.WeiboRecord) string {
	lines := make([]string, 0, 4)

	headerParts := make([]string, 0, 2)
	headerParts = append(headerParts, formatRecordTag(record))
	if createdAt := formatDisplayTime(record.CreatedAt); createdAt != "" {
		headerParts = append(headerParts, createdAt)
	}
	if len(headerParts) > 0 {
		lines = append(lines, strings.Join(headerParts, " "))
	}

	text := strings.TrimSpace(record.Text)
	if text != "" {
		lines = append(lines, "", normalizeTelegramText(text))
	}
	if record.SkippedMediaCount > 0 {
		lines = append(
			lines,
			"",
			fmt.Sprintf("提示：相关媒体已在前文中发送，本次跳过重复发送（%d 个）", record.SkippedMediaCount),
		)
	}

	sourceURL := strings.TrimSpace(record.SourceURL)
	if sourceURL != "" {
		lines = append(lines, "", sourceURL)
	}

	if len(lines) == 0 {
		return "微博更新"
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func normalizeTelegramText(text string) string {
	if !strings.Contains(text, "http://") && !strings.Contains(text, "https://") {
		return text
	}

	var builder strings.Builder
	builder.Grow(len(text) + 8)

	for i := 0; i < len(text); {
		if strings.HasPrefix(text[i:], "http://") || strings.HasPrefix(text[i:], "https://") {
			start := i
			for i < len(text) {
				b := text[i]
				if b >= utf8.RuneSelf || unicode.IsSpace(rune(b)) {
					break
				}
				i++
			}
			urlPart, trailing := splitTelegramURLSuffix(text[start:i])
			builder.WriteString(urlPart)
			if trailing != "" {
				builder.WriteByte(' ')
				builder.WriteString(trailing)
			}
			if i < len(text) {
				r, size := utf8.DecodeRuneInString(text[i:])
				if unicode.IsLetter(r) || unicode.IsNumber(r) || needsTelegramURLSeparator(r) {
					builder.WriteByte(' ')
				}
				builder.WriteRune(r)
				i += size
			}
			continue
		}

		r, size := utf8.DecodeRuneInString(text[i:])
		builder.WriteRune(r)
		i += size
	}

	return builder.String()
}

func splitTelegramURLSuffix(value string) (string, string) {
	end := len(value)
	for end > 0 {
		r, size := utf8.DecodeLastRuneInString(value[:end])
		if !isTelegramURLTrailingPunctuation(r) {
			break
		}
		end -= size
	}
	return value[:end], value[end:]
}

func needsTelegramURLSeparator(r rune) bool {
	return isTelegramURLTrailingPunctuation(r) || isTelegramURLAdjacentBracket(r)
}

func isTelegramURLTrailingPunctuation(r rune) bool {
	switch r {
	case '.', ',', ';', ':', '!', '?',
		'，', '。', '、', '；', '：', '！', '？',
		')', ']', '}', '>', '）', '】', '》', '」', '』', '〉', '〕', '］', '｝',
		'"', '\'', '”', '’':
		return true
	default:
		return false
	}
}

func isTelegramURLAdjacentBracket(r rune) bool {
	switch r {
	case '(', '[', '{', '<', '（', '【', '《', '「', '『', '〈', '〔', '［', '｛':
		return true
	default:
		return false
	}
}

func formatRecordTag(record *weibo.WeiboRecord) string {
	if record != nil && record.IsRetweet {
		return "#转发"
	}
	return "#原创"
}

func formatDisplayTime(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	parsed, err := weibo.ParseWeiboTime(value)
	if err != nil {
		return "【" + value + "】"
	}

	return "【" + parsed.In(time.Local).Format("06-01-02 15:04:05") + "】"
}

func splitText(text string, limit int) []string {
	if text == "" {
		return nil
	}

	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}

	var chunks []string
	for len(runes) > limit {
		cut := limit
		for i := limit; i > limit/2; i-- {
			if runes[i-1] == '\n' {
				cut = i
				break
			}
		}
		chunks = append(chunks, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
	}

	if len(runes) > 0 {
		chunks = append(chunks, strings.TrimSpace(string(runes)))
	}

	return chunks
}

func parseAPIResponse(method string, resp *http.Response) (*telegramMessage, error) {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s 返回 HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result apiResponse
	if len(body) == 0 {
		return &telegramMessage{}, nil
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return &telegramMessage{}, nil
	}
	if !result.OK {
		return nil, fmt.Errorf("%s 返回失败: %s", method, result.Description)
	}

	msg := &telegramMessage{}
	if result.Result == nil {
		return msg, nil
	}

	switch typed := result.Result.(type) {
	case map[string]any:
		if value, ok := typed["message_id"].(float64); ok {
			msg.MessageID = int(value)
		}
	case []any:
		if len(typed) > 0 {
			if first, ok := typed[0].(map[string]any); ok {
				if value, ok := first["message_id"].(float64); ok {
					msg.MessageID = int(value)
				}
			}
		}
	}
	return msg, nil
}
