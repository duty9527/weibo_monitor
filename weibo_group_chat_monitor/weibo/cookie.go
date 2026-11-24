package weibo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	playwright "github.com/mxschmitt/playwright-go"

	"weibo_group_chat_monitor/config"
)

const (
	defaultVerifyBaseURL    = "https://weibo.com"
	defaultVerifyTimeout    = 10 * time.Second
	defaultVerifyAttempts   = 3
	defaultVerifyRetryDelay = time.Second
	sessionSnapshotFilename = "weibo_session_cookies.json"
)

var hasInteractiveDisplay = defaultHasInteractiveDisplay

func defaultHasInteractiveDisplay() bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return true
	}
	return strings.TrimSpace(os.Getenv("DISPLAY")) != "" ||
		strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) != ""
}

// CookieVerifyError 表示微博 Cookie 校验失败及是否属于可恢复的瞬时异常。
type CookieVerifyError struct {
	Transient  bool
	StatusCode int
	Err        error
}

func (e *CookieVerifyError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("HTTP %d", e.StatusCode)
	}
	return "微博 Cookie 校验失败"
}

func (e *CookieVerifyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func isTransientCookieVerifyError(err error) bool {
	var verifyErr *CookieVerifyError
	return errors.As(err, &verifyErr) && verifyErr.Transient
}

type sessionCookieSnapshot struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires"`
	HTTPOnly bool    `json:"http_only"`
	Secure   bool    `json:"secure"`
}

// CookieExtractor 从浏览器用户数据目录提取微博 Cookie。
type CookieExtractor struct {
	userDataDir      string
	logger           *slog.Logger
	verifyClient     *http.Client
	verifyBaseURL    string
	verifyAttempts   int
	verifyRetryDelay time.Duration
	launchSession    func(ctx context.Context, cfg config.WeiboConfig, headless bool, waitForLogin bool) (string, error)
}

type ExtractResult struct {
	CookieString   string
	UsedPlaywright bool
}

// NewCookieExtractor 创建提取器。
func NewCookieExtractor(userDataDir string, logger *slog.Logger) *CookieExtractor {
	return &CookieExtractor{
		userDataDir:      userDataDir,
		logger:           logger,
		verifyClient:     &http.Client{Timeout: defaultVerifyTimeout},
		verifyBaseURL:    defaultVerifyBaseURL,
		verifyAttempts:   defaultVerifyAttempts,
		verifyRetryDelay: defaultVerifyRetryDelay,
	}
}

// ExtractOrLogin 优先尝试直接读取 Cookie，失败后使用纯 Go Playwright 打开持久化上下文读取/等待登录。
func (e *CookieExtractor) ExtractOrLogin(ctx context.Context, cfg config.WeiboConfig) (string, error) {
	result, err := e.ExtractOrLoginResult(ctx, cfg)
	if err != nil {
		return "", err
	}
	return result.CookieString, nil
}

// ExtractOrLoginResult 返回 Cookie 和是否使用了 Playwright。
func (e *CookieExtractor) ExtractOrLoginResult(ctx context.Context, cfg config.WeiboConfig) (*ExtractResult, error) {
	cookieStr, err := e.Extract(ctx)
	if err == nil && cookieStr != "" {
		verifyErr := e.verifyCookies(ctx, cookieStr, cfg.TargetUID)
		if verifyErr == nil {
			e.logger.Info("通过本地存储直接读取到有效 Cookie")
			return &ExtractResult{CookieString: cookieStr}, nil
		}
		if isTransientCookieVerifyError(verifyErr) {
			return nil, fmt.Errorf("校验本地微博 Cookie 时发生网络异常: %w", verifyErr)
		}
		e.logger.Warn("本地直读拿到的 Cookie 校验未通过，切换到 Playwright 持久化上下文", "err", verifyErr)
	} else if err != nil {
		e.logger.Info("本地直读未拿到可用 Cookie，切换到 Playwright 持久化上下文", "reason", err)
	}

	cookieStr, err = e.ExtractViaPlaywright(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &ExtractResult{CookieString: cookieStr, UsedPlaywright: true}, nil
}

// Extract 只尝试从磁盘上的浏览器存储读取 Cookie，不触发浏览器自动化。
func (e *CookieExtractor) Extract(ctx context.Context) (string, error) {
	e.logger.Info("正在从浏览器用户数据目录提取 Cookie...")

	snapshotPath := filepath.Join(e.userDataDir, sessionSnapshotFilename)
	cookieStr, err := readSessionSnapshot(snapshotPath, time.Now())
	if err == nil && cookieStr != "" {
		e.logger.Info("成功从共享登录快照提取 Cookie")
		return cookieStr, nil
	}
	if err != nil && !os.IsNotExist(err) {
		e.logger.Warn("读取共享登录快照失败", "err", err)
	}

	cookieStr, err = e.readChromiumCookies(ctx)
	if err == nil && cookieStr != "" {
		e.logger.Info("成功从浏览器数据库提取 Cookie")
		return cookieStr, nil
	}
	if err != nil {
		e.logger.Warn("直接读取浏览器 Cookie 失败", "err", err)
	}

	cookiesFile := filepath.Join(e.userDataDir, "cookies.txt")
	cookieStr, err = ReadNetscapeCookies(cookiesFile)
	if err == nil && cookieStr != "" {
		e.logger.Info("成功从 cookies.txt 提取 Cookie")
		return cookieStr, nil
	}
	if err != nil && !os.IsNotExist(err) {
		e.logger.Warn("读取 cookies.txt 失败", "err", err)
	}

	return "", fmt.Errorf("未能通过磁盘直读从 user_data_dir=%s 中读取到可用 Cookie", e.userDataDir)
}

// ExtractViaPlaywright 使用纯 Go Playwright 启动持久化上下文，必要时等待用户登录。
func (e *CookieExtractor) ExtractViaPlaywright(ctx context.Context, cfg config.WeiboConfig) (string, error) {
	sessionRunner := e.extractViaPlaywrightSession
	if e.launchSession != nil {
		sessionRunner = e.launchSession
	}

	cookieStr, headlessErr := sessionRunner(ctx, cfg, true, false)
	if headlessErr == nil && cookieStr != "" {
		return cookieStr, nil
	}
	if !hasInteractiveDisplay() {
		if headlessErr != nil {
			return "", fmt.Errorf("Playwright 无头模式未获取到有效 Cookie，且当前无图形会话（跳过有头浏览器降级）: %w", headlessErr)
		}
		return "", fmt.Errorf("Playwright 无头模式未获取到有效 Cookie，且当前无图形会话（跳过有头浏览器降级）")
	}
	if headlessErr != nil {
		e.logger.Warn("Playwright 无头模式未拿到有效 Cookie，将切换为可交互模式", "err", headlessErr)
	}

	return sessionRunner(ctx, cfg, false, true)
}

// RefreshViaPlaywright 使用持久化浏览器上下文主动访问微博，尝试刷新登录态但不等待人工登录。
func (e *CookieExtractor) RefreshViaPlaywright(ctx context.Context, cfg config.WeiboConfig) (string, error) {
	return e.extractViaPlaywrightSession(ctx, cfg, true, false)
}

func (e *CookieExtractor) extractViaPlaywrightSession(
	ctx context.Context,
	cfg config.WeiboConfig,
	headless bool,
	waitForLogin bool,
) (string, error) {
	runOptions := &playwright.RunOptions{
		Browsers: []string{"chromium"},
		Verbose:  false,
		Stdout:   io.Discard,
		Stderr:   os.Stderr,
	}

	pw, err := playwright.Run(runOptions)
	if err != nil {
		e.logger.Info("Playwright driver/browser 未就绪，开始安装 Chromium")
		if installErr := playwright.Install(runOptions); installErr != nil {
			return "", fmt.Errorf("安装 Playwright 失败: %w", installErr)
		}
		pw, err = playwright.Run(runOptions)
		if err != nil {
			return "", fmt.Errorf("启动 Playwright 失败: %w", err)
		}
	}
	defer pw.Stop()

	launchOptions := playwright.BrowserTypeLaunchPersistentContextOptions{
		Headless: playwright.Bool(headless),
		Timeout:  playwright.Float(float64(cfg.LoginTimeoutSeconds * 1000)),
	}
	if channel := strings.TrimSpace(cfg.BrowserChannel); channel != "" {
		launchOptions.Channel = playwright.String(channel)
	}

	browserContext, err := pw.Chromium.LaunchPersistentContext(cfg.UserDataDir, launchOptions)
	if err != nil {
		return "", fmt.Errorf("启动 Playwright 持久化上下文失败: %w", err)
	}
	defer browserContext.Close()

	page, err := ensureContextPage(browserContext)
	if err != nil {
		return "", fmt.Errorf("创建 Playwright 页面失败: %w", err)
	}
	if err := page.BringToFront(); err != nil {
		e.logger.Debug("切换浏览器前台失败", "err", err)
	}

	loginURL := strings.TrimSpace(cfg.LoginURL)
	if loginURL == "" {
		loginURL = fmt.Sprintf("https://weibo.com/u/%s", cfg.TargetUID)
	}
	if _, err := page.Goto(loginURL); err != nil {
		e.logger.Warn("导航到微博登录页失败，但将继续读取 Cookie", "url", loginURL, "err", err)
	} else {
		page.WaitForTimeout(3000)
	}

	cookieStr, readErr := e.readValidCookiesFromContext(ctx, browserContext, cfg)
	if readErr == nil && cookieStr != "" {
		return cookieStr, nil
	}

	if !waitForLogin {
		if readErr == nil {
			readErr = fmt.Errorf("持久化上下文中暂无有效微博 Cookie")
		} else if isTransientCookieVerifyError(readErr) || ctx.Err() != nil {
			return "", readErr
		}

		// Multi-hop Sina SSO redirects can take more than 3s after initial Goto.
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if !sleepWithContext(ctx, 2*time.Second) {
				return "", ctx.Err()
			}
			cookieStr, pollErr := e.readValidCookiesFromContext(ctx, browserContext, cfg)
			if pollErr == nil && cookieStr != "" {
				return cookieStr, nil
			}
			if pollErr != nil {
				readErr = pollErr
				if isTransientCookieVerifyError(pollErr) || ctx.Err() != nil {
					return "", pollErr
				}
			}
		}
		return "", readErr
	}

	e.logger.Info(
		"请在 Playwright 打开的浏览器中完成微博登录",
		"user_data_dir", cfg.UserDataDir,
		"login_url", loginURL,
		"browser_channel", cfg.BrowserChannel,
	)

	return e.waitForCookiesFromContext(ctx, browserContext, cfg)
}

func (e *CookieExtractor) waitForCookiesFromContext(
	ctx context.Context,
	browserContext playwright.BrowserContext,
	cfg config.WeiboConfig,
) (string, error) {
	timeout := time.Duration(cfg.LoginTimeoutSeconds) * time.Second
	interval := time.Duration(cfg.LoginCheckInterval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastErr error
	for {
		if cookieStr, err := e.readValidCookiesFromContext(waitCtx, browserContext, cfg); err == nil && cookieStr != "" {
			e.logger.Info("检测到有效微博 Cookie，登录成功")
			return cookieStr, nil
		} else if err != nil {
			lastErr = err
		}

		select {
		case <-waitCtx.Done():
			if lastErr != nil {
				return "", fmt.Errorf("等待微博登录超时，请在 %d 秒内完成登录: %w", cfg.LoginTimeoutSeconds, lastErr)
			}
			return "", fmt.Errorf("等待微博登录超时，请在 %d 秒内完成登录", cfg.LoginTimeoutSeconds)
		case <-ticker.C:
		}
	}
}

func (e *CookieExtractor) readValidCookiesFromContext(
	ctx context.Context,
	browserContext playwright.BrowserContext,
	cfg config.WeiboConfig,
) (string, error) {
	cookies, err := browserContext.Cookies("https://weibo.com")
	if err != nil {
		return "", err
	}

	cookieStr := cookiesToHeader(cookies)
	if cookieStr == "" {
		return "", fmt.Errorf("Playwright context 中暂无微博 Cookie")
	}
	if err := e.verifyCookies(ctx, cookieStr, cfg.TargetUID); err != nil {
		return "", fmt.Errorf("Playwright context 中的微博 Cookie 校验失败（已读到 Cookie: %s）: %w", joinCookieNames(cookies), err)
	}
	// Publish a private snapshot, never share the live Chromium profile/lock.
	if all, err := browserContext.Cookies(); err == nil {
		if err := saveSessionSnapshot(filepath.Join(e.userDataDir, sessionSnapshotFilename), all); err != nil {
			e.logger.Warn("导出共享登录快照失败", "err", err)
		}
	}
	return cookieStr, nil
}

func ensureContextPage(browserContext playwright.BrowserContext) (playwright.Page, error) {
	pages := browserContext.Pages()
	if len(pages) > 0 {
		return pages[0], nil
	}
	return browserContext.NewPage()
}

func cookiesToHeader(cookies []playwright.Cookie) string {
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie.Name == "" || cookie.Value == "" {
			continue
		}
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

func joinCookieNames(cookies []playwright.Cookie) string {
	names := make([]string, 0, len(cookies))
	seen := make(map[string]struct{}, len(cookies))

	for _, cookie := range cookies {
		if cookie.Name == "" {
			continue
		}
		if _, ok := seen[cookie.Name]; ok {
			continue
		}
		seen[cookie.Name] = struct{}{}
		names = append(names, cookie.Name)
	}

	return strings.Join(names, ",")
}

// readChromiumCookies 直接读取 Chromium Cookie SQLite 数据库。
func (e *CookieExtractor) readChromiumCookies(ctx context.Context) (string, error) {
	cookieDBPath := filepath.Join(e.userDataDir, "Default", "Cookies")

	tmpFile, err := os.CreateTemp("", "weibo_cookies_*.db")
	if err != nil {
		return "", fmt.Errorf("创建临时 Cookie 数据库失败: %w", err)
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("关闭临时 Cookie 数据库失败: %w", err)
	}
	defer os.Remove(tmpPath)

	if err := copyFile(cookieDBPath, tmpPath); err != nil {
		return "", fmt.Errorf("复制 Cookie 数据库失败: %w", err)
	}

	db, err := sql.Open("sqlite3", "file:"+tmpPath+"?mode=ro")
	if err != nil {
		return "", fmt.Errorf("打开 Cookie 数据库失败: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(
		ctx,
		`SELECT name, value FROM cookies WHERE (host_key LIKE '%weibo.com%' OR host_key LIKE '%sina.com%') AND value != ''`,
	)
	if err != nil {
		return "", fmt.Errorf("查询 Cookie 失败: %w", err)
	}
	defer rows.Close()

	var parts []string
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			continue
		}
		if name != "" && value != "" {
			parts = append(parts, name+"="+value)
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("读取 Cookie 结果失败: %w", err)
	}
	if len(parts) > 0 {
		return strings.Join(parts, "; "), nil
	}

	var encryptedCount int
	if err := db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM cookies WHERE (host_key LIKE '%weibo.com%' OR host_key LIKE '%sina.com%') AND length(encrypted_value) > 0`,
	).Scan(&encryptedCount); err != nil {
		return "", fmt.Errorf("统计加密 Cookie 失败: %w", err)
	}
	if encryptedCount > 0 {
		return "", fmt.Errorf("检测到 %d 个微博 Cookie 仅保存在 encrypted_value 中，磁盘直读不可用", encryptedCount)
	}

	return "", fmt.Errorf("Cookies 数据库中未找到微博相关 Cookie")
}

// VerifyCookies 验证 Cookie 是否有效。
func VerifyCookies(ctx context.Context, cookieStr, targetUID string) bool {
	return VerifyCookiesDetailed(ctx, cookieStr, targetUID) == nil
}

// VerifyCookiesDetailed 验证 Cookie 是否有效，并返回可区分瞬时网络错误与登录失效的详细错误。
func VerifyCookiesDetailed(ctx context.Context, cookieStr, targetUID string) error {
	return verifyCookiesWithClient(
		ctx,
		&http.Client{Timeout: defaultVerifyTimeout},
		defaultVerifyBaseURL,
		cookieStr,
		targetUID,
		defaultVerifyAttempts,
		defaultVerifyRetryDelay,
	)
}

func (e *CookieExtractor) verifyCookies(ctx context.Context, cookieStr, targetUID string) error {
	client := e.verifyClient
	if client == nil {
		client = &http.Client{Timeout: defaultVerifyTimeout}
	}
	baseURL := strings.TrimSpace(e.verifyBaseURL)
	if baseURL == "" {
		baseURL = defaultVerifyBaseURL
	}
	attempts := e.verifyAttempts
	if attempts <= 0 {
		attempts = defaultVerifyAttempts
	}
	delay := e.verifyRetryDelay
	if delay <= 0 {
		delay = defaultVerifyRetryDelay
	}
	return verifyCookiesWithClient(ctx, client, baseURL, cookieStr, targetUID, attempts, delay)
}

func verifyCookiesWithClient(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	cookieStr string,
	targetUID string,
	attempts int,
	baseDelay time.Duration,
) error {
	if attempts <= 0 {
		attempts = 1
	}
	if baseDelay <= 0 {
		baseDelay = defaultVerifyRetryDelay
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiURL := fmt.Sprintf("%s/ajax/statuses/mymblog?uid=%s&page=1&feature=0", baseURL, targetUID)

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			return &CookieVerifyError{Transient: false, Err: err}
		}
		for key, value := range buildSessionHeaders(cookieStr, targetUID) {
			req.Header.Set(key, value)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = &CookieVerifyError{Transient: true, Err: err}
			if ctx.Err() != nil || attempt == attempts {
				return lastErr
			}
			if !sleepWithContext(ctx, baseDelay<<(attempt-1)) {
				return &CookieVerifyError{Transient: true, Err: ctx.Err()}
			}
			continue
		}

		if resp.StatusCode == http.StatusRequestTimeout ||
			resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode >= http.StatusInternalServerError {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			lastErr = &CookieVerifyError{
				Transient:  true,
				StatusCode: resp.StatusCode,
				Err:        fmt.Errorf("HTTP %d", resp.StatusCode),
			}
			if attempt == attempts {
				return lastErr
			}
			if !sleepWithContext(ctx, baseDelay<<(attempt-1)) {
				return &CookieVerifyError{Transient: true, Err: ctx.Err()}
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return &CookieVerifyError{
				Transient:  false,
				StatusCode: resp.StatusCode,
				Err:        fmt.Errorf("HTTP %d", resp.StatusCode),
			}
		}

		contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
		if strings.HasPrefix(contentType, "text/html") {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return &CookieVerifyError{
				Transient:  false,
				StatusCode: resp.StatusCode,
				Err:        fmt.Errorf("微博接口返回 HTML 登录页"),
			}
		}

		var payload struct {
			OK  *int   `json:"ok"`
			URL string `json:"url"`
			Msg string `json:"msg"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&payload)
		resp.Body.Close()
		if decodeErr != nil {
			return &CookieVerifyError{
				Transient:  false,
				StatusCode: resp.StatusCode,
				Err:        fmt.Errorf("解析微博接口响应失败: %w", decodeErr),
			}
		}
		if payload.OK == nil || *payload.OK != 1 {
			return &CookieVerifyError{
				Transient:  false,
				StatusCode: resp.StatusCode,
				Err:        fmt.Errorf("微博接口返回未登录或异常状态 (ok=%s, url=%s, msg=%s)", formatOptionalInt(payload.OK), payload.URL, payload.Msg),
			}
		}

		return nil
	}

	return lastErr
}

func formatOptionalInt(v *int) string {
	if v == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d", *v)
}

func sleepWithContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func readSessionSnapshot(path string, now time.Time) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var rows []sessionCookieSnapshot
	if err := json.Unmarshal(data, &rows); err != nil {
		return "", fmt.Errorf("解析共享登录快照失败: %w", err)
	}

	order := make([]string, 0, len(rows))
	values := make(map[string]string, len(rows))
	exactDomain := make(map[string]bool, len(rows))

	for _, c := range rows {
		name := strings.TrimSpace(c.Name)
		value := strings.TrimSpace(c.Value)
		if name == "" || value == "" {
			continue
		}
		rawDomain := strings.ToLower(strings.TrimSpace(c.Domain))
		host := strings.TrimPrefix(rawDomain, ".")
		if host != "weibo.com" {
			continue
		}
		if c.Expires > 0 && time.Unix(int64(c.Expires), 0).Before(now) {
			continue
		}

		isExact := rawDomain == "weibo.com"
		if _, exists := values[name]; !exists {
			order = append(order, name)
			values[name] = value
			exactDomain[name] = isExact
			continue
		}
		if isExact || !exactDomain[name] {
			values[name] = value
			if isExact {
				exactDomain[name] = true
			}
		}
	}

	if len(order) == 0 {
		return "", fmt.Errorf("共享登录快照中没有有效的 weibo.com Cookie")
	}

	parts := make([]string, 0, len(order))
	for _, name := range order {
		parts = append(parts, name+"="+values[name])
	}
	return strings.Join(parts, "; "), nil
}

func saveSessionSnapshot(path string, cookies []playwright.Cookie) error {
	rows := make([]map[string]any, 0, len(cookies))
	for _, c := range cookies {
		host := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(c.Domain)), ".")
		if !isSessionSnapshotHost(host) {
			continue
		}
		rows = append(rows, map[string]any{"name": c.Name, "value": c.Value, "domain": c.Domain, "path": c.Path, "expires": c.Expires, "http_only": c.HttpOnly, "secure": c.Secure})
	}
	data, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func isSessionSnapshotHost(host string) bool {
	return host == "weibo.com" || strings.HasSuffix(host, ".weibo.com") ||
		host == "sina.com.cn" || strings.HasSuffix(host, ".sina.com.cn") ||
		host == "weibo.cn" || strings.HasSuffix(host, ".weibo.cn")
}
