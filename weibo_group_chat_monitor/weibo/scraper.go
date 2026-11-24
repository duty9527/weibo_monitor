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
	apiBaseURL         string
	httpClient         *http.Client
	mediaClient        *http.Client
	mediaRetryAttempts int
	mediaRetryDelay    time.Duration
	sessionHeaders     map[string]string
	logger             *slog.Logger
	sinceTime          time.Time
	hasSinceTime       bool
	latestFetchedAt    time.Time
	hasLatestFetchedAt bool
}

const (
	defaultAPIBaseURL         = "https://weibo.com"
	defaultMediaRetryAttempts = 3
	defaultMediaRetryDelay    = time.Second
	defaultWeiboUserAgent     = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36"
)

// NewScraper 创建抓取器并解析时间节点。
func NewScraper(cfg *config.WeiboModeConfig, logger *slog.Logger) (*Scraper, error) {
	mediaTransport := &http.Transport{
		ResponseHeaderTimeout: 15 * time.Second,
	}
	if baseTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		mediaTransport = baseTransport.Clone()
		mediaTransport.ResponseHeaderTimeout = 15 * time.Second
	}

	s := &Scraper{
		cfg:        cfg,
		apiBaseURL: defaultAPIBaseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		mediaClient: &http.Client{
			Timeout:   120 * time.Second,
			Transport: mediaTransport,
		},
		mediaRetryAttempts: defaultMediaRetryAttempts,
		mediaRetryDelay:    defaultMediaRetryDelay,
		sessionHeaders:     map[string]string{},
		logger:             logger,
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
	baseURL := strings.TrimRight(strings.TrimSpace(s.apiBaseURL), "/")
	if baseURL == "" {
		baseURL = defaultAPIBaseURL
	}
	apiURL := fmt.Sprintf(
		"%s/ajax/statuses/mymblog?uid=%s&page=%d&feature=0",
		baseURL, s.cfg.Weibo.TargetUID, page,
	)

	resp, err := s.doGet(ctx, apiURL)
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
	if apiResp.OK != nil && *apiResp.OK != 1 {
		return nil, fmt.Errorf("微博列表接口返回异常状态 (ok=%d, url=%s, msg=%s)", *apiResp.OK, apiResp.URL, apiResp.Msg)
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

	seenPicIDs := make(map[string]struct{})
	for _, pic := range item.PicInfos {
		var chosen string
		switch {
		case pic.Mw2000 != nil && pic.Mw2000.URL != "":
			chosen = pic.Mw2000.URL
		case pic.Original != nil && pic.Original.URL != "":
			chosen = pic.Original.URL
		case pic.Large != nil && pic.Large.URL != "":
			chosen = pic.Large.URL
		}
		if chosen != "" {
			urls = append(urls, chosen)
			if id := mediaIDFromURL(chosen); id != "" {
				seenPicIDs[id] = struct{}{}
			}
		}
	}

	mixedURLs := extractMixedMedia(item.MixMediaInfo)
	for _, mixedURL := range mixedURLs {
		if id := mediaIDFromURL(mixedURL); id != "" {
			seenPicIDs[id] = struct{}{}
		}
	}
	urls = append(urls, mixedURLs...)

	if len(item.PicInfos) == 0 {
		for _, pid := range item.PicIDs {
			pid = strings.TrimSpace(pid)
			if pid == "" {
				continue
			}
			if _, ok := seenPicIDs[pid]; ok {
				continue
			}
			urls = append(urls, fmt.Sprintf("https://wx1.sinaimg.cn/mw2000/%s.jpg", pid))
		}
	}

	return uniqueStrings(urls)
}

func mediaIDFromURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	base := path.Base(parsed.Path)
	if base == "" || base == "." || base == "/" {
		return ""
	}
	ext := path.Ext(base)
	return strings.TrimSuffix(base, ext)
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
func (s *Scraper) DownloadMedia(rawURL string) string {
	targetURL := normalizeMediaDownloadURL(rawURL)
	if targetURL == "" {
		return ""
	}
	if err := os.MkdirAll(s.cfg.Weibo.MediaDir, 0o755); err != nil {
		s.logger.Error("创建媒体目录失败", "err", err)
		return ""
	}

	if hasKnownMediaFilename(targetURL) {
		fpath := filepath.Join(s.cfg.Weibo.MediaDir, mediaFilename(targetURL, ""))
		if info, err := os.Stat(fpath); err == nil && info.Size() > 0 {
			return fpath
		}
	}

	attempts := s.mediaRetryAttempts
	if attempts <= 0 {
		attempts = defaultMediaRetryAttempts
	}
	baseDelay := s.mediaRetryDelay
	if baseDelay <= 0 {
		baseDelay = defaultMediaRetryDelay
	}

	var lastErr error
	var usedAttempts int
	for attempt := 1; attempt <= attempts; attempt++ {
		usedAttempts = attempt
		fpath, retryable, err := s.downloadMediaOnce(targetURL)
		if err == nil {
			return fpath
		}
		lastErr = err
		if !retryable || attempt == attempts {
			break
		}

		delay := baseDelay << (attempt - 1)
		s.logger.Warn(
			"媒体下载失败，准备重试",
			"url", targetURL,
			"attempt", attempt,
			"max_attempts", attempts,
			"retry_after", delay,
			"err", err,
		)
		time.Sleep(delay)
	}

	s.logger.Error("下载失败", "url", targetURL, "attempts", usedAttempts, "err", lastErr)
	return ""
}

func (s *Scraper) downloadMediaOnce(targetURL string) (string, bool, error) {
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return "", false, err
	}

	ua := strings.TrimSpace(s.sessionHeaders["User-Agent"])
	if ua == "" {
		ua = defaultWeiboUserAgent
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://weibo.com/")
	if parsed, parseErr := url.Parse(targetURL); parseErr == nil {
		if strings.EqualFold(parsed.Scheme, "https") && shouldSendWeiboCookie(parsed.Hostname()) {
			if cookieStr := s.sessionHeaders["Cookie"]; cookieStr != "" {
				req.Header.Set("Cookie", cookieStr)
			}
		}
	}

	client := s.mediaClient
	if client == nil {
		client = s.httpClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", true, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		retryable := resp.StatusCode == http.StatusRequestTimeout ||
			resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode >= http.StatusInternalServerError
		return "", retryable, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "text/html") || strings.HasPrefix(contentType, "application/json") {
		return "", false, fmt.Errorf("响应不是媒体文件 (content_type=%s)", contentType)
	}

	filename := mediaFilename(targetURL, resp.Header.Get("Content-Type"))
	fpath := filepath.Join(s.cfg.Weibo.MediaDir, filename)
	if info, err := os.Stat(fpath); err == nil && info.Size() > 0 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fpath, false, nil
	}

	out, err := os.CreateTemp(s.cfg.Weibo.MediaDir, ".media-*")
	if err != nil {
		return "", false, fmt.Errorf("创建临时媒体文件失败: %w", err)
	}
	tempPath := out.Name()
	keepTemp := false
	defer func() {
		out.Close()
		if !keepTemp {
			_ = os.Remove(tempPath)
		}
	}()

	written, err := io.Copy(out, resp.Body)
	if err != nil {
		return "", true, fmt.Errorf("写入文件失败: %w", err)
	}
	if written == 0 {
		return "", true, fmt.Errorf("写入文件失败: 媒体响应为空")
	}
	if err := out.Close(); err != nil {
		return "", false, fmt.Errorf("关闭媒体文件失败: %w", err)
	}
	if err := os.Rename(tempPath, fpath); err != nil {
		return "", false, fmt.Errorf("保存媒体文件失败: %w", err)
	}
	keepTemp = true
	return fpath, false, nil
}

func normalizeMediaDownloadURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return trimmed
	}
	if strings.EqualFold(parsed.Scheme, "http") && isWeiboMediaCDNHost(parsed.Hostname()) {
		parsed.Scheme = "https"
		return parsed.String()
	}
	return trimmed
}

func isWeiboMediaCDNHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return host == "weibocdn.com" ||
		strings.HasSuffix(host, ".weibocdn.com") ||
		host == "sinaimg.cn" ||
		strings.HasSuffix(host, ".sinaimg.cn")
}

func shouldSendWeiboCookie(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return host == "weibo.com" ||
		strings.HasSuffix(host, ".weibo.com") ||
		host == "weibo.cn" ||
		strings.HasSuffix(host, ".weibo.cn")
}

func hasKnownMediaFilename(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	base := path.Base(parsed.Path)
	if decoded, decodeErr := url.PathUnescape(base); decodeErr == nil {
		base = decoded
	}
	base = filepath.Base(base)
	if base == "" || base == "." || base == "/" {
		return false
	}
	return isKnownMediaExtension(strings.ToLower(filepath.Ext(base)))
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
