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

	blocks := buildGroupChatSummaryBlocks(header, entries)
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

type renderedSummaryLine struct {
	Markdown      string
	VisibleLength int
}

func buildGroupChatSummaryBlocks(header string, entries []GroupChatSummaryEntry) []groupChatSummaryBlock {
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
		lineLength := visibleGroupChatSummaryLineLength(line, len(buildExistingMediaItems(entry.MediaPaths, nil)))
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

	renderedLines := buildRenderedGroupChatSummaryLines(block, mediaRefs, captionLimit)
	textChunks, caption := splitRenderedSummaryDelivery(renderedLines, captionLimit, textLimit)
	for _, chunk := range textChunks {
		if err := c.sendMarkdownText(ctx, chunk); err != nil {
			return err
		}
	}
	if strings.TrimSpace(caption) == "" {
		return nil
	}
	return c.editMessageCaptionMarkdown(ctx, mediaMessageID, caption)
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
		lines = append(lines, renderGroupChatSummaryLine(line, refs))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func renderGroupChatSummaryLine(line string, refs []string) string {
	line = strings.TrimSpace(line)
	if len(refs) == 0 {
		return line
	}

	parts := make([]string, 0, len(refs)+1)
	if line != "" {
		parts = append(parts, line)
	}
	parts = append(parts, refs[0])
	for idx, ref := range refs[1:] {
		parts = append(parts, fmt.Sprintf("媒体%d: %s", idx+2, ref))
	}
	return strings.Join(parts, " ")
}

func renderGroupChatSummaryLineMarkdown(line string, refs []string) string {
	line = strings.TrimSpace(line)
	if len(refs) == 0 {
		return escapeTelegramMarkdownV2(line)
	}

	lines := strings.Split(line, " ")
	time := ""
	text := ""
	if len(lines) > 1 {
		time = lines[0]
		text = strings.Join(lines[1:], " ")
	}
	parts := []string{fmt.Sprintf("%s [%s](%s)", time, escapeTelegramMarkdownV2(text), refs[0])}
	for idx, ref := range refs[1:] {
		parts = append(parts, fmt.Sprintf("[%s](%s)", escapeTelegramMarkdownV2(fmt.Sprintf("媒体%d", idx+2)), ref))
	}
	return strings.Join(parts, " ")
}

func buildRenderedGroupChatSummaryLines(block groupChatSummaryBlock, mediaRefs map[int][]string, limit int) []renderedSummaryLine {
	lines := make([]renderedSummaryLine, 0, len(block.Lines))
	for idx, line := range block.Lines {
		lines = append(lines, splitRenderedGroupChatSummaryLine(line, mediaRefs[idx], limit)...)
	}
	return lines
}

func splitRenderedGroupChatSummaryLine(line string, refs []string, limit int) []renderedSummaryLine {
	line = strings.TrimSpace(line)
	if line == "" && len(refs) == 0 {
		return nil
	}

	visibleLength := visibleGroupChatSummaryLineLength(line, len(refs))
	if visibleLength <= limit {
		return []renderedSummaryLine{{
			Markdown:      renderGroupChatSummaryLineMarkdown(line, refs),
			VisibleLength: visibleLength,
		}}
	}

	runes := []rune(line)
	if len(runes) == 0 {
		return nil
	}

	if len(refs) == 0 {
		chunks := make([]renderedSummaryLine, 0, (len(runes)+limit-1)/limit)
		for len(runes) > 0 {
			cut := min(len(runes), limit)
			chunk := string(runes[:cut])
			runes = runes[cut:]
			chunk = strings.TrimSpace(chunk)
			if chunk == "" {
				continue
			}
			chunks = append(chunks, renderedSummaryLine{
				Markdown:      escapeTelegramMarkdownV2(chunk),
				VisibleLength: len([]rune(chunk)),
			})
		}
		return chunks
	}

	allowedTextLength := limit - visibleGroupChatSummaryExtraLabelLength(len(refs))
	if allowedTextLength <= 0 {
		allowedTextLength = 1
	}
	if len(runes) <= allowedTextLength {
		return []renderedSummaryLine{{
			Markdown:      renderGroupChatSummaryLineMarkdown(line, refs),
			VisibleLength: visibleLength,
		}}
	}

	splitAt := len(runes) - allowedTextLength
	prefix := string(runes[:splitAt])
	suffix := string(runes[splitAt:])

	result := splitRenderedGroupChatSummaryLine(prefix, nil, limit)
	suffix = strings.TrimSpace(suffix)
	if suffix != "" {
		result = append(result, renderedSummaryLine{
			Markdown:      renderGroupChatSummaryLineMarkdown(suffix, refs),
			VisibleLength: visibleGroupChatSummaryLineLength(suffix, len(refs)),
		})
	}
	return result
}

func splitRenderedSummaryDelivery(lines []renderedSummaryLine, captionLineLimit, textLineLimit int) ([]string, string) {
	if len(lines) == 0 {
		return nil, ""
	}

	captionStart := len(lines)
	total := 0
	for i := len(lines) - 1; i >= 0; i-- {
		addition := lines[i].VisibleLength
		if total > 0 {
			addition++
		}
		if total > 0 && total+addition > captionLineLimit {
			break
		}
		if total == 0 {
			total = lines[i].VisibleLength
		} else {
			total += addition
		}
		captionStart = i
	}

	textChunks := packRenderedSummaryLines(lines[:captionStart], textLineLimit)
	caption := joinRenderedSummaryLines(lines[captionStart:])
	return textChunks, caption
}

func packRenderedSummaryLines(lines []renderedSummaryLine, limit int) []string {
	if len(lines) == 0 {
		return nil
	}

	chunks := make([]string, 0, 1)
	start := 0
	currentLength := 0
	for idx, line := range lines {
		addition := line.VisibleLength
		if currentLength > 0 {
			addition++
		}
		if currentLength > 0 && currentLength+addition > limit {
			chunks = append(chunks, joinRenderedSummaryLines(lines[start:idx]))
			start = idx
			currentLength = line.VisibleLength
			continue
		}
		if currentLength == 0 {
			currentLength = line.VisibleLength
		} else {
			currentLength += addition
		}
	}
	chunks = append(chunks, joinRenderedSummaryLines(lines[start:]))
	return chunks
}

func joinRenderedSummaryLines(lines []renderedSummaryLine) string {
	if len(lines) == 0 {
		return ""
	}
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line.Markdown) == "" {
			continue
		}
		parts = append(parts, line.Markdown)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func visibleGroupChatSummaryLineLength(line string, mediaCount int) int {
	length := len([]rune(strings.TrimSpace(line)))
	return length + visibleGroupChatSummaryExtraLabelLength(mediaCount)
}

func visibleGroupChatSummaryExtraLabelLength(mediaCount int) int {
	if mediaCount <= 1 {
		return 0
	}
	total := 0
	for i := 2; i <= mediaCount; i++ {
		total += 1 + len([]rune(fmt.Sprintf("媒体%d", i)))
	}
	return total
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

func (c *Client) sendMarkdownText(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	values := url.Values{}
	values.Set("chat_id", c.cfg.ChatID)
	values.Set("text", text)
	values.Set("parse_mode", "MarkdownV2")
	values.Set("disable_web_page_preview", "true")
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
		return fmt.Errorf("发送 Telegram Markdown 文本消息失败: %w", err)
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
	return c.sendSingleMediaWithMessageIDAs(ctx, item, caption, item.Type)
}

// sendSingleMediaWithMessageIDAs 使用指定的 mediaType 发送（用于降级为 document）。
func (c *Client) sendSingleMediaWithMessageIDAs(ctx context.Context, item mediaItem, caption string, mediaType string) (int, error) {
	file, err := os.Open(item.Path)
	if err != nil {
		return 0, fmt.Errorf("打开媒体文件失败: %w", err)
	}
	defer file.Close()

	method := singleMediaMethod(mediaType)
	field := mediaType // sendPhoto -> field="photo", sendDocument -> field="document"

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
		if mediaType == "photo" || mediaType == "video" {
			if err := writer.WriteField("show_caption_above_media", "true"); err != nil {
				return 0, err
			}
		}
	}

	part, err := writer.CreateFormFile(field, filepath.Base(item.Path))
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
		// 图片/视频尺寸或格式不合法时，自动降级为 sendDocument 重试
		if mediaType != "document" && isTelegramMediaInvalidError(err) {
			c.logger.Warn("媒体格式被 Telegram 拒绝，降级为 sendDocument 重试",
				"method", method,
				"file", filepath.Base(item.Path),
				"reason", err.Error(),
			)
			docItem := mediaItem{Path: item.Path, Type: "document", Field: "document"}
			return c.sendSingleMediaWithMessageIDAs(ctx, docItem, caption, "document")
		}
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

	_, groupErr := parseAPIResponse("sendMediaGroup", resp)
	if groupErr != nil {
		// 媒体组中有不合法的图片/视频时，整组回退为逐个 sendDocument
		if isTelegramMediaInvalidError(groupErr) {
			c.logger.Warn("sendMediaGroup 被 Telegram 拒绝，回退为逐个 sendDocument 发送",
				"count", len(items),
				"reason", groupErr.Error(),
			)
			captionUsed := false
			for _, item := range items {
				itemCaption := ""
				if !captionUsed {
					itemCaption = caption
					captionUsed = true
				}
				docItem := mediaItem{Path: item.Path, Type: "document", Field: "document"}
				if _, err := c.sendSingleMediaWithMessageIDAs(ctx, docItem, itemCaption, "document"); err != nil {
					return err
				}
			}
			return nil
		}
		return groupErr
	}
	return nil
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

func (c *Client) editMessageCaptionMarkdown(ctx context.Context, messageID int, caption string) error {
	values := url.Values{}
	values.Set("chat_id", c.cfg.ChatID)
	values.Set("message_id", strconv.Itoa(messageID))
	values.Set("caption", strings.TrimSpace(caption))
	values.Set("parse_mode", "MarkdownV2")
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
		return fmt.Errorf("编辑 Telegram Markdown caption 失败: %w", err)
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

func escapeTelegramMarkdownV2(text string) string {
	var builder strings.Builder
	for _, r := range text {
		switch r {
		case '_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!', '\\':
			builder.WriteByte('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
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

// isTelegramMediaInvalidError 判断 Telegram 返回的错误是否属于媒体格式/尺寸不合法，
// 与 Python 版本中的降级触发条件保持一致。
func isTelegramMediaInvalidError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, kw := range []string{
		"PHOTO_INVALID_DIMENSIONS",
		"PHOTO_SAVE_FILE_INVALID",
		"VIDEO_FILE_INVALID",
		"failed to get HTTP URL content",
		"Wrong type of the web page content",
		"IMAGE_PROCESS_FAILED",
	} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}
