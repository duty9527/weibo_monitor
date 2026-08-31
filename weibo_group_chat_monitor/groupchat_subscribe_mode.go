package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"weibo_group_chat_monitor/config"
	"weibo_group_chat_monitor/groupchat"
)

type loggedSubscriptionEvent struct {
	ReceivedAt string          `json:"received_at"`
	Channel    string          `json:"channel"`
	Data       json.RawMessage `json:"data"`
}

func runGroupChatSubscribeMode(args []string) int {
	fs := flag.NewFlagSet("groupchat-subscribe", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", defaultGroupChatConfigPath(), "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.LoadGroupChat(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		return 1
	}
	logger := newLogger(cfg.Log.Level)
	if cfg.Subscription.TLSInsecureSkipVerify {
		logger.Warn("订阅 TLS 证书校验已关闭；此设置仅应用于 web.im.weibo.com 测试连接")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	processor, err := groupchat.NewSubscriptionEventProcessor(cfg)
	if err != nil {
		logger.Error("初始化订阅事件处理器失败", "err", err)
		return 1
	}

	session, err := groupchat.AcquireAuthSession(ctx, cfg, logger, false)
	if err != nil {
		logger.Error("获取微博订阅认证失败", "err", err)
		return 1
	}
	logger.Info("群聊订阅模式已启动", "uid", session.UID, "event_log", cfg.Subscription.EventLogFile)

	for ctx.Err() == nil {
		err = runSubscriptionSession(ctx, cfg, session, processor, logger)
		if ctx.Err() != nil {
			break
		}
		if errors.Is(err, groupchat.ErrBayeuxReconnectNone) {
			logger.Info("微博服务端要求停止订阅重连")
			break
		}
		logger.Warn("微博订阅连接中断", "err", err)

		if isSubscriptionAuthError(err) {
			logger.Warn("订阅认证失效，将临时启动浏览器刷新登录态")
			refreshedSession, refreshErr := groupchat.AcquireAuthSession(ctx, cfg, logger, true)
			if refreshErr != nil {
				logger.Error("刷新微博登录态失败", "err", refreshErr)
				if err := waitForRetry(ctx, time.Duration(cfg.Subscription.RetryDelaySeconds)*time.Second); err != nil {
					break
				}
				continue
			}
			session = refreshedSession
		}

		if err := waitForRetry(ctx, time.Duration(cfg.Subscription.RetryDelaySeconds)*time.Second); err != nil {
			break
		}
	}

	logger.Info("群聊订阅模式已停止")
	return 0
}

func runSubscriptionSession(ctx context.Context, cfg *config.GroupChatModeConfig, session *groupchat.AuthSession, processor *groupchat.SubscriptionEventProcessor, logger interface {
	Info(string, ...any)
}) error {
	if session == nil {
		return fmt.Errorf("微博订阅认证会话为空")
	}
	processor.SetMediaCookies(session.Cookies)
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsConfig, err := groupchat.NewWeiboIMTLSConfig(cfg.Subscription.TLSInsecureSkipVerify)
	if err != nil {
		return err
	}
	transport.TLSClientConfig = tlsConfig
	httpClient := &http.Client{Transport: transport, Jar: jar}
	cookieHeader := groupchat.CookieHeaderForHost(session.Cookies, "web.im.weibo.com")
	client, err := groupchat.NewBayeuxClient(groupchat.DefaultBayeuxEndpoint, cookieHeader, httpClient)
	if err != nil {
		return err
	}
	channel := "/im/" + session.UID
	return client.ListenWithError(ctx, channel, func(message groupchat.BayeuxMessage) error {
		event := loggedSubscriptionEvent{
			ReceivedAt: time.Now().Format(time.RFC3339Nano),
			Channel:    message.Channel,
			Data:       message.Data,
		}
		if err := appendSubscriptionEvent(cfg.Subscription.EventLogFile, event); err != nil {
			logger.Info("写入订阅事件失败", "err", err)
			return fmt.Errorf("写入订阅事件失败: %w", err)
		}
		result, err := processor.Process(message.Data)
		if err != nil {
			logger.Info("处理微博实时事件失败", "err", err)
			return err
		}
		switch {
		case result.Duplicate:
			logger.Info("忽略重复微博实时事件", "kind", result.Kind, "message_id", result.MessageID)
		case result.Ignored:
			logger.Info("忽略非目标微博实时事件", "kind", result.Kind)
		case result.Kind == "recall":
			logger.Info("处理微博消息撤回", "ids", result.RecalledIDs, "updated", result.UpdatedCount)
		default:
			logger.Info("保存微博实时消息", "message_id", result.MessageID, "media_count", len(result.MediaPaths), "media_errors", len(result.MediaErrors))
		}
		return nil
	})
}

func appendSubscriptionEvent(path string, event loggedSubscriptionEvent) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func isSubscriptionAuthError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "auth fail") ||
		strings.Contains(message, "create_denied") ||
		strings.Contains(message, "403")
}

func waitForRetry(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
