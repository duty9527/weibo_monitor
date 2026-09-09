package groupchat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"time"

	"weibo_group_chat_monitor/config"

	playwright "github.com/mxschmitt/playwright-go"
)

type StoredCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires"`
	HTTPOnly bool    `json:"http_only"`
	Secure   bool    `json:"secure"`
	SameSite string  `json:"same_site,omitempty"`
}

type AuthSession struct {
	UID     string
	Cookies []StoredCookie
}

type authRefreshState struct {
	LastALFFingerprint string `json:"last_alf_fingerprint"`
	LastAttemptAt      string `json:"last_attempt_at"`
}

func AcquireAuthSession(ctx context.Context, cfg *config.GroupChatModeConfig, logger *slog.Logger, forceBrowser bool) (*AuthSession, error) {
	if !forceBrowser {
		cookies, err := LoadStoredCookies(cfg.Subscription.CookieCacheFile)
		if err == nil {
			if session, err := validateStoredCookies(ctx, cookies); err == nil {
				fingerprint, expiresAt, due := ALFProactiveRefreshDue(cookies, time.Now(), time.Duration(cfg.Subscription.ProactiveRefreshBeforeExpiryHours)*time.Hour)
				if due {
					claimed, claimErr := ClaimALFProactiveRefresh(cfg.Subscription.AuthRefreshStateFile, fingerprint, time.Now())
					if claimErr != nil {
						logger.Warn("记录 ALF 预刷新状态失败，将继续使用有效 Cookie", "err", claimErr)
						return session, nil
					}
					if claimed {
						logger.Info("ALF 即将过期，尝试一次无头 Playwright 预刷新", "expires_at", expiresAt.Format(time.RFC3339))
						if refreshed, refreshErr := refreshStoredCookiesHeadless(ctx, cfg, logger); refreshErr == nil {
							return refreshed, nil
						} else {
							logger.Warn("ALF 预刷新失败，将继续使用当前有效 Cookie", "err", refreshErr)
						}
					}
				}
				logger.Info("使用缓存 Cookie 建立订阅认证", "uid", session.UID, "cookie_file", cfg.Subscription.CookieCacheFile)
				return session, nil
			}
		} else if !os.IsNotExist(err) {
			logger.Warn("读取订阅 Cookie 缓存失败", "err", err)
		}
	}

	logger.Info("尝试通过无头浏览器恢复微博登录态")
	if session, err := refreshStoredCookiesHeadless(ctx, cfg, logger); err == nil {
		return session, nil
	} else {
		logger.Info("无头浏览器中没有有效登录态", "reason", err)
	}

	logger.Info("正在打开微博登录页面；请使用微博手机客户端扫码")
	cookies, err := extractCookiesWithPlaywright(ctx, cfg, logger, false, true)
	if err != nil {
		return nil, err
	}
	session, err := validateRefreshedCookies(ctx, cfg, cookies)
	if err != nil {
		return nil, err
	}
	if err := SaveStoredCookies(cfg.Subscription.CookieCacheFile, cookies); err != nil {
		return nil, err
	}
	logger.Info("扫码登录成功，Cookie 已保存，浏览器即将关闭", "uid", session.UID, "cookie_file", cfg.Subscription.CookieCacheFile)
	return session, nil
}

func refreshStoredCookiesHeadless(ctx context.Context, cfg *config.GroupChatModeConfig, logger *slog.Logger) (*AuthSession, error) {
	cookies, err := extractCookiesWithPlaywright(ctx, cfg, logger, true, false)
	if err != nil {
		return nil, err
	}
	session, err := validateRefreshedCookies(ctx, cfg, cookies)
	if err != nil {
		return nil, err
	}
	if err := SaveStoredCookies(cfg.Subscription.CookieCacheFile, cookies); err != nil {
		return nil, err
	}
	logger.Info("已从持久化浏览器会话刷新 Cookie，浏览器即将关闭", "uid", session.UID)
	return session, nil
}

func validateRefreshedCookies(ctx context.Context, cfg *config.GroupChatModeConfig, cookies []StoredCookie) (*AuthSession, error) {
	session, err := validateStoredCookies(ctx, cookies)
	if err != nil {
		return nil, err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := ValidateBayeuxHandshake(checkCtx, cookies, cfg.Subscription.TLSInsecureSkipVerify); err != nil {
		return nil, fmt.Errorf("刷新后的 Cookie 未通过 Bayeux 握手验证: %w", err)
	}
	return session, nil
}

func ValidateStoredCookies(ctx context.Context, cookies []StoredCookie) (*AuthSession, error) {
	return validateStoredCookies(ctx, cookies)
}

func ValidateBayeuxHandshake(ctx context.Context, cookies []StoredCookie, insecure bool) error {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsConfig, err := NewWeiboIMTLSConfig(insecure)
	if err != nil {
		return err
	}
	transport.TLSClientConfig = tlsConfig
	httpClient := &http.Client{Transport: transport, Jar: jar}
	client, err := NewBayeuxClient(DefaultBayeuxEndpoint, CookieHeaderForHost(cookies, "web.im.weibo.com"), httpClient)
	if err != nil {
		return err
	}
	_, err = client.Handshake(ctx)
	return err
}

func ALFProactiveRefreshDue(cookies []StoredCookie, now time.Time, before time.Duration) (string, time.Time, bool) {
	if before <= 0 {
		return "", time.Time{}, false
	}
	var expiresAt time.Time
	var domain string
	for _, cookie := range cookies {
		if !strings.EqualFold(strings.TrimSpace(cookie.Name), "ALF") || cookie.Expires <= 0 {
			continue
		}
		candidate := time.Unix(int64(cookie.Expires), 0)
		if expiresAt.IsZero() || candidate.After(expiresAt) {
			expiresAt = candidate
			domain = strings.ToLower(strings.TrimSpace(cookie.Domain))
		}
	}
	if expiresAt.IsZero() || expiresAt.After(now.Add(before)) {
		return "", expiresAt, false
	}
	fingerprint := fmt.Sprintf("ALF|%s|%d", domain, expiresAt.Unix())
	return fingerprint, expiresAt, true
}

func ClaimALFProactiveRefresh(path, fingerprint string, now time.Time) (bool, error) {
	path = strings.TrimSpace(path)
	if path == "" || fingerprint == "" {
		return false, nil
	}
	var state authRefreshState
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return false, fmt.Errorf("解析认证刷新状态失败: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("读取认证刷新状态失败: %w", err)
	}
	if state.LastALFFingerprint == fingerprint {
		return false, nil
	}
	state.LastALFFingerprint = fingerprint
	state.LastAttemptAt = now.In(time.Local).Format(time.RFC3339)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auth-refresh-*")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return false, err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return false, err
	}
	return true, nil
}

func extractCookiesWithPlaywright(
	ctx context.Context,
	cfg *config.GroupChatModeConfig,
	logger *slog.Logger,
	headless bool,
	waitForLogin bool,
) ([]StoredCookie, error) {
	runOptions := &playwright.RunOptions{
		Browsers:            []string{"chromium"},
		SkipInstallBrowsers: true,
		Verbose:             false,
		Stdout:              io.Discard,
		Stderr:              os.Stderr,
	}
	pw, err := playwright.Run(runOptions)
	if err != nil {
		if installErr := playwright.Install(runOptions); installErr != nil {
			return nil, fmt.Errorf("安装 Playwright driver 失败: %w", installErr)
		}
		pw, err = playwright.Run(runOptions)
		if err != nil {
			return nil, fmt.Errorf("启动 Playwright 失败: %w", err)
		}
	}
	defer pw.Stop()

	launchOptions := playwright.BrowserTypeLaunchPersistentContextOptions{
		Headless: playwright.Bool(headless),
		Viewport: &playwright.Size{Width: cfg.Browser.ViewportWidth, Height: cfg.Browser.ViewportHeight},
	}
	executablePath, channel := browserLaunchTarget(cfg.Browser)
	if executablePath != "" {
		launchOptions.ExecutablePath = playwright.String(executablePath)
		logger.Info("使用系统浏览器启动 Playwright", "executable", executablePath)
	} else if channel != "" {
		launchOptions.Channel = playwright.String(channel)
	}
	browser, err := pw.Chromium.LaunchPersistentContext(cfg.Browser.UserDataDir, launchOptions)
	if err != nil {
		return nil, fmt.Errorf("启动持久化浏览器失败: %w", err)
	}
	defer browser.Close()

	pages := browser.Pages()
	var page playwright.Page
	if len(pages) > 0 {
		page = pages[0]
	} else {
		page, err = browser.NewPage()
		if err != nil {
			return nil, fmt.Errorf("创建登录页面失败: %w", err)
		}
	}
	if !headless {
		_ = page.BringToFront()
	}
	if _, err := page.Goto(cfg.Chat.URL); err != nil {
		logger.Warn("打开微博群聊登录页返回错误，将继续检查登录态", "err", err)
	}

	deadline := time.Now().Add(time.Duration(cfg.Subscription.LoginTimeoutSeconds) * time.Second)
	for {
		cookies, cookieErr := browser.Cookies()
		if cookieErr == nil {
			stored := convertPlaywrightCookies(cookies)
			checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, checkErr := QueryCurrentUID(checkCtx, CookieHeaderForHost(stored, "api.weibo.com"), http.DefaultClient)
			cancel()
			if checkErr == nil {
				return stored, nil
			}
		}

		if !waitForLogin {
			return nil, fmt.Errorf("持久化浏览器中暂无有效微博登录态")
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等待扫码登录超时（%d 秒）", cfg.Subscription.LoginTimeoutSeconds)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func QueryCurrentUID(ctx context.Context, cookieHeader string, client *http.Client) (string, error) {
	if strings.TrimSpace(cookieHeader) == "" {
		return "", fmt.Errorf("微博 Cookie 为空")
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.weibo.com/webim/query_primary_info.json", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Cookie", cookieHeader)
	req.Header.Set("Referer", "https://api.weibo.com/chat/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/140 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("查询当前微博 UID 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("查询当前微博 UID 返回 HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Profile struct {
			ID json.RawMessage `json:"id"`
		} `json:"profile"`
		Error     string `json:"error"`
		ErrorCode int    `json:"error_code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("解析当前微博 UID 失败: %w", err)
	}
	if payload.Error != "" {
		return "", fmt.Errorf("查询当前微博 UID 失败: %s (%d)", payload.Error, payload.ErrorCode)
	}
	uid := strings.Trim(string(payload.Profile.ID), `"`)
	if uid == "" || uid == "null" {
		return "", fmt.Errorf("当前用户响应中缺少 profile.id")
	}
	return uid, nil
}

func LoadStoredCookies(path string) ([]StoredCookie, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cookies []StoredCookie
	if err := json.Unmarshal(data, &cookies); err != nil {
		return nil, fmt.Errorf("解析 Cookie 缓存失败: %w", err)
	}
	if len(cookies) == 0 {
		return nil, fmt.Errorf("Cookie 缓存为空")
	}
	return cookies, nil
}

func SaveStoredCookies(path string, cookies []StoredCookie) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建 Cookie 缓存目录失败: %w", err)
	}
	data, err := json.MarshalIndent(cookies, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 Cookie 缓存失败: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".groupchat-cookies-*")
	if err != nil {
		return fmt.Errorf("创建 Cookie 临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("更新 Cookie 缓存失败: %w", err)
	}
	return nil
}

func CookieHeaderForHost(cookies []StoredCookie, host string) string {
	now := float64(time.Now().Unix())
	parts := make([]string, 0, len(cookies))
	seen := make(map[string]struct{}, len(cookies))
	for _, cookie := range cookies {
		domain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(cookie.Domain)), ".")
		target := strings.ToLower(strings.TrimSpace(host))
		if domain != "" && target != domain && !strings.HasSuffix(target, "."+domain) {
			continue
		}
		if cookie.Expires > 0 && cookie.Expires < now {
			continue
		}
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

func validateStoredCookies(ctx context.Context, cookies []StoredCookie) (*AuthSession, error) {
	uid, err := QueryCurrentUID(ctx, CookieHeaderForHost(cookies, "api.weibo.com"), http.DefaultClient)
	if err != nil {
		return nil, err
	}
	return &AuthSession{UID: uid, Cookies: cookies}, nil
}

func convertPlaywrightCookies(cookies []playwright.Cookie) []StoredCookie {
	stored := make([]StoredCookie, 0, len(cookies))
	for _, cookie := range cookies {
		domain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(cookie.Domain)), ".")
		if domain != "weibo.com" && !strings.HasSuffix(domain, ".weibo.com") &&
			domain != "sina.com" && !strings.HasSuffix(domain, ".sina.com") &&
			domain != "sina.com.cn" && !strings.HasSuffix(domain, ".sina.com.cn") {
			continue
		}
		sameSite := ""
		if cookie.SameSite != nil {
			sameSite = string(*cookie.SameSite)
		}
		stored = append(stored, StoredCookie{
			Name: cookie.Name, Value: cookie.Value, Domain: cookie.Domain, Path: cookie.Path,
			Expires: cookie.Expires, HTTPOnly: cookie.HttpOnly, Secure: cookie.Secure, SameSite: sameSite,
		})
	}
	return stored
}
