package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	neturl "net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	htmlBRPattern    = regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlTagPattern   = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceLinePattern = regexp.MustCompile(`\n{3,}`)
	fileNamePattern  = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
)

type WeiboScraper struct {
	cookies *CookieProvider
}

func NewWeiboScraper() *WeiboScraper {
	return &WeiboScraper{
		cookies: NewCookieProvider(),
	}
}

func (s *WeiboScraper) Close() error {
	if s.cookies == nil {
		return nil
	}
	return s.cookies.Close()
}

func (s *WeiboScraper) Scrape(ctx context.Context, cfg Config, sourceURL string) (*Record, error) {
	resolvedURL, err := s.resolveURL(ctx, cfg, sourceURL)
	if err != nil {
		return nil, err
	}

	weiboID, err := extractWeiboID(resolvedURL)
	if err != nil {
		return nil, err
	}

	status, err := s.fetchStatus(ctx, cfg, weiboID)
	if err != nil {
		return nil, err
	}

	mainText := firstNonEmpty(status.TextRaw, cleanHTML(status.Text))
	if status.IsLongText {
		longTextID := firstNonEmpty(status.MblogID, status.IDStr, weiboID)
		if longText, err := s.fetchLongText(ctx, cfg, longTextID); err == nil && longText != "" {
			mainText = longText
		} else if err != nil {
			log.Printf("获取微博长文本失败: %v", err)
		}
	}

	finalText := mainText
	mediaURLs := extractMedia(status)
	isRetweet := status.RetweetedStatus != nil

	if status.RetweetedStatus != nil {
		retweetText := firstNonEmpty(status.RetweetedStatus.TextRaw, cleanHTML(status.RetweetedStatus.Text))
		if status.RetweetedStatus.IsLongText {
			retweetID := firstNonEmpty(status.RetweetedStatus.MblogID, status.RetweetedStatus.IDStr)
			if retweetLongText, err := s.fetchLongText(ctx, cfg, retweetID); err == nil && retweetLongText != "" {
				retweetText = retweetLongText
			} else if err != nil {
				log.Printf("获取转发长文本失败: %v", err)
			}
		}
		retweetAuthor := firstNonEmpty(status.RetweetedStatus.User.ScreenName, "Unknown")
		if finalText != "" {
			finalText += "\n\n"
		}
		finalText += fmt.Sprintf("[转发自 @%s]:\n%s", retweetAuthor, strings.TrimSpace(retweetText))
		mediaURLs = append(mediaURLs, extractMedia(status.RetweetedStatus)...)
	}

	mediaURLs = uniqueStrings(mediaURLs)

	record := &Record{
		ID:        firstNonEmpty(status.IDStr, fmt.Sprintf("%d", status.ID), weiboID),
		CreatedAt: status.CreatedAt,
		Text:      strings.TrimSpace(finalText),
		MediaURLs: mediaURLs,
		IsRetweet: isRetweet,
		SourceURL: resolvedURL,
	}

	if cfg.Weibo.DownloadMedia && len(record.MediaURLs) > 0 {
		record.LocalMediaPaths, record.FailedMediaURLs = s.downloadAllMedia(ctx, cfg, record.ID, record.MediaURLs)
	}

	if cfg.Weibo.SaveRecord {
		if err := saveRecord(cfg.Weibo.SaveRecordDir, record); err != nil {
			log.Printf("保存微博记录失败: %v", err)
		}
	}

	return record, nil
}

func (s *WeiboScraper) resolveURL(ctx context.Context, cfg Config, rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("微博链接不能为空")
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}

	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("无效的微博链接: %w", err)
	}

	host := strings.ToLower(parsed.Hostname())
	if host != "t.cn" {
		return parsed.String(), nil
	}

	client := &http.Client{
		Timeout: time.Duration(cfg.Weibo.RequestTimeoutSeconds) * time.Second,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("解析短链失败: %w", err)
	}
	defer resp.Body.Close()
	return resp.Request.URL.String(), nil
}

func extractWeiboID(rawURL string) (string, error) {
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("无效链接: %w", err)
	}
	if mid := parsed.Query().Get("mid"); mid != "" {
		return mid, nil
	}

	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	if len(filtered) == 0 {
		return "", fmt.Errorf("无法从链接中提取微博 ID")
	}

	last := filtered[len(filtered)-1]
	switch last {
	case "status", "detail":
		return "", fmt.Errorf("无法从链接中提取微博 ID")
	}
	return last, nil
}

func (s *WeiboScraper) fetchStatus(ctx context.Context, cfg Config, weiboID string) (*WeiboStatus, error) {
	apiURL := fmt.Sprintf("https://weibo.com/ajax/statuses/show?id=%s", neturl.QueryEscape(weiboID))
	body, err := s.doWeiboGet(ctx, cfg, apiURL)
	if err != nil {
		return nil, err
	}

	var apiErr WeiboErrorResponse
	_ = json.Unmarshal(body, &apiErr)
	if apiErr.ErrorType != "" || apiErr.ErrorCode != 0 {
		return nil, fmt.Errorf("微博 API 返回错误: %s (%d)", firstNonEmpty(apiErr.Message, apiErr.ErrorType), apiErr.ErrorCode)
	}

	var status WeiboStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("解析微博内容失败: %w", err)
	}
	if status.ID == 0 && status.IDStr == "" && status.MblogID == "" {
		return nil, fmt.Errorf("微博内容为空或结构不符合预期")
	}
	return &status, nil
}

func (s *WeiboScraper) fetchLongText(ctx context.Context, cfg Config, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	apiURL := fmt.Sprintf("https://weibo.com/ajax/statuses/longtext?id=%s", neturl.QueryEscape(id))
	body, err := s.doWeiboGet(ctx, cfg, apiURL)
	if err != nil {
		return "", err
	}
	var resp WeiboLongTextResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("解析长文本失败: %w", err)
	}
	return cleanHTML(resp.Data.LongTextContent), nil
}

func (s *WeiboScraper) doWeiboGet(ctx context.Context, cfg Config, requestURL string) ([]byte, error) {
	cookie, err := s.cookies.Header(ctx, cfg)
	if err != nil {
		return nil, err
	}

	client := &http.Client{
		Timeout: time.Duration(cfg.Weibo.RequestTimeoutSeconds) * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", cfg.Weibo.UserAgent)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Referer", cfg.Weibo.Referer)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json, text/plain, */*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求微博接口失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取微博接口响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("微博接口返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func extractMedia(status *WeiboStatus) []string {
	if status == nil {
		return nil
	}

	var urls []string
	if status.PageInfo != nil && string(status.PageInfo.Type) == "video" && status.PageInfo.MediaInfo != nil {
		videoURL := firstNonEmpty(status.PageInfo.MediaInfo.MP4720p, status.PageInfo.MediaInfo.MP4SD, status.PageInfo.MediaInfo.Stream)
		if videoURL != "" {
			urls = append(urls, videoURL)
		}
	}

	for _, pic := range status.PicInfos {
		switch {
		case pic.MW2000 != nil && pic.MW2000.URL != "":
			urls = append(urls, pic.MW2000.URL)
		case pic.Original != nil && pic.Original.URL != "":
			urls = append(urls, pic.Original.URL)
		case pic.Large != nil && pic.Large.URL != "":
			urls = append(urls, pic.Large.URL)
		}
	}

	if len(status.PicInfos) == 0 {
		for _, id := range status.PicIDs {
			id = strings.TrimSpace(id)
			if id != "" {
				urls = append(urls, "https://wx1.sinaimg.cn/mw2000/"+id+".jpg")
			}
		}
	}
	return urls
}

func (s *WeiboScraper) downloadAllMedia(ctx context.Context, cfg Config, recordID string, urls []string) ([]string, []string) {
	if len(urls) == 0 {
		return nil, nil
	}
	dir := filepath.Join(cfg.Weibo.DownloadDir, recordID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("创建媒体目录失败: %v", err)
		return nil, urls
	}

	var paths []string
	var failed []string
	for _, mediaURL := range urls {
		localPath, err := s.downloadMedia(ctx, cfg, mediaURL, dir)
		if err != nil {
			log.Printf("下载媒体失败 %s: %v", mediaURL, err)
			failed = append(failed, mediaURL)
			continue
		}
		paths = append(paths, localPath)
	}
	return paths, failed
}

func (s *WeiboScraper) downloadMedia(ctx context.Context, cfg Config, mediaURL, dir string) (string, error) {
	parsed, err := neturl.Parse(mediaURL)
	if err != nil {
		return "", fmt.Errorf("无效媒体链接: %w", err)
	}
	fileName := sanitizeFileName(path.Base(parsed.Path))
	if fileName == "" || fileName == "." || fileName == "/" {
		fileName = fmt.Sprintf("media_%d.bin", time.Now().UnixNano())
	}
	targetPath := filepath.Join(dir, fileName)
	if _, err := os.Stat(targetPath); err == nil {
		return targetPath, nil
	}

	client := &http.Client{
		Timeout: time.Duration(cfg.Weibo.MediaDownloadTimeoutSeconds) * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", cfg.Weibo.UserAgent)
	req.Header.Set("Referer", cfg.Weibo.Referer)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	tempPath := targetPath + ".part"
	file, err := os.Create(tempPath)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(file, resp.Body); err != nil {
		file.Close()
		_ = os.Remove(tempPath)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	if err := os.Rename(tempPath, targetPath); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	return targetPath, nil
}

func saveRecord(dir string, record *Record) error {
	if record == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "weibo_"+record.ID+".json")
	return os.WriteFile(path, data, 0o644)
}

func cleanHTML(text string) string {
	text = htmlBRPattern.ReplaceAllString(text, "\n")
	text = strings.ReplaceAll(text, "</p>", "\n")
	text = strings.ReplaceAll(text, "<p>", "")
	text = html.UnescapeString(text)
	text = strings.ReplaceAll(text, "\u00a0", " ")
	text = htmlTagPattern.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = spaceLinePattern.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

func sanitizeFileName(name string) string {
	name = fileNamePattern.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._")
	return name
}

func uniqueStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
