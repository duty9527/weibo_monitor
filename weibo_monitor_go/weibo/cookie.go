package weibo

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	playwright "github.com/playwright-community/playwright-go"

	"weibo_monitor/config"
)

// CookieExtractor 从浏览器用户数据目录提取微博 Cookie。
type CookieExtractor struct {
	userDataDir string
	logger      *slog.Logger
}

// NewCookieExtractor 创建提取器。
func NewCookieExtractor(userDataDir string, logger *slog.Logger) *CookieExtractor {
	return &CookieExtractor{userDataDir: userDataDir, logger: logger}
}

// ExtractOrLogin 优先尝试直接读取 Cookie，失败后使用纯 Go Playwright 打开持久化上下文读取/等待登录。
func (e *CookieExtractor) ExtractOrLogin(ctx context.Context, cfg config.WeiboConfig) (string, error) {
	cookieStr, err := e.Extract(ctx)
	if err == nil && cookieStr != "" && VerifyCookies(ctx, cookieStr, cfg.TargetUID) {
		e.logger.Info("通过本地存储直接读取到有效 Cookie")
		return cookieStr, nil
	}
	if err != nil {
		e.logger.Info("本地直读未拿到可用 Cookie，切换到 Playwright 持久化上下文", "reason", err)
	} else {
		e.logger.Info("本地直读拿到的 Cookie 校验未通过，切换到 Playwright 持久化上下文")
	}

	return e.ExtractViaPlaywright(ctx, cfg)
}

// Extract 只尝试从磁盘上的浏览器存储读取 Cookie，不触发浏览器自动化。
func (e *CookieExtractor) Extract(ctx context.Context) (string, error) {
	e.logger.Info("正在从浏览器用户数据目录提取 Cookie...")

	cookieStr, err := e.readChromiumCookies(ctx)
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
	if cookieStr, err := e.extractViaPlaywrightSession(ctx, cfg, true, false); err == nil && cookieStr != "" {
		return cookieStr, nil
	} else if err != nil {
		e.logger.Warn("Playwright 无头模式未拿到有效 Cookie，将切换为可交互模式", "err", err)
	}

	return e.extractViaPlaywrightSession(ctx, cfg, false, true)
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

	if cookieStr, err := e.readValidCookiesFromContext(ctx, browserContext, cfg); err == nil && cookieStr != "" {
		return cookieStr, nil
	}

	if !waitForLogin {
		return "", fmt.Errorf("持久化上下文中暂无有效微博 Cookie")
	}

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
		e.logger.Warn("导航到微博登录页失败，但将继续等待用户登录", "url", loginURL, "err", err)
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
	if !VerifyCookies(ctx, cookieStr, cfg.TargetUID) {
		return "", fmt.Errorf("Playwright context 中的微博 Cookie 尚未生效，当前已读到 Cookie: %s", joinCookieNames(cookies))
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
	client := &http.Client{Timeout: 10 * time.Second}
	apiURL := fmt.Sprintf("https://weibo.com/ajax/statuses/mymblog?uid=%s&page=1&feature=0", targetUID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return false
	}
	for key, value := range buildSessionHeaders(cookieStr, targetUID) {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
