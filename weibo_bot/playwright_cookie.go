package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/playwright-community/playwright-go"
)

type CookieProvider struct {
	mu sync.Mutex
	pw *playwright.Playwright
}

func NewCookieProvider() *CookieProvider {
	return &CookieProvider{}
}

func (p *CookieProvider) Header(ctx context.Context, cfg Config) (string, error) {
	if strings.ToLower(strings.TrimSpace(cfg.Weibo.CookieSource)) == "static" {
		return cfg.staticWeiboCookie()
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	pw, err := p.ensurePlaywright()
	if err != nil {
		return "", err
	}

	browserContext, err := pw.Chromium.LaunchPersistentContext(
		cfg.Weibo.UserDataDir,
		playwright.BrowserTypeLaunchPersistentContextOptions{
			Headless:  playwright.Bool(cfg.Weibo.PlaywrightHeadless),
			UserAgent: playwright.String(cfg.Weibo.UserAgent),
			Viewport: &playwright.Size{
				Width:  1280,
				Height: 800,
			},
			Timeout: playwright.Float(float64(cfg.Weibo.PlaywrightTimeoutSeconds * 1000)),
		},
	)
	if err != nil {
		return "", fmt.Errorf("启动 Playwright 浏览器失败: %w", err)
	}
	defer browserContext.Close()

	page, err := ensurePage(browserContext)
	if err != nil {
		return "", err
	}

	if _, err := page.Goto(
		cfg.Weibo.CookieWarmupURL,
		playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateLoad,
			Timeout:   playwright.Float(float64(cfg.Weibo.PlaywrightTimeoutSeconds * 1000)),
		},
	); err != nil {
		return "", fmt.Errorf("访问微博首页失败: %w", err)
	}
	if cfg.Weibo.CookieWaitMillis > 0 {
		page.WaitForTimeout(float64(cfg.Weibo.CookieWaitMillis))
	}

	cookies, err := browserContext.Cookies("https://weibo.com/", "https://m.weibo.cn/", "https://weibo.cn/")
	if err != nil {
		return "", fmt.Errorf("读取微博 Cookie 失败: %w", err)
	}

	header := serializeCookies(cookies)
	if header == "" {
		return "", fmt.Errorf("未从 Playwright 浏览器会话中读取到微博 Cookie")
	}
	return header, nil
}

func (p *CookieProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pw == nil {
		return nil
	}
	err := p.pw.Stop()
	p.pw = nil
	return err
}

func (p *CookieProvider) ensurePlaywright() (*playwright.Playwright, error) {
	if p.pw != nil {
		return p.pw, nil
	}
	pw, err := playwright.Run()
	if err != nil {
		return nil, fmt.Errorf(
			"启动 Playwright 失败: %w。请先执行 `go run github.com/playwright-community/playwright-go/cmd/playwright install chromium` 安装运行时",
			err,
		)
	}
	p.pw = pw
	return p.pw, nil
}

func ensurePage(browserContext playwright.BrowserContext) (playwright.Page, error) {
	pages := browserContext.Pages()
	if len(pages) > 0 {
		return pages[0], nil
	}
	page, err := browserContext.NewPage()
	if err != nil {
		return nil, fmt.Errorf("创建 Playwright 页面失败: %w", err)
	}
	return page, nil
}

func serializeCookies(cookies []playwright.Cookie) string {
	if len(cookies) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(cookies))
	seen := make(map[string]struct{}, len(cookies))
	for _, cookie := range cookies {
		name := strings.TrimSpace(cookie.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		pairs = append(pairs, name+"="+cookie.Value)
	}
	return strings.Join(pairs, "; ")
}
