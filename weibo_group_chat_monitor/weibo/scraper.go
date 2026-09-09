package weibo

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"weibo_group_chat_monitor/config"
)

// Scraper 微博抓取核心逻辑。
type Scraper struct {
	cfg                *config.WeiboModeConfig
	httpClient         *http.Client
	sessionHeaders     map[string]string
	logger             *slog.Logger
	sinceTime          time.Time
	hasSinceTime       bool
	latestFetchedAt    time.Time
	hasLatestFetchedAt bool
}

// NewScraper 创建抓取器并解析时间节点。
func NewScraper(cfg *config.WeiboModeConfig, logger *slog.Logger) (*Scraper, error) {
	s := &Scraper{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		sessionHeaders: map[string]string{},
		logger:         logger,
	}

	if strings.TrimSpace(cfg.Weibo.SinceTime) != "" {
		sinceTime, err := ParseConfigTime(cfg.Weibo.SinceTime)
		if err != nil {
			return nil, fmt.Errorf("解析 weibo.since_time 失败: %w", err)
		}
		s.sinceTime = sinceTime
		s.hasSinceTime = true
	}

	return s, nil
}

// SetCookies 注入 Cookie 并构造请求头。
func (s *Scraper) SetCookies(cookieStr string) {
	s.sessionHeaders = buildSessionHeaders(cookieStr, s.cfg.Weibo.TargetUID)
}

func (s *Scraper) LatestFetchedTime() (time.Time, bool) {
	if !s.hasLatestFetchedAt {
		return time.Time{}, false
	}
	return s.latestFetchedAt, true
}

func buildSessionHeaders(cookieStr, targetUID string) map[string]string {
	headers := map[string]string{
		"User-Agent":       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36",
		"Cookie":           cookieStr,
		"Referer":          fmt.Sprintf("https://weibo.com/u/%s", targetUID),
		"x-requested-with": "XMLHttpRequest",
		"accept":           "application/json, text/plain, */*",
	}

	if xsrfToken := extractCookieValue(cookieStr, "XSRF-TOKEN"); xsrfToken != "" {
		headers["x-xsrf-token"] = xsrfToken
	}

	return headers
}

func extractCookieValue(cookieHeader, name string) string {
	prefix := name + "="
	for _, part := range strings.Split(cookieHeader, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, prefix) {
			return strings.TrimPrefix(part, prefix)
		}
	}
	return ""
}

// GetSeenIDs 从历史 JSONL 文件中读取已记录的微博 ID。
func (s *Scraper) GetSeenIDs() (map[string]bool, error) {
	seen := make(map[string]bool)
	f, err := os.Open(s.cfg.Weibo.HistoryFile)
	if os.IsNotExist(err) {
		return seen, nil
	}
	if err != nil {
		return nil, fmt.Errorf("打开历史文件失败: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var rec WeiboRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			s.logger.Warn("解析历史记录失败，已跳过", "err", err)
			continue
		}
		if rec.ID != "" {
			seen[rec.ID] = true
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取历史文件失败: %w", err)
	}

	return seen, nil
}

// AppendRecord 将已成功处理的微博记录追加到历史文件。
func (s *Scraper) AppendRecord(record *WeiboRecord) error {
	if record == nil {
		return nil
	}

	historyDir := filepath.Dir(s.cfg.Weibo.HistoryFile)
	if historyDir != "." {
		if err := os.MkdirAll(historyDir, 0o755); err != nil {
			return fmt.Errorf("创建历史目录失败: %w", err)
		}
	}

	f, err := os.OpenFile(s.cfg.Weibo.HistoryFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("打开历史文件失败: %w", err)
	}
	defer f.Close()

	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("序列化历史记录失败: %w", err)
	}

	if _, err := fmt.Fprintf(f, "%s\n", line); err != nil {
		return fmt.Errorf("写入历史文件失败: %w", err)
	}

	return nil
}

func (s *Scraper) doGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range s.sessionHeaders {
		req.Header.Set(k, v)
	}
	return s.httpClient.Do(req)
}

func (s *Scraper) fetchPage(ctx context.Context, page int) ([]WeiboItem, error) {
	url := fmt.Sprintf(
		"https://weibo.com/ajax/statuses/mymblog?uid=%s&page=%d&feature=0",
		s.cfg.Weibo.TargetUID, page,
	)

	resp, err := s.doGet(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}
	return apiResp.Data.List, nil
}

func (s *Scraper) getFullText(ctx context.Context, mblogID string) string {
	url := fmt.Sprintf("https://weibo.com/ajax/statuses/longtext?id=%s", mblogID)
	resp, err := s.doGet(ctx, url)
	if err != nil {
		s.logger.Warn("获取长文失败", "mblogid", mblogID, "err", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.logger.Warn("获取长文失败", "mblogid", mblogID, "status", resp.StatusCode)
		return ""
	}

	var result LongTextResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		s.logger.Warn("解析长文响应失败", "mblogid", mblogID, "err", err)
		return ""
	}
	return result.Data.LongTextContent
}

func (s *Scraper) extractText(ctx context.Context, item *WeiboItem) string {
	text := item.TextRaw
	if item.IsLongText && item.MblogID != "" {
		if full := s.getFullText(ctx, item.MblogID); full != "" {
			s.logger.Info("获取到长文全文", "mblogid", item.MblogID)
			text = full
			time.Sleep(500 * time.Millisecond)
		}
	}
	return text
}

func extractMedia(item *WeiboItem) []string {
	if item == nil {
		return nil
	}

	var urls []string

	// 微博的视频 page_info.type 既可能是 "video"，也常见数字 5、11。
	// media_info 的存在比 type 更稳定，因此不再依赖 type 判断。
	if item.PageInfo != nil {
		urls = append(urls, bestVideoURL(item.PageInfo.MediaInfo))
	}

	for _, pic := range item.PicInfos {
		switch {
		case pic.Mw2000 != nil && pic.Mw2000.URL != "":
			urls = append(urls, pic.Mw2000.URL)
		case pic.Original != nil && pic.Original.URL != "":
			urls = append(urls, pic.Original.URL)
		case pic.Large != nil && pic.Large.URL != "":
			urls = append(urls, pic.Large.URL)
		}
	}

	if len(item.PicInfos) == 0 {
		for _, pid := range item.PicIDs {
			urls = append(urls, fmt.Sprintf("https://wx1.sinaimg.cn/mw2000/%s.jpg", pid))
		}
	}

	urls = append(urls, extractMixedMedia(item.MixMediaInfo)...)

	return uniqueStrings(urls)
}

func bestVideoURL(mi *MediaInfo) string {
	if mi == nil {
		return ""
	}
	for _, candidate := range []string{
		mi.MP4720P,
		mi.MP4HDURL,
		mi.StreamURLHD,
		mi.MP4SdURL,
		mi.StreamURL,
		mi.HEVCMP4720P,
		mi.H265MP4HD,
		mi.H265MP4LD,
	} {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	return ""
}

// extractMixedMedia 兼容 mix_media_info.items 中图文、视频混排的结构。
// 该接口字段变化较频繁，因此仅在 mix_media_info 子树内递归识别明确的
// 图片尺寸字段和视频地址字段，避免把头像、卡片链接误判成正文媒体。
func extractMixedMedia(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}

	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}

	var urls []string
	var walk func(any)
	walk = func(node any) {
		switch v := node.(type) {
		case []any:
			for _, child := range v {
				walk(child)
			}
		case map[string]any:
			if videoURL := bestVideoURLFromContainer(v); videoURL != "" {
				urls = append(urls, videoURL)
				return
			}

			for _, size := range []string{"mw2000", "original", "large"} {
				if imageURL := nestedString(v, size, "url"); imageURL != "" {
					urls = append(urls, imageURL)
					return
				}
			}

			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(value)
	return uniqueStrings(urls)
}

func bestVideoURLFromContainer(value map[string]any) string {
	if videoURL := bestVideoURLFromMap(value); videoURL != "" {
		return videoURL
	}
	if mediaInfo, ok := value["media_info"].(map[string]any); ok {
		if videoURL := bestVideoURLFromMap(mediaInfo); videoURL != "" {
			return videoURL
		}
	}
	if pageInfo, ok := value["page_info"].(map[string]any); ok {
		if mediaInfo, ok := pageInfo["media_info"].(map[string]any); ok {
			return bestVideoURLFromMap(mediaInfo)
		}
	}
	return ""
}

func bestVideoURLFromMap(value map[string]any) string {
	for _, key := range []string{
		"mp4_720p_mp4",
		"mp4_hd_url",
		"stream_url_hd",
		"mp4_sd_url",
		"stream_url",
		"hevc_mp4_720p",
		"h265_mp4_hd",
		"h265_mp4_ld",
	} {
		if candidate, ok := value[key].(string); ok && strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	return ""
}

func nestedString(value map[string]any, objectKey, valueKey string) string {
	nested, ok := value[objectKey].(map[string]any)
	if !ok {
		return ""
	}
	result, _ := nested[valueKey].(string)
	return strings.TrimSpace(result)
}

func idStr(id interface{}) string {
	if id == nil {
		return ""
	}
	switch v := id.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func buildWeiboURL(targetUID string, item *WeiboItem) string {
	if item == nil {
		return ""
	}
	if strings.TrimSpace(item.MblogID) != "" && strings.TrimSpace(targetUID) != "" {
		return fmt.Sprintf("https://weibo.com/%s/%s", targetUID, item.MblogID)
	}
	if wid := idStr(item.ID); wid != "" {
		return fmt.Sprintf("https://weibo.com/detail/%s", wid)
	}
	return ""
}

// DownloadMedia 下载单个媒体文件，返回本地路径。
func (s *Scraper) DownloadMedia(url string) string {
	if url == "" {
		return ""
	}
	if err := os.MkdirAll(s.cfg.Weibo.MediaDir, 0o755); err != nil {
		s.logger.Error("创建媒体目录失败", "err", err)
		return ""
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		s.logger.Error("下载失败", "url", url, "err", err)
		return ""
	}
	req.Header.Set("User-Agent", s.sessionHeaders["User-Agent"])
	req.Header.Set("Referer", "https://weibo.com/")
	if cookieStr := s.sessionHeaders["Cookie"]; cookieStr != "" {
		req.Header.Set("Cookie", cookieStr)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.logger.Error("下载失败", "url", url, "err", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.logger.Error("下载失败", "url", url, "status", resp.StatusCode)
		return ""
	}

	filename := mediaFilename(url, resp.Header.Get("Content-Type"))
	fpath := filepath.Join(s.cfg.Weibo.MediaDir, filename)
	if _, err := os.Stat(fpath); err == nil {
		return fpath
	}

	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "text/html") || strings.HasPrefix(contentType, "application/json") {
		s.logger.Error("下载失败，响应不是媒体文件", "url", url, "content_type", contentType)
		return ""
	}

	out, err := os.CreateTemp(s.cfg.Weibo.MediaDir, ".media-*")
	if err != nil {
		s.logger.Error("创建临时媒体文件失败", "path", fpath, "err", err)
		return ""
	}
	tempPath := out.Name()
	keepTemp := false
	defer func() {
		out.Close()
		if !keepTemp {
			_ = os.Remove(tempPath)
		}
	}()

	if _, err := io.Copy(out, resp.Body); err != nil {
		s.logger.Error("写入文件失败", "path", fpath, "err", err)
		return ""
	}
	if err := out.Close(); err != nil {
		s.logger.Error("关闭媒体文件失败", "path", fpath, "err", err)
		return ""
	}
	if err := os.Rename(tempPath, fpath); err != nil {
		s.logger.Error("保存媒体文件失败", "path", fpath, "err", err)
		return ""
	}
	keepTemp = true
	return fpath
}

func mediaFilename(rawURL, contentType string) string {
	filename := ""
	if parsed, err := url.Parse(rawURL); err == nil {
		filename = path.Base(parsed.Path)
		if decoded, decodeErr := url.PathUnescape(filename); decodeErr == nil {
			filename = decoded
		}
	}
	if filename == "" || filename == "." || filename == "/" {
		filename = fmt.Sprintf("media_%d", time.Now().UnixMilli())
	}
	filename = filepath.Base(filename)

	extension := strings.ToLower(filepath.Ext(filename))
	if isKnownMediaExtension(extension) {
		return filename
	}

	if extensions, err := mime.ExtensionsByType(strings.TrimSpace(strings.Split(contentType, ";")[0])); err == nil && len(extensions) > 0 {
		filename = strings.TrimSuffix(filename, filepath.Ext(filename)) + preferredMediaExtension(extensions)
	} else if extension == "" {
		filename += ".file"
	}
	return filename
}

func isKnownMediaExtension(extension string) bool {
	switch extension {
	case ".mp4", ".mov", ".m4v", ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return true
	default:
		return false
	}
}

func preferredMediaExtension(extensions []string) string {
	for _, preferred := range []string{".mp4", ".jpg", ".png", ".webp", ".mov"} {
		for _, extension := range extensions {
			if extension == preferred {
				return extension
			}
		}
	}
	return extensions[0]
}

// FetchNewRecords 根据历史记录和时间节点抓取新的微博数据。
func (s *Scraper) FetchNewRecords(ctx context.Context) ([]*WeiboRecord, error) {
	seenIDs, err := s.GetSeenIDs()
	if err != nil {
		return nil, err
	}

	s.logger.Info(
		"开始抓取微博",
		"target_uid", s.cfg.Weibo.TargetUID,
		"since_time", s.cfg.Weibo.SinceTime,
		"history_file", s.cfg.Weibo.HistoryFile,
	)

	var allItems []WeiboItem
	for page := 1; ; page++ {
		if s.cfg.Weibo.MaxPages > 0 && page > s.cfg.Weibo.MaxPages {
			s.logger.Info("达到最大抓取页数", "max_pages", s.cfg.Weibo.MaxPages)
			break
		}

		items, err := s.fetchPage(ctx, page)
		if err != nil {
			return nil, fmt.Errorf("获取第 %d 页失败: %w", page, err)
		}
		if len(items) == 0 {
			s.logger.Info("未获取到更多微博，抓取结束", "page", page)
			break
		}

		s.logger.Info("读取页面数据", "page", page, "count", len(items))
		allItems = append(allItems, items...)

		hitSeenID := false
		for _, item := range items {
			if seenIDs[idStr(item.ID)] {
				hitSeenID = true
				break
			}
		}

		hitSinceBoundary := s.pageReachedSinceBoundary(items)
		if hitSeenID {
			s.logger.Info("检测到历史记录重叠，停止翻页", "page", page)
		}
		if hitSinceBoundary {
			s.logger.Info("检测到 since_time 边界，停止翻页", "page", page)
		}
		if hitSeenID || hitSinceBoundary {
			break
		}

		time.Sleep(time.Duration(s.cfg.Weibo.PageIntervalSeconds) * time.Second)
	}

	if len(allItems) == 0 {
		return nil, nil
	}

	s.captureLatestFetchedTime(allItems)

	seenInBatch := make(map[string]bool, len(allItems))
	uniqueItems := make([]WeiboItem, 0, len(allItems))
	for _, item := range allItems {
		wid := idStr(item.ID)
		if wid == "" || seenInBatch[wid] {
			continue
		}
		seenInBatch[wid] = true
		uniqueItems = append(uniqueItems, item)
	}

	newRecords := make([]*WeiboRecord, 0, len(uniqueItems))
	for i := len(uniqueItems) - 1; i >= 0; i-- {
		item := uniqueItems[i]
		wid := idStr(item.ID)
		if wid == "" || seenIDs[wid] || !s.isAfterSince(item.CreatedAt) {
			continue
		}

		finalText := s.extractText(ctx, &item)
		mediaURLs := extractMedia(&item)
		isRetweet := false

		if item.RetweetedStatus != nil {
			isRetweet = true
			rt := item.RetweetedStatus
			authorName := "Unknown"
			if rt.User != nil && strings.TrimSpace(rt.User.ScreenName) != "" {
				authorName = rt.User.ScreenName
			}
			rtText := s.extractText(ctx, rt)
			finalText += fmt.Sprintf("\n\n[转发自 @%s]:\n%s", authorName, rtText)
			mediaURLs = uniqueStrings(append(mediaURLs, extractMedia(rt)...))
		}

		localPaths := make([]string, 0, len(mediaURLs))
		failedMediaURLs := make([]string, 0)
		for _, mediaURL := range mediaURLs {
			if localPath := s.DownloadMedia(mediaURL); localPath != "" {
				localPaths = append(localPaths, localPath)
				continue
			}
			failedMediaURLs = append(failedMediaURLs, mediaURL)
		}

		record := &WeiboRecord{
			ID:              wid,
			CreatedAt:       item.CreatedAt,
			Text:            finalText,
			SourceURL:       buildWeiboURL(s.cfg.Weibo.TargetUID, &item),
			MediaURLs:       mediaURLs,
			LocalMediaPaths: uniqueStrings(localPaths),
			IsRetweet:       isRetweet,
			FailedMediaURLs: uniqueStrings(failedMediaURLs),
		}

		newRecords = append(newRecords, record)
		s.logger.Info(
			"抓取到新微博",
			"id", record.ID,
			"created_at", record.CreatedAt,
			"media_count", len(record.LocalMediaPaths),
		)
	}

	s.logger.Info("抓取完成", "new_count", len(newRecords))
	return newRecords, nil
}

func (s *Scraper) captureLatestFetchedTime(items []WeiboItem) {
	s.latestFetchedAt = time.Time{}
	s.hasLatestFetchedAt = false

	for _, item := range items {
		createdAt, err := ParseWeiboTime(item.CreatedAt)
		if err != nil {
			s.logger.Warn("微博时间解析失败，无法写入最新抓取时间", "created_at", item.CreatedAt, "err", err)
			continue
		}
		if !s.hasLatestFetchedAt || createdAt.After(s.latestFetchedAt) {
			s.latestFetchedAt = createdAt
			s.hasLatestFetchedAt = true
		}
	}
}

func (s *Scraper) pageReachedSinceBoundary(items []WeiboItem) bool {
	if !s.hasSinceTime {
		return false
	}

	for _, item := range items {
		createdAt, err := ParseWeiboTime(item.CreatedAt)
		if err != nil {
			s.logger.Warn("微博时间解析失败，无法用于翻页截断", "created_at", item.CreatedAt, "err", err)
			continue
		}
		if !createdAt.After(s.sinceTime) {
			return true
		}
	}
	return false
}

func (s *Scraper) isAfterSince(createdAt string) bool {
	if !s.hasSinceTime {
		return true
	}

	parsedTime, err := ParseWeiboTime(createdAt)
	if err != nil {
		s.logger.Warn("微博时间解析失败，跳过该条记录", "created_at", createdAt, "err", err)
		return false
	}

	return parsedTime.After(s.sinceTime)
}
