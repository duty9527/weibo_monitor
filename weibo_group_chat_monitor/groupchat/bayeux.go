package groupchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const DefaultBayeuxEndpoint = "https://web.im.weibo.com/im"

var ErrBayeuxReconnectNone = errors.New("服务端要求停止 Bayeux 重连")

type BayeuxClient struct {
	endpoint       string
	httpClient     *http.Client
	origin         string
	clientID       string
	messageID      int64
	connectionType string
	ack            json.RawMessage
}

type BayeuxMessage struct {
	Channel                  string          `json:"channel"`
	ClientID                 string          `json:"clientId,omitempty"`
	Version                  string          `json:"version,omitempty"`
	MinimumVersion           string          `json:"minimumVersion,omitempty"`
	SupportedConnectionTypes []string        `json:"supportedConnectionTypes,omitempty"`
	ConnectionType           string          `json:"connectionType,omitempty"`
	Subscription             string          `json:"subscription,omitempty"`
	Successful               bool            `json:"successful,omitempty"`
	Error                    string          `json:"error,omitempty"`
	Data                     json.RawMessage `json:"data,omitempty"`
	Advice                   *BayeuxAdvice   `json:"advice,omitempty"`
	Ext                      json.RawMessage `json:"ext,omitempty"`
	ID                       string          `json:"id,omitempty"`
}

type BayeuxAdvice struct {
	Reconnect string `json:"reconnect,omitempty"`
	Interval  int    `json:"interval,omitempty"`
	Timeout   int    `json:"timeout,omitempty"`
}

func NewBayeuxClient(endpoint, cookieHeader string, httpClient *http.Client) (*BayeuxClient, error) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	endpointURL, err := url.Parse(endpoint)
	if err != nil || endpointURL.Scheme == "" || endpointURL.Host == "" {
		return nil, fmt.Errorf("Bayeux endpoint 非法: %q", endpoint)
	}

	if httpClient == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, fmt.Errorf("创建 Cookie Jar 失败: %w", err)
		}
		httpClient = &http.Client{Jar: jar}
	} else if httpClient.Jar == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, fmt.Errorf("创建 Cookie Jar 失败: %w", err)
		}
		httpClient.Jar = jar
	}

	if cookies := parseCookieHeader(cookieHeader); len(cookies) > 0 {
		httpClient.Jar.SetCookies(endpointURL, cookies)
	}

	return &BayeuxClient{
		endpoint:       endpoint,
		httpClient:     httpClient,
		origin:         "https://api.weibo.com",
		connectionType: "long-polling",
	}, nil
}

func (c *BayeuxClient) Handshake(ctx context.Context) (*BayeuxMessage, error) {
	request := BayeuxMessage{
		Channel:                  "/meta/handshake",
		Version:                  "1.0",
		MinimumVersion:           "1.0",
		SupportedConnectionTypes: []string{"long-polling"},
		Ext:                      json.RawMessage(`{"ack":true}`),
		ID:                       c.nextMessageID(),
	}
	messages, err := c.post(ctx, "handshake", []BayeuxMessage{request})
	if err != nil {
		return nil, err
	}
	reply := findBayeuxMessage(messages, "/meta/handshake")
	if reply == nil {
		return nil, fmt.Errorf("握手响应缺少 /meta/handshake")
	}
	if !reply.Successful || strings.TrimSpace(reply.ClientID) == "" {
		return nil, fmt.Errorf("Bayeux 握手失败: %s", reply.Error)
	}
	c.clientID = reply.ClientID
	c.ack = initialAckValue(reply.Ext)
	return reply, nil
}

func (c *BayeuxClient) Subscribe(ctx context.Context, channel string) error {
	if c.clientID == "" {
		return fmt.Errorf("订阅前必须先完成握手")
	}
	request := BayeuxMessage{
		Channel:      "/meta/subscribe",
		ClientID:     c.clientID,
		Subscription: channel,
		ID:           c.nextMessageID(),
	}
	messages, err := c.post(ctx, "subscribe", []BayeuxMessage{request})
	if err != nil {
		return err
	}
	reply := findBayeuxMessage(messages, "/meta/subscribe")
	if reply == nil {
		return fmt.Errorf("订阅响应缺少 /meta/subscribe")
	}
	if !reply.Successful {
		return fmt.Errorf("订阅 %s 失败: %s", channel, reply.Error)
	}
	return nil
}

func (c *BayeuxClient) Connect(ctx context.Context) ([]BayeuxMessage, error) {
	if c.clientID == "" {
		return nil, fmt.Errorf("连接前必须先完成握手")
	}
	request := BayeuxMessage{
		Channel:        "/meta/connect",
		ClientID:       c.clientID,
		ConnectionType: c.connectionType,
		ID:             c.nextMessageID(),
	}
	if len(c.ack) > 0 {
		request.Ext = buildAckExtension(c.ack)
	}
	messages, err := c.post(ctx, "connect", []BayeuxMessage{request})
	if err != nil {
		return nil, err
	}
	reply := findBayeuxMessage(messages, "/meta/connect")
	if reply == nil {
		return nil, fmt.Errorf("连接响应缺少 /meta/connect")
	}
	if ack := ackValue(reply.Ext); len(ack) > 0 {
		c.ack = ack
	}
	if !reply.Successful {
		return messages, fmt.Errorf("Bayeux connect 失败: %s", reply.Error)
	}
	return messages, nil
}

func (c *BayeuxClient) Listen(ctx context.Context, channel string, onMessage func(BayeuxMessage)) error {
	return c.ListenWithError(ctx, channel, func(message BayeuxMessage) error {
		if onMessage != nil {
			onMessage(message)
		}
		return nil
	})
}

func (c *BayeuxClient) ListenWithError(ctx context.Context, channel string, onMessage func(BayeuxMessage) error) error {
	if _, err := c.Handshake(ctx); err != nil {
		return err
	}
	if err := c.Subscribe(ctx, channel); err != nil {
		return err
	}
	return c.receive(ctx, channel, onMessage)
}

// Receive keeps the Bayeux connect request open and dispatches messages for an
// already subscribed channel. Handshake and Subscribe must be called first.
func (c *BayeuxClient) Receive(ctx context.Context, channel string, onMessage func(BayeuxMessage)) error {
	return c.receive(ctx, channel, func(message BayeuxMessage) error {
		if onMessage != nil {
			onMessage(message)
		}
		return nil
	})
}

func (c *BayeuxClient) receive(ctx context.Context, channel string, onMessage func(BayeuxMessage) error) error {
	if c.clientID == "" {
		return fmt.Errorf("接收消息前必须先完成握手和订阅")
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		messages, err := c.Connect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if reply := findBayeuxMessage(messages, "/meta/connect"); reply != nil && reply.Advice != nil {
				if handled, handleErr := c.handleReconnectAdvice(ctx, channel, reply.Advice); handled {
					if handleErr != nil {
						return handleErr
					}
					continue
				}
			}
			return err
		}

		var advice *BayeuxAdvice
		for _, message := range messages {
			if message.Channel == "/meta/connect" {
				advice = message.Advice
				continue
			}
			if message.Channel == channel && len(message.Data) > 0 && onMessage != nil {
				if err := onMessage(message); err != nil {
					return err
				}
			}
		}
		if ctx.Err() != nil {
			return nil
		}

		if handled, err := c.handleReconnectAdvice(ctx, channel, advice); handled {
			if err != nil {
				return err
			}
			continue
		}
		interval := time.Duration(0)
		if advice != nil && advice.Interval > 0 {
			interval = time.Duration(advice.Interval) * time.Millisecond
		}
		if err := sleepContext(ctx, interval); err != nil {
			return nil
		}
	}
}

func (c *BayeuxClient) handleReconnectAdvice(ctx context.Context, channel string, advice *BayeuxAdvice) (bool, error) {
	if advice == nil {
		return false, nil
	}
	interval := time.Duration(advice.Interval) * time.Millisecond
	switch strings.ToLower(strings.TrimSpace(advice.Reconnect)) {
	case "none":
		return true, ErrBayeuxReconnectNone
	case "handshake":
		if err := sleepContext(ctx, interval); err != nil {
			return true, nil
		}
		if _, err := c.Handshake(ctx); err != nil {
			return true, err
		}
		if err := c.Subscribe(ctx, channel); err != nil {
			return true, err
		}
		return true, nil
	case "retry":
		if err := sleepContext(ctx, interval); err != nil {
			return true, nil
		}
		return true, nil
	default:
		return false, nil
	}
}

func (c *BayeuxClient) nextMessageID() string {
	c.messageID++
	return strconv.FormatInt(c.messageID, 10)
}

func (c *BayeuxClient) post(ctx context.Context, action string, payload []BayeuxMessage) ([]BayeuxMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("编码 Bayeux %s 请求失败: %w", action, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/"+action, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("创建 Bayeux %s 请求失败: %w", action, err)
	}
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", c.origin)
	req.Header.Set("Referer", c.origin+"/chat/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/140 Safari/537.36")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("执行 Bayeux %s 请求失败: %w", action, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Bayeux %s 返回 HTTP %d", action, resp.StatusCode)
	}

	var messages []BayeuxMessage
	if err := json.NewDecoder(resp.Body).Decode(&messages); err != nil {
		return nil, fmt.Errorf("解析 Bayeux %s 响应失败: %w", action, err)
	}
	return messages, nil
}

func findBayeuxMessage(messages []BayeuxMessage, channel string) *BayeuxMessage {
	for i := range messages {
		if messages[i].Channel == channel {
			return &messages[i]
		}
	}
	return nil
}

func initialAckValue(ext json.RawMessage) json.RawMessage {
	var value struct {
		Ack json.RawMessage `json:"ack"`
	}
	if len(ext) == 0 || json.Unmarshal(ext, &value) != nil || string(value.Ack) != "true" {
		return nil
	}
	return json.RawMessage("0")
}

func ackValue(ext json.RawMessage) json.RawMessage {
	var value struct {
		Ack json.RawMessage `json:"ack"`
	}
	if len(ext) == 0 || json.Unmarshal(ext, &value) != nil || len(value.Ack) == 0 {
		return nil
	}
	if value.Ack[0] < '0' || value.Ack[0] > '9' {
		return nil
	}
	return append(json.RawMessage(nil), value.Ack...)
}

func buildAckExtension(ack json.RawMessage) json.RawMessage {
	return json.RawMessage(`{"ack":` + string(ack) + `}`)
}

func parseCookieHeader(header string) []*http.Cookie {
	parts := strings.Split(header, ";")
	cookies := make([]*http.Cookie, 0, len(parts))
	for _, part := range parts {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || name == "" || value == "" {
			continue
		}
		cookies = append(cookies, &http.Cookie{Name: name, Value: value, Path: "/"})
	}
	return cookies
}
