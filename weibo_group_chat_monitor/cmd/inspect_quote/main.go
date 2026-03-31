package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"weibo_group_chat_monitor/config"

	playwright "github.com/playwright-community/playwright-go"
)

const quoteSeparator = "\n- - - - - - - - - - - - - - -\n"

type responseEnvelope struct {
	Messages []json.RawMessage `json:"messages"`
}

type messageSummary struct {
	ID      flexibleString `json:"id"`
	Text    string         `json:"text"`
	Content string         `json:"content"`
}

type flexibleString string

func (s *flexibleString) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*s = ""
		return nil
	}

	if data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*s = flexibleString(value)
		return nil
	}

	*s = flexibleString(string(data))
	return nil
}

func (s flexibleString) String() string {
	return strings.TrimSpace(string(s))
}

type fetchResult struct {
	OK    bool             `json:"ok"`
	Error string           `json:"error"`
	Data  responseEnvelope `json:"data"`
}

func main() {
	configPath := flag.String("config", "config.groupchat.yaml", "配置文件路径")
	targetID := flag.String("id", "5282330998475900", "目标消息 ID")
	maxPages := flag.Int("max-pages", 200, "最多翻取页数")
	flag.Parse()

	cfg, err := config.LoadGroupChat(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	runOptions := &playwright.RunOptions{
		Browsers: []string{"chromium"},
		Verbose:  false,
		Stdout:   io.Discard,
		Stderr:   os.Stderr,
	}

	pw, err := playwright.Run(runOptions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "启动 Playwright 失败: %v\n", err)
		os.Exit(1)
	}
	defer pw.Stop()

	browser, err := launchBrowserWithFallback(pw, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "启动浏览器失败: %v\n", err)
		os.Exit(1)
	}
	defer browser.Close()

	page, err := ensureContextPage(browser)
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建页面失败: %v\n", err)
		os.Exit(1)
	}

	timeout := float64(cfg.Browser.InitialLoadTimeoutSeconds * 1000)
	response, err := page.ExpectResponse(func(url string) bool {
		return strings.Contains(url, cfg.Chat.APIURLBase)
	}, func() error {
		_, err := page.Goto(cfg.Chat.URL)
		return err
	}, playwright.PageExpectResponseOptions{Timeout: &timeout})
	if err != nil {
		fmt.Fprintf(os.Stderr, "等待初始响应失败: %v\n", err)
		os.Exit(1)
	}

	body, err := response.Body()
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取初始响应失败: %v\n", err)
		os.Exit(1)
	}

	var envelope responseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		fmt.Fprintf(os.Stderr, "解析初始响应失败: %v\n", err)
		os.Exit(1)
	}

	if raw, found := locateMessage(envelope.Messages, *targetID); found {
		printRaw(raw)
		return
	}

	maxMID := oldestMessageID(envelope.Messages)
	for pageIndex := 1; pageIndex <= *maxPages && maxMID != ""; pageIndex++ {
		messages, err := fetchHistoryBatch(page, cfg, maxMID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "抓取第 %d 页历史失败: %v\n", pageIndex, err)
			os.Exit(1)
		}
		if len(messages) == 0 {
			break
		}

		filtered := make([]json.RawMessage, 0, len(messages))
		for _, raw := range messages {
			var summary messageSummary
			if err := json.Unmarshal(raw, &summary); err == nil && summary.ID.String() == strings.TrimSpace(maxMID) {
				continue
			}
			filtered = append(filtered, raw)
		}
		if raw, found := locateMessage(filtered, *targetID); found {
			printRaw(raw)
			return
		}

		maxMID = oldestMessageID(filtered)
		time.Sleep(time.Duration(cfg.Chat.HistoryIntervalMilliseconds) * time.Millisecond)
	}

	fmt.Fprintf(os.Stderr, "未找到目标消息: %s\n", *targetID)
	os.Exit(1)
}

func locateMessage(messages []json.RawMessage, targetID string) (json.RawMessage, bool) {
	for _, raw := range messages {
		var summary messageSummary
		if err := json.Unmarshal(raw, &summary); err != nil {
			continue
		}
		if summary.ID.String() == strings.TrimSpace(targetID) {
			return raw, true
		}
	}
	return nil, false
}

func oldestMessageID(messages []json.RawMessage) string {
	if len(messages) == 0 {
		return ""
	}

	type pair struct {
		ID    string
		Value int64
		Valid bool
	}

	pairs := make([]pair, 0, len(messages))
	for _, raw := range messages {
		var summary messageSummary
		if err := json.Unmarshal(raw, &summary); err != nil {
			continue
		}
		id := summary.ID.String()
		value, err := strconv.ParseInt(id, 10, 64)
		pairs = append(pairs, pair{ID: id, Value: value, Valid: err == nil})
	}
	if len(pairs) == 0 {
		return ""
	}

	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Valid && pairs[j].Valid {
			return pairs[i].Value < pairs[j].Value
		}
		return pairs[i].ID < pairs[j].ID
	})
	return strings.TrimSpace(pairs[0].ID)
}

func fetchHistoryBatch(page playwright.Page, cfg *config.GroupChatModeConfig, maxMID string) ([]json.RawMessage, error) {
	fetchURL := fmt.Sprintf(
		"%s?id=%s&count=%d&convert_emoji=1&query_sender=1&source=%s&max_mid=%s",
		cfg.Chat.APIURLBase,
		cfg.Chat.GroupID,
		cfg.Chat.BatchSize,
		cfg.Chat.Source,
		maxMID,
	)

	var result fetchResult
	value, err := page.Evaluate(`
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
		"timeoutMs": cfg.Chat.HistoryFetchTimeoutSeconds * 1000,
	})
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, fmt.Errorf("%s", result.Error)
	}
	return result.Data.Messages, nil
}

func ensureContextPage(browser playwright.BrowserContext) (playwright.Page, error) {
	pages := browser.Pages()
	if len(pages) > 0 {
		return pages[0], nil
	}
	return browser.NewPage()
}

func launchBrowserWithFallback(pw *playwright.Playwright, cfg *config.GroupChatModeConfig) (playwright.BrowserContext, error) {
	options := playwright.BrowserTypeLaunchPersistentContextOptions{
		AcceptDownloads: playwright.Bool(true),
		Headless:        playwright.Bool(cfg.Browser.Headless),
		Viewport: &playwright.Size{
			Width:  cfg.Browser.ViewportWidth,
			Height: cfg.Browser.ViewportHeight,
		},
	}

	if channel := strings.TrimSpace(cfg.Browser.BrowserChannel); channel != "" {
		options.Channel = playwright.String(channel)
		browser, err := pw.Chromium.LaunchPersistentContext(cfg.Browser.UserDataDir, options)
		if err == nil {
			return browser, nil
		}
		fmt.Fprintf(os.Stderr, "使用 channel=%s 启动失败，回退到 Playwright Chromium: %v\n", channel, err)
		options.Channel = nil
	}

	return pw.Chromium.LaunchPersistentContext(cfg.Browser.UserDataDir, options)
}

func printRaw(raw json.RawMessage) {
	var pretty any
	if err := json.Unmarshal(raw, &pretty); err != nil {
		fmt.Println(string(raw))
		return
	}

	formatted, err := json.MarshalIndent(pretty, "", "  ")
	if err != nil {
		fmt.Println(string(raw))
		return
	}
	fmt.Println(string(formatted))
}
