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

	return parseAPIResponse("sendMessage", resp)
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
	file, err := os.Open(item.Path)
	if err != nil {
		return fmt.Errorf("打开媒体文件失败: %w", err)
	}
	defer file.Close()

	method := singleMediaMethod(item.Type)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if err := writer.WriteField("chat_id", c.cfg.ChatID); err != nil {
		return err
	}
	if err := c.applyThreadWriter(writer); err != nil {
		return err
	}
	if caption = strings.TrimSpace(caption); caption != "" {
		if err := writer.WriteField("caption", caption); err != nil {
			return err
		}
		if item.Type == "photo" || item.Type == "video" {
			if err := writer.WriteField("show_caption_above_media", "true"); err != nil {
				return err
			}
		}
	}

	part, err := writer.CreateFormFile(item.Field, filepath.Base(item.Path))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL(method), &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送 Telegram 媒体失败: %w", err)
	}
	defer resp.Body.Close()

	return parseAPIResponse(method, resp)
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

	return parseAPIResponse("sendMediaGroup", resp)
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

func parseAPIResponse(method string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s 返回 HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result apiResponse
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil
	}
	if !result.OK {
		return fmt.Errorf("%s 返回失败: %s", method, result.Description)
	}
	return nil
}
