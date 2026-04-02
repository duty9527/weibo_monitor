package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	textLimit       = 4096
	captionLimit    = 1024
	mediaGroupLimit = 10
)

var (
	photoExts = map[string]struct{}{
		".jpg": {}, ".jpeg": {}, ".png": {}, ".webp": {},
	}
	videoExts = map[string]struct{}{
		".mp4": {}, ".mov": {}, ".m4v": {},
	}
)

type TelegramClient struct{}

func NewTelegramClient() *TelegramClient {
	return &TelegramClient{}
}

func (c *TelegramClient) RegisterDefaults(ctx context.Context, cfg Config) error {
	commands := []TelegramBotCommand{
		{Command: "scrape", Description: "抓取微博链接"},
		{Command: "help", Description: "显示帮助"},
	}

	var resp TelegramAPIResponse[bool]
	if err := c.callJSON(ctx, telegramHTTPClient(30), cfg, "setMyCommands", map[string]any{
		"commands": commands,
	}, &resp); err != nil {
		return err
	}

	if err := c.callJSON(ctx, telegramHTTPClient(30), cfg, "setChatMenuButton", map[string]any{
		"menu_button": TelegramMenuButton{Type: "commands"},
	}, &resp); err != nil {
		return err
	}

	return nil
}

func (c *TelegramClient) GetUpdates(ctx context.Context, cfg Config, offset int64) ([]TelegramUpdate, error) {
	timeout := cfg.Telegram.PollTimeoutSeconds
	client := &http.Client{
		Timeout: time.Duration(timeout+10) * time.Second,
	}
	payload := map[string]any{
		"offset":          offset,
		"timeout":         timeout,
		"allowed_updates": []string{"message"},
	}

	var resp TelegramAPIResponse[[]TelegramUpdate]
	if err := c.callJSON(ctx, client, cfg, "getUpdates", payload, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func (c *TelegramClient) SendText(ctx context.Context, cfg Config, target Target, text string, enablePreview bool) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	for _, chunk := range splitText(text, textLimit) {
		payload := map[string]any{
			"chat_id": target.ChatID,
			"text":    chunk,
		}
		if target.ThreadID > 0 {
			payload["message_thread_id"] = target.ThreadID
		}
		if !enablePreview {
			payload["disable_web_page_preview"] = true
		}

		var resp TelegramAPIResponse[telegramRawResult]
		if err := c.callJSON(ctx, telegramHTTPClient(30), cfg, "sendMessage", payload, &resp); err != nil {
			return err
		}
	}
	return nil
}

func (c *TelegramClient) SendRecord(ctx context.Context, cfg Config, target Target, record *Record, sourceURL string) error {
	if record == nil {
		return fmt.Errorf("空微博记录")
	}

	body := formatRecordMessage(record, sourceURL)
	localPaths := filterExistingPaths(record.LocalMediaPaths)
	items := make([]mediaItem, 0, len(localPaths))
	for _, p := range localPaths {
		items = append(items, mediaItem{Path: p, Type: detectMediaKind(p)})
	}

	failedURLs := append([]string(nil), record.FailedMediaURLs...)

	if len(items) == 0 {
		if err := c.SendText(ctx, cfg, target, body, sourceURL != ""); err != nil {
			return err
		}
		if len(failedURLs) > 0 {
			return c.SendText(ctx, cfg, target, "以下媒体未成功下载，原始链接如下：\n"+strings.Join(failedURLs, "\n"), false)
		}
		return nil
	}

	if len(body) <= captionLimit {
		if err := c.sendMediaSet(ctx, cfg, target, items, body); err != nil {
			return err
		}
	} else {
		log.Printf("正文超过 Telegram caption 限制，拆分为文本消息 + 媒体消息")
		if err := c.SendText(ctx, cfg, target, body, false); err != nil {
			return err
		}
		if err := c.sendMediaSet(ctx, cfg, target, items, ""); err != nil {
			return err
		}
	}

	if len(failedURLs) > 0 {
		return c.SendText(ctx, cfg, target, "部分媒体未成功下载，原始链接如下：\n"+strings.Join(failedURLs, "\n"), false)
	}
	return nil
}

func (c *TelegramClient) callJSON(ctx context.Context, client *http.Client, cfg Config, method string, payload any, out any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, telegramAPIURL(cfg, method), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s 请求失败: %w", method, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s 读取响应失败: %w", method, err)
	}

	var apiResp TelegramAPIResponse[telegramRawResult]
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return fmt.Errorf("%s 解析响应失败: %w", method, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s 失败 (HTTP %d): %s", method, resp.StatusCode, firstNonEmpty(apiResp.Description, strings.TrimSpace(string(body))))
	}
	if !apiResp.OK {
		return fmt.Errorf("%s 失败: %s", method, firstNonEmpty(apiResp.Description, strings.TrimSpace(string(body))))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("%s 解析响应失败: %w", method, err)
		}
	}
	return nil
}

func (c *TelegramClient) sendMediaSet(ctx context.Context, cfg Config, target Target, items []mediaItem, caption string) error {
	if len(items) == 0 {
		return nil
	}
	if len(items) == 1 {
		return c.sendSingleMedia(ctx, cfg, target, items[0], caption)
	}
	if allGroupable(items) {
		captionUsed := false
		for start := 0; start < len(items); start += mediaGroupLimit {
			end := start + mediaGroupLimit
			if end > len(items) {
				end = len(items)
			}
			groupCaption := ""
			if !captionUsed {
				groupCaption = caption
				captionUsed = true
			}
			if err := c.sendMediaGroup(ctx, cfg, target, items[start:end], groupCaption); err != nil {
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
		if err := c.sendSingleMedia(ctx, cfg, target, item, itemCaption); err != nil {
			return err
		}
	}
	return nil
}

func (c *TelegramClient) sendSingleMedia(ctx context.Context, cfg Config, target Target, item mediaItem, caption string) error {
	method := map[string]string{
		"photo":    "sendPhoto",
		"video":    "sendVideo",
		"document": "sendDocument",
	}[item.Type]
	if method == "" {
		method = "sendDocument"
	}

	fields := basicTargetFields(target)
	if caption = strings.TrimSpace(caption); caption != "" {
		fields["caption"] = caption
	}

	err := c.callMultipart(ctx, telegramHTTPClient(60), cfg, method, fields, map[string]string{item.Type: item.Path})
	if err != nil && item.Type != "document" && shouldFallbackToDocument(err) {
		log.Printf("%s 被拒绝，降级为 sendDocument 重试: %s", method, filepath.Base(item.Path))
		return c.callMultipart(ctx, telegramHTTPClient(60), cfg, "sendDocument", fields, map[string]string{"document": item.Path})
	}
	return err
}

func (c *TelegramClient) sendMediaGroup(ctx context.Context, cfg Config, target Target, items []mediaItem, caption string) error {
	fields := basicTargetFields(target)
	media := make([]map[string]string, 0, len(items))
	files := make(map[string]string, len(items))
	for i, item := range items {
		name := "file" + strconv.Itoa(i)
		entry := map[string]string{
			"type":  item.Type,
			"media": "attach://" + name,
		}
		if i == 0 && strings.TrimSpace(caption) != "" {
			entry["caption"] = caption
		}
		media = append(media, entry)
		files[name] = item.Path
	}
	mediaJSON, err := json.Marshal(media)
	if err != nil {
		return err
	}
	fields["media"] = string(mediaJSON)
	return c.callMultipart(ctx, telegramHTTPClient(60), cfg, "sendMediaGroup", fields, files)
}

func (c *TelegramClient) callMultipart(ctx context.Context, client *http.Client, cfg Config, method string, fields map[string]string, files map[string]string) error {
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	writeErrCh := make(chan error, 1)

	go func() {
		defer close(writeErrCh)
		defer pw.Close()
		for key, value := range fields {
			if err := writer.WriteField(key, value); err != nil {
				writeErrCh <- err
				_ = writer.Close()
				return
			}
		}
		for field, path := range files {
			file, err := os.Open(path)
			if err != nil {
				writeErrCh <- err
				_ = writer.Close()
				return
			}
			part, err := writer.CreateFormFile(field, filepath.Base(path))
			if err != nil {
				file.Close()
				writeErrCh <- err
				_ = writer.Close()
				return
			}
			if _, err := io.Copy(part, file); err != nil {
				file.Close()
				writeErrCh <- err
				_ = writer.Close()
				return
			}
			file.Close()
		}
		writeErrCh <- writer.Close()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, telegramAPIURL(cfg, method), pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s 请求失败: %w", method, err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return fmt.Errorf("%s 读取响应失败: %w", method, readErr)
	}
	if writeErr := <-writeErrCh; writeErr != nil {
		return fmt.Errorf("%s 构造请求失败: %w", method, writeErr)
	}

	var apiResp TelegramAPIResponse[telegramRawResult]
	if err := json.Unmarshal(body, &apiResp); err == nil {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 && apiResp.OK {
			return nil
		}
		return fmt.Errorf("%s 失败 (HTTP %d): %s", method, resp.StatusCode, firstNonEmpty(apiResp.Description, strings.TrimSpace(string(body))))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s 失败 (HTTP %d): %s", method, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func telegramAPIURL(cfg Config, method string) string {
	return strings.TrimRight(cfg.Telegram.APIBase, "/") + "/bot" + cfg.Telegram.BotToken + "/" + method
}

func telegramHTTPClient(timeoutSeconds int) *http.Client {
	return &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second}
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
		chunk := strings.TrimSpace(string(runes[:cut]))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		runes = runes[cut:]
	}
	if tail := strings.TrimSpace(string(runes)); tail != "" {
		chunks = append(chunks, tail)
	}
	return chunks
}

func formatRecordMessage(record *Record, sourceURL string) string {
	var lines []string
	tag := "#原创"
	if record != nil && record.IsRetweet {
		tag = "#转发"
	}
	timeStr := formatDisplayTime(record.CreatedAt)
	header := strings.TrimSpace(strings.TrimSpace(tag + " " + timeStr))
	if header != "" {
		lines = append(lines, header)
	}
	if text := strings.TrimSpace(record.Text); text != "" {
		lines = append(lines, "", text)
	}
	if sourceURL != "" {
		lines = append(lines, "", sourceURL)
	}
	if len(lines) == 0 {
		return "微博更新"
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func formatDisplayTime(createdAt string) string {
	if strings.TrimSpace(createdAt) == "" {
		return ""
	}
	layouts := []string{
		"Mon Jan 02 15:04:05 -0700 2006",
		"2006-01-02T15:04:05-0700",
		time.RFC3339,
	}
	for _, layout := range layouts {
		t, err := time.Parse(layout, createdAt)
		if err == nil {
			return "【" + t.Local().Format("06-01-02 15:04:05") + "】"
		}
	}
	return "【" + createdAt + "】"
}

type mediaItem struct {
	Path string
	Type string
}

func detectMediaKind(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if _, ok := photoExts[ext]; ok {
		return "photo"
	}
	if _, ok := videoExts[ext]; ok {
		return "video"
	}
	return "document"
}

func allGroupable(items []mediaItem) bool {
	for _, item := range items {
		if item.Type != "photo" && item.Type != "video" {
			return false
		}
	}
	return true
}

func filterExistingPaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func basicTargetFields(target Target) map[string]string {
	fields := map[string]string{
		"chat_id": strconv.FormatInt(target.ChatID, 10),
	}
	if target.ThreadID > 0 {
		fields["message_thread_id"] = strconv.FormatInt(target.ThreadID, 10)
	}
	return fields
}

func shouldFallbackToDocument(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "PHOTO_INVALID_DIMENSIONS") ||
		strings.Contains(msg, "PHOTO_SAVE_FILE_INVALID") ||
		strings.Contains(msg, "VIDEO_FILE_INVALID") ||
		strings.Contains(msg, "failed to get HTTP URL content")
}
