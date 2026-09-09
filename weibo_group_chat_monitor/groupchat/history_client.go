package groupchat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"weibo_group_chat_monitor/config"
)

type GroupHistoryClient struct {
	cfg        *config.GroupChatModeConfig
	cookies    []StoredCookie
	httpClient *http.Client
}

func NewGroupHistoryClient(cfg *config.GroupChatModeConfig, cookies []StoredCookie) *GroupHistoryClient {
	timeout := time.Duration(cfg.Chat.HistoryFetchTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &GroupHistoryClient{
		cfg:        cfg,
		cookies:    append([]StoredCookie(nil), cookies...),
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (c *GroupHistoryClient) Fetch(ctx context.Context, maxMID string) ([]ChatMessage, error) {
	if c == nil || c.cfg == nil {
		return nil, fmt.Errorf("群历史客户端配置为空")
	}
	endpoint, err := url.Parse(c.cfg.Chat.APIURLBase)
	if err != nil {
		return nil, fmt.Errorf("解析群历史接口失败: %w", err)
	}
	query := endpoint.Query()
	query.Set("id", c.cfg.Chat.GroupID)
	query.Set("count", strconv.Itoa(c.cfg.Chat.BatchSize))
	query.Set("convert_emoji", "1")
	query.Set("query_sender", "1")
	query.Set("source", c.cfg.Chat.Source)
	if maxMID = strings.TrimSpace(maxMID); maxMID != "" {
		query.Set("max_mid", maxMID)
	}
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", CookieHeaderForHost(c.cookies, endpoint.Hostname()))
	req.Header.Set("Referer", "https://api.weibo.com/chat/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/140 Safari/537.36")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求群历史接口失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("群历史接口返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload ChatAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("解析群历史响应失败: %w", err)
	}
	if strings.TrimSpace(payload.Error) != "" {
		return nil, fmt.Errorf("群历史接口返回错误: %s (%d)", payload.Error, payload.ErrorCode.Int64())
	}
	return payload.Messages, nil
}
