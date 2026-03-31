package groupchat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"weibo_group_chat_monitor/config"

	playwright "github.com/playwright-community/playwright-go"
)

type Scraper struct {
	cfg            *config.GroupChatModeConfig
	logger         *slog.Logger
	downloadClient *http.Client
	pw             *playwright.Playwright
	browser        playwright.BrowserContext
	page           playwright.Page
}

type RunResult struct {
	NewRecords     []OutputRecord
	LatestBoundary MessageBoundary
}

type MessageBoundary struct {
	ID   string
	Time time.Time
}

type fetchEvalResult struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error"`
	Data  ChatAPIResponse `json:"data"`
}

type browserDownloadResult struct {
	Success     bool   `json:"success"`
	Error       string `json:"error"`
	Status      int    `json:"status"`
	ContentType string `json:"contentType"`
	Base64Data  string `json:"base64data"`
}

func NewScraper(cfg *config.GroupChatModeConfig, logger *slog.Logger) *Scraper {
	return &Scraper{
		cfg:    cfg,
		logger: logger,
		downloadClient: &http.Client{
			Timeout: time.Duration(cfg.Chat.DirectDownloadTimeoutSeconds) * time.Second,
		},
	}
}

func (s *Scraper) Run(ctx context.Context) (*RunResult, error) {
	if err := s.prepareOutput(); err != nil {
		return nil, err
	}

	seenIDs, err := loadSeenIDs(s.cfg.Output.HistoryFile)
	if err != nil {
		return nil, err
	}

	state, err := LoadRunState(s.cfg.State.StateFile)
	if err != nil {
		return nil, err
	}

	if err := s.startBrowser(); err != nil {
		return nil, err
	}
	defer s.close()

	initialResponse, err := s.openChatAndWaitForInitialResponse()
	if err != nil {
		return nil, err
	}

	var initialData ChatAPIResponse
	if err := initialResponse.JSON(&initialData); err != nil {
		return nil, fmt.Errorf("解析初始群聊响应失败: %w", err)
	}

	boundary := latestBoundary(initialData.Messages)
	collected := make(map[string]OutputRecord)

	maxMID, shouldStop, err := s.collectMessages(initialData.Messages, state, seenIDs, collected)
	if err != nil {
		return nil, err
	}

	for strings.TrimSpace(maxMID) != "" && !shouldStop {
		messages, err := s.fetchHistoryBatch(maxMID)
		if err != nil {
			s.logger.Error("抓取历史消息失败，将稍后重试", "max_mid", maxMID, "err", err)
			if err := sleepContext(ctx, time.Duration(s.cfg.Chat.RetryDelayMilliseconds)*time.Millisecond); err != nil {
				return nil, err
			}
			continue
		}
		if len(messages) == 0 {
			break
		}

		filtered := make([]ChatMessage, 0, len(messages))
		for _, msg := range messages {
			if msg.IDString() == maxMID {
				continue
			}
			filtered = append(filtered, msg)
		}
		if len(filtered) == 0 {
			break
		}

		maxMID, shouldStop, err = s.collectMessages(filtered, state, seenIDs, collected)
		if err != nil {
			return nil, err
		}
		if shouldStop {
			break
		}

		if err := sleepContext(ctx, time.Duration(s.cfg.Chat.HistoryIntervalMilliseconds)*time.Millisecond); err != nil {
			return nil, err
		}
	}

	records := make([]OutputRecord, 0, len(collected))
	for _, record := range collected {
		records = append(records, record)
	}
	sortOutputRecords(records)

	if err := appendRecords(s.cfg.Output.HistoryFile, records); err != nil {
		return nil, err
	}

	if boundary.ID != "" || !boundary.Time.IsZero() {
		state.SetBoundary(boundary.ID, boundary.Time)
		state.SetLastRunAt(time.Now())
		if err := SaveRunState(s.cfg.State.StateFile, state); err != nil {
			return nil, err
		}
	}

	s.logger.Info(
		"群聊历史抓取完成",
		"new_count", len(records),
		"history_dir", historyOutputDir(s.cfg.Output.HistoryFile),
		"state_file", s.cfg.State.StateFile,
	)

	result := &RunResult{
		NewRecords:     records,
		LatestBoundary: boundary,
	}
	if s.cfg.Browser.KeepOpen {
		s.logger.Info("keep_open 已开启，浏览器将保持打开直到收到退出信号")
		<-ctx.Done()
	}
	return result, nil
}

func (s *Scraper) collectMessages(
	messages []ChatMessage,
	state *RunState,
	seenIDs map[string]struct{},
	collected map[string]OutputRecord,
) (string, bool, error) {
	if len(messages) == 0 {
		return "", false, nil
	}

	sortedMessages := append([]ChatMessage(nil), messages...)
	sort.Slice(sortedMessages, func(i, j int) bool {
		return chatMessageLess(sortedMessages[j], sortedMessages[i])
	})

	shouldStop := false
	for _, msg := range sortedMessages {
		readableTime := msg.ReadableTime(time.Now())
		sender := msg.SenderName()
		text := msg.TextContent()

		if !isAfterStateBoundary(state, msg) {
			shouldStop = true
			break
		}

		if matchesStopCondition(s.cfg.StopCondition, readableTime, sender, text) {
			s.logger.Info(
				"触发手动停止节点",
				"time", readableTime,
				"sender", sender,
				"message", truncateText(text, 30),
			)
			shouldStop = true
			break
		}

		id := msg.IDString()
		if id == "" {
			continue
		}
		if _, ok := seenIDs[id]; ok {
			continue
		}
		if _, ok := collected[id]; ok {
			continue
		}

		mediaPaths := s.downloadMessageMedia(msg)
		collected[id] = buildOutputRecord(msg, readableTime, sender, text, mediaPaths)
	}

	newest := sortedMessages[0]
	s.logger.Info(
		"处理完成一个批次",
		"latest_time", newest.ReadableTime(time.Now()),
		"latest_sender", newest.SenderName(),
		"latest_message", truncateText(newest.TextContent(), 30),
		"stop", shouldStop,
	)

	if shouldStop {
		return "", true, nil
	}
	return sortedMessages[len(sortedMessages)-1].IDString(), false, nil
}

func latestBoundary(messages []ChatMessage) MessageBoundary {
	if len(messages) == 0 {
		return MessageBoundary{}
	}

	sortedMessages := append([]ChatMessage(nil), messages...)
	sort.Slice(sortedMessages, func(i, j int) bool {
		return chatMessageLess(sortedMessages[i], sortedMessages[j])
	})

	latest := sortedMessages[len(sortedMessages)-1]
	return MessageBoundary{
		ID:   latest.IDString(),
		Time: latest.TimeValue(time.Now()),
	}
}

func (s *Scraper) prepareOutput() error {
	if err := os.MkdirAll(historyOutputDir(s.cfg.Output.HistoryFile), 0o755); err != nil {
		return fmt.Errorf("创建历史输出目录失败: %w", err)
	}
	if err := ensureParentDir(s.cfg.State.StateFile); err != nil {
		return fmt.Errorf("创建状态文件目录失败: %w", err)
	}
	if err := os.MkdirAll(s.cfg.Output.MediaDir, 0o755); err != nil {
		return fmt.Errorf("创建媒体目录失败: %w", err)
	}
	return nil
}

func (s *Scraper) startBrowser() error {
	runOptions := &playwright.RunOptions{
		Browsers: []string{"chromium"},
		Verbose:  false,
		Stdout:   io.Discard,
		Stderr:   os.Stderr,
	}

	pw, err := playwright.Run(runOptions)
	if err != nil {
		s.logger.Info("Playwright driver/browser 未就绪，开始安装 Chromium")
		if installErr := playwright.Install(runOptions); installErr != nil {
			return fmt.Errorf("安装 Playwright 失败: %w", installErr)
		}
		pw, err = playwright.Run(runOptions)
		if err != nil {
			return fmt.Errorf("启动 Playwright 失败: %w", err)
		}
	}
	s.pw = pw

	launchOptions := playwright.BrowserTypeLaunchPersistentContextOptions{
		AcceptDownloads: playwright.Bool(true),
		Headless:        playwright.Bool(s.cfg.Browser.Headless),
		Viewport: &playwright.Size{
			Width:  s.cfg.Browser.ViewportWidth,
			Height: s.cfg.Browser.ViewportHeight,
		},
	}
	if channel := strings.TrimSpace(s.cfg.Browser.BrowserChannel); channel != "" {
		launchOptions.Channel = playwright.String(channel)
	}

	browser, err := pw.Chromium.LaunchPersistentContext(s.cfg.Browser.UserDataDir, launchOptions)
	if err != nil {
		return fmt.Errorf("启动 Playwright 持久化上下文失败: %w", err)
	}
	s.browser = browser

	page, err := ensureContextPage(browser)
	if err != nil {
		return fmt.Errorf("创建或获取页面失败: %w", err)
	}
	s.page = page

	if !s.cfg.Browser.Headless {
		if err := page.BringToFront(); err != nil {
			s.logger.Debug("切换浏览器前台失败", "err", err)
		}
	}

	return nil
}

func (s *Scraper) close() {
	if s.browser != nil {
		if err := s.browser.Close(); err != nil {
			s.logger.Warn("关闭浏览器上下文失败", "err", err)
		}
	}
	if s.pw != nil {
		if err := s.pw.Stop(); err != nil {
			s.logger.Warn("关闭 Playwright 失败", "err", err)
		}
	}
}

func (s *Scraper) openChatAndWaitForInitialResponse() (playwright.Response, error) {
	timeout := timeoutMillisPointer(s.cfg.Browser.InitialLoadTimeoutSeconds)
	response, err := s.page.ExpectResponse(func(url string) bool {
		return strings.Contains(url, s.cfg.Chat.APIURLBase)
	}, func() error {
		s.logger.Info(
			"打开微博群聊页面",
			"url", s.cfg.Chat.URL,
			"user_data_dir", s.cfg.Browser.UserDataDir,
			"browser_channel", s.cfg.Browser.BrowserChannel,
		)
		if _, err := s.page.Goto(s.cfg.Chat.URL); err != nil {
			s.logger.Warn("页面导航返回错误，将继续等待群聊消息接口", "err", err)
		}
		s.logger.Info("如未登录，请在打开的浏览器中完成登录；程序正在等待群聊消息接口返回")
		return nil
	}, playwright.PageExpectResponseOptions{Timeout: timeout})
	if err != nil {
		return nil, fmt.Errorf("等待群聊消息接口失败: %w", err)
	}
	if response.Status() != http.StatusOK {
		return nil, fmt.Errorf("群聊消息接口返回异常状态: %d", response.Status())
	}
	if response.Request().Method() != http.MethodGet {
		return nil, fmt.Errorf("群聊消息接口返回了非 GET 请求: %s", response.Request().Method())
	}

	s.logger.Info("群聊页面已加载，开始抓取历史消息")
	return response, nil
}

func (s *Scraper) fetchHistoryBatch(maxMID string) ([]ChatMessage, error) {
	fetchURL := fmt.Sprintf(
		"%s?id=%s&count=%d&convert_emoji=1&query_sender=1&source=%s&max_mid=%s",
		s.cfg.Chat.APIURLBase,
		s.cfg.Chat.GroupID,
		s.cfg.Chat.BatchSize,
		s.cfg.Chat.Source,
		maxMID,
	)

	var result fetchEvalResult
	err := evaluateInto(s.page, `
	async ({ url, timeoutMs }) => {
		const controller = new AbortController();
		const timer = setTimeout(() => controller.abort(), timeoutMs);
		try {
			const response = await fetch(url, {
				method: "GET",
				credentials: "include",
				signal: controller.signal
			});
			if (!response.ok) {
				return { ok: false, error: `+"`HTTP ${response.status}`"+` };
			}
			return { ok: true, data: await response.json() };
		} catch (e) {
			return { ok: false, error: String(e) };
		} finally {
			clearTimeout(timer);
		}
	}
	`, map[string]any{
		"url":       fetchURL,
		"timeoutMs": s.cfg.Chat.HistoryFetchTimeoutSeconds * 1000,
	}, &result)
	if err != nil {
		return nil, fmt.Errorf("执行浏览器内 fetch 失败: %w", err)
	}
	if !result.OK {
		if result.Error == "" {
			result.Error = "unknown error"
		}
		return nil, fmt.Errorf("%s", result.Error)
	}

	return result.Data.Messages, nil
}

func (s *Scraper) downloadMessageMedia(msg ChatMessage) []string {
	var paths []string

	for _, fid := range msg.FIDs {
		path, err := s.downloadByFID(fid)
		if err != nil {
			s.logger.Error("按 fid 下载媒体失败", "fid", fid, "err", err)
			s.recordMediaDownloadFailure(msg, "fid", fid, err)
			continue
		}
		if path != "" {
			paths = append(paths, path)
		}
	}

	if msg.PageInfo != nil {
		switch msg.PageInfo.Type.String() {
		case "pic":
			if mediaURL := normalizeMediaURL(msg.PageInfo.PreferredPictureURL()); mediaURL != "" {
				filename := sanitizeFilename(fmt.Sprintf("%s_image.jpg", msg.IDString()))
				if path, err := s.downloadDirectMedia(mediaURL, filename); err != nil {
					s.logger.Error("下载页面图片失败", "url", mediaURL, "err", err)
					s.recordMediaDownloadFailure(msg, "page_pic", mediaURL, err)
				} else if path != "" {
					paths = append(paths, path)
				}
			}
		case "video":
			if mediaURL := normalizeMediaURL(msg.PageInfo.PreferredVideoURL()); mediaURL != "" {
				filename := sanitizeFilename(fmt.Sprintf("%s_video.mp4", msg.IDString()))
				if path, err := s.downloadDirectMedia(mediaURL, filename); err != nil {
					s.logger.Error("下载页面视频失败", "url", mediaURL, "err", err)
					s.recordMediaDownloadFailure(msg, "page_video", mediaURL, err)
				} else if path != "" {
					paths = append(paths, path)
				}
			}
		}
	}

	return uniqueStrings(paths)
}

func (s *Scraper) downloadByFID(fid string) (string, error) {
	fid = strings.TrimSpace(fid)
	if fid == "" {
		return "", nil
	}

	value, err := s.page.Evaluate(`
	({ source, fid }) => {
		return new Promise((resolve) => {
			window.__group_chat_meta_cb = function(data) {
				resolve(data);
			};
			const script = document.createElement("script");
			script.src = `+"`https://upload.api.weibo.com/2/mss/meta_query.json?source=${source}&fid=${fid}&replace=false&callback=__group_chat_meta_cb`"+`;
			script.onerror = () => resolve(null);
			document.head.appendChild(script);
			setTimeout(() => resolve(null), 5000);
		});
	}
	`, map[string]any{
		"source": s.cfg.Chat.Source,
		"fid":    fid,
	})
	if err != nil {
		return "", fmt.Errorf("执行 meta_query 失败: %w", err)
	}

	var meta MetaQueryResult
	if payload, marshalErr := json.Marshal(value); marshalErr == nil && string(payload) != "null" {
		if err := json.Unmarshal(payload, &meta); err != nil {
			return "", fmt.Errorf("解析 meta_query 响应失败: %w", err)
		}
	}

	ext := strings.TrimSpace(meta.Extension)
	if ext == "" {
		ext = "jpg"
	}

	origName := sanitizeFilename(strings.TrimSpace(meta.Filename))
	if origName == "" || origName == "file" {
		origName = fmt.Sprintf("%s.%s", fid, ext)
	}

	filename := sanitizeFilename(fmt.Sprintf("img_%s_%s", fid, origName))
	path := filepath.Join(s.cfg.Output.MediaDir, filename)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	downloadURL := fmt.Sprintf(
		"https://upload.api.weibo.com/2/mss/msget?fid=%s&source=%s&imageType=origin&ts=%d",
		fid,
		s.cfg.Chat.Source,
		time.Now().UnixMilli(),
	)

	response, err := s.page.ExpectResponse(func(url string) bool {
		return strings.Contains(url, "msget") && strings.Contains(url, fid)
	}, func() error {
		_, err := s.page.Evaluate(`
			url => {
			const img = document.createElement("img");
			img.src = url;
			img.style.display = "none";
			document.body.appendChild(img);
		}
		`, downloadURL)
		return err
	}, playwright.PageExpectResponseOptions{Timeout: timeoutMillisPointer(s.cfg.Chat.ImageResponseTimeoutSeconds)})
	if err != nil {
		s.logger.Warn("通过响应监听下载 fid 失败，尝试回退到浏览器内 fetch", "fid", fid, "err", err)
		return s.downloadByFIDViaBrowserFetch(fid, path)
	}
	if response.Status() != http.StatusOK {
		s.logger.Warn("msget 返回异常状态，尝试回退到浏览器内 fetch", "fid", fid, "status", response.Status())
		return s.downloadByFIDViaBrowserFetch(fid, path)
	}

	body, err := response.Body()
	if err != nil {
		s.logger.Warn("读取 msget 响应体失败，尝试回退到浏览器内 fetch", "fid", fid, "err", err)
		return s.downloadByFIDViaBrowserFetch(fid, path)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return "", fmt.Errorf("写入 fid 媒体文件失败: %w", err)
	}
	return path, nil
}

func (s *Scraper) downloadByFIDViaBrowserFetch(fid, path string) (string, error) {
	candidateURLs := s.fidDownloadCandidateURLs(fid)

	var lastErr error
	for _, candidateURL := range candidateURLs {
		body, err := s.fetchBinaryInBrowser(candidateURL)
		if err != nil {
			lastErr = err
			s.logger.Warn("浏览器内 fetch 下载 fid 失败", "fid", fid, "url", candidateURL, "err", err)
			continue
		}

		if err := os.WriteFile(path, body, 0o644); err != nil {
			return "", fmt.Errorf("写入 fid 媒体文件失败: %w", err)
		}
		s.logger.Info("fid 媒体已通过浏览器 fetch 回退下载成功", "fid", fid, "path", path)
		return path, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("all candidate msget urls failed")
	}
	s.logger.Warn("浏览器内 fid 下载全部失败，尝试直接 HTTP 下载", "fid", fid, "err", lastErr)
	return s.downloadByFIDViaDirectHTTP(fid, path, candidateURLs)
}

func (s *Scraper) fidDownloadCandidateURLs(fid string) []string {
	ts := time.Now().UnixMilli()
	return []string{
		fmt.Sprintf(
			"https://upload.api.weibo.com/2/mss/msget?fid=%s&source=%s&imageType=origin&ts=%d",
			fid,
			s.cfg.Chat.Source,
			ts,
		),
		fmt.Sprintf(
			"https://upload.api.weibo.com/2/mss/msget?fid=%s&touid=%s&ts=%d",
			fid,
			s.cfg.Chat.GroupID,
			ts,
		),
		fmt.Sprintf(
			"https://upload.api.weibo.com/2/mss/msget?fid=%s&source=%s&touid=%s&imageType=origin&ts=%d",
			fid,
			s.cfg.Chat.Source,
			s.cfg.Chat.GroupID,
			ts,
		),
	}
}

func (s *Scraper) downloadByFIDViaDirectHTTP(fid, path string, candidateURLs []string) (string, error) {
	var lastErr error
	for _, candidateURL := range candidateURLs {
		body, err := s.fetchBinaryDirect(candidateURL)
		if err != nil {
			lastErr = err
			s.logger.Warn("直接 HTTP 下载 fid 失败", "fid", fid, "url", candidateURL, "err", err)
			continue
		}

		if err := os.WriteFile(path, body, 0o644); err != nil {
			return "", fmt.Errorf("写入 fid 媒体文件失败: %w", err)
		}
		s.logger.Info("fid 媒体已通过直接 HTTP 回退下载成功", "fid", fid, "path", path)
		return path, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("all direct http candidate msget urls failed")
	}
	return "", lastErr
}

func (s *Scraper) fetchBinaryInBrowser(url string) ([]byte, error) {
	var result browserDownloadResult
	err := evaluateInto(s.page, `
	async ({ url }) => {
		try {
			const response = await fetch(url, {
				method: "GET",
				credentials: "include",
				headers: {
					"Referer": "https://api.weibo.com/chat/"
				}
			});

			if (!response.ok) {
				let text = "";
				try {
					text = await response.text();
				} catch (_) {}
				return {
					success: false,
					status: response.status,
					error: text || `+"`HTTP ${response.status}`"+`
				};
			}

			const contentType = response.headers.get("content-type") || "";
			const blob = await response.blob();

			return await new Promise((resolve) => {
				const reader = new FileReader();
				reader.onloadend = () => resolve({
					success: true,
					contentType,
					base64data: String(reader.result || "")
				});
				reader.onerror = () => resolve({
					success: false,
					error: "FileReader failed"
				});
				reader.readAsDataURL(blob);
			});
		} catch (e) {
			return {
				success: false,
				error: String(e)
			};
		}
	}
	`, map[string]any{
		"url": url,
	}, &result)
	if err != nil {
		return nil, fmt.Errorf("执行浏览器内二进制 fetch 失败: %w", err)
	}
	if !result.Success {
		if result.Error == "" {
			result.Error = "unknown error"
		}
		return nil, fmt.Errorf("%s", result.Error)
	}

	parts := strings.SplitN(result.Base64Data, ",", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("unexpected data url payload")
	}

	body, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("解码浏览器返回的 base64 数据失败: %w", err)
	}
	return body, nil
}

func (s *Scraper) fetchBinaryDirect(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://api.weibo.com/chat/")
	req.Header.Set("Accept", "*/*")
	if cookieHeader := s.contextCookieHeader(); cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}

	resp, err := s.downloadClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return io.ReadAll(resp.Body)
}

func (s *Scraper) downloadDirectMedia(url, filename string) (string, error) {
	url = normalizeMediaURL(url)
	if url == "" {
		return "", nil
	}

	filename = sanitizeFilename(filename)
	if filename == "" || filename == "file" {
		parts := strings.Split(strings.Split(url, "?")[0], "/")
		filename = sanitizeFilename(parts[len(parts)-1])
	}
	if filename == "" || filename == "file" {
		filename = fmt.Sprintf("media_%d.file", time.Now().UnixMilli())
	}

	path := filepath.Join(s.cfg.Output.MediaDir, filename)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://weibo.com/")
	if cookieHeader := s.contextCookieHeader(); cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}

	resp, err := s.downloadClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	file, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Scraper) contextCookieHeader() string {
	if s.browser == nil {
		return ""
	}

	cookies, err := s.browser.Cookies("https://weibo.com", "https://api.weibo.com", "https://upload.api.weibo.com")
	if err != nil {
		s.logger.Debug("读取浏览器 Cookie 失败", "err", err)
		return ""
	}

	seen := make(map[string]struct{}, len(cookies))
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie.Name == "" || cookie.Value == "" {
			continue
		}
		if _, ok := seen[cookie.Name]; ok {
			continue
		}
		seen[cookie.Name] = struct{}{}
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

func ensureContextPage(browser playwright.BrowserContext) (playwright.Page, error) {
	pages := browser.Pages()
	if len(pages) > 0 {
		return pages[0], nil
	}
	return browser.NewPage()
}

func (s *Scraper) recordMediaDownloadFailure(msg ChatMessage, mediaType, mediaRef string, mediaErr error) {
	if s == nil {
		return
	}

	recordPath := failedMediaRecordPath(s.cfg.Output.HistoryFile)
	if err := ensureParentDir(recordPath); err != nil {
		s.logger.Warn("创建失败消息记录目录失败", "path", recordPath, "err", err)
		return
	}

	record := FailedMediaRecord{
		RecordedAt:  time.Now().In(time.Local).Format(time.RFC3339),
		MessageID:   msg.IDString(),
		MessageTime: msg.ReadableTime(time.Now()),
		Sender:      msg.SenderName(),
		MediaType:   strings.TrimSpace(mediaType),
		MediaRef:    strings.TrimSpace(mediaRef),
		RawMessage:  msg.RawPayload(),
	}
	if mediaErr != nil {
		record.Error = mediaErr.Error()
	}

	line, err := json.Marshal(record)
	if err != nil {
		s.logger.Warn("序列化失败消息记录失败", "message_id", record.MessageID, "err", err)
		return
	}

	file, err := os.OpenFile(recordPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		s.logger.Warn("打开失败消息记录文件失败", "path", recordPath, "err", err)
		return
	}
	defer file.Close()

	if _, err := fmt.Fprintf(file, "%s\n", line); err != nil {
		s.logger.Warn("写入失败消息记录失败", "path", recordPath, "err", err)
		return
	}
	s.logger.Warn(
		"已保存媒体下载失败消息",
		"path", recordPath,
		"message_id", record.MessageID,
		"media_type", record.MediaType,
		"media_ref", record.MediaRef,
	)
}
