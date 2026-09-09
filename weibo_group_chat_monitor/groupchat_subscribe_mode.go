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
	"sync"
	"syscall"
	"time"

	"weibo_group_chat_monitor/config"
	"weibo_group_chat_monitor/groupchat"
	"weibo_group_chat_monitor/telegram"
)

type loggedSubscriptionEvent struct {
	ReceivedAt string          `json:"received_at"`
	Channel    string          `json:"channel"`
	Data       json.RawMessage `json:"data"`
}

type subscriptionEventLogger struct {
	dir             string
	retentionDays   int
	mu              sync.Mutex
	lastCleanupDate string
}

var (
	errProactiveCookieRefresh = errors.New("ALF 临期预刷新")
	errConfirmedAuthInvalid   = errors.New("微博登录态验证失败")
)

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
	notifier := telegram.NewClient(cfg.Telegram, logger)
	if notifier.Enabled() {
		outbox, err := groupchat.NewNotificationOutbox(cfg.Subscription.NotificationQueueFile)
		if err != nil {
			logger.Error("初始化群聊实时推送队列失败", "err", err)
			return 1
		}
		processor.SetMessageHook(func(record groupchat.OutputRecord) error {
			if !groupchat.MatchesTargetSender(record.Sender, record.SenderUID, cfg.Filters.TargetSenders, cfg.Filters.TargetSenderUIDs) {
				return nil
			}
			queued, err := outbox.Enqueue(record)
			if err != nil {
				return err
			}
			if queued {
				logger.Info("群聊消息已加入 Telegram 推送队列", "message_id", record.ID, "sender", record.Sender, "sender_uid", record.SenderUID)
			}
			return nil
		})
		go runGroupChatNotificationWorker(ctx, notifier, outbox, logger)
	}
	eventLogger := newSubscriptionEventLogger(cfg.Subscription.EventLogFile, cfg.Subscription.EventLogRetentionDays)
	if err := eventLogger.cleanup(time.Now()); err != nil {
		logger.Error("清理过期订阅原始事件失败", "err", err)
	}

	session, err := groupchat.AcquireAuthSession(ctx, cfg, logger, false)
	if err != nil {
		logger.Error("获取微博订阅认证失败", "err", err)
		if ctx.Err() == nil && notifier.Enabled() {
			_, alertErr := notifyGroupChatAuthFailureOnce(ctx, notifier, cfg.Subscription.RuntimeAlertStateFile, err)
			if alertErr != nil {
				logger.Error("发送微博群聊监控运行异常告警失败", "err", alertErr)
			}
		}
		return 1
	}
	if err := clearGroupChatAuthFailureAlert(cfg.Subscription.RuntimeAlertStateFile); err != nil {
		logger.Error("清除微博群聊监控运行异常状态失败", "err", err)
	}
	logger.Info("群聊订阅模式已启动", "uid", session.UID, "event_log_dir", eventLogger.dir)

	for ctx.Err() == nil {
		sessionCtx, cancelSession := context.WithCancel(ctx)
		authEvents := monitorSubscriptionAuth(sessionCtx, cancelSession, cfg, session, logger)
		err = runSubscriptionSession(sessionCtx, cfg, session, processor, eventLogger, logger)
		cancelSession()
		select {
		case authErr := <-authEvents:
			err = authErr
		default:
		}
		if ctx.Err() != nil {
			break
		}
		if errors.Is(err, groupchat.ErrBayeuxReconnectNone) {
			logger.Info("微博服务端要求停止订阅重连")
			break
		}
		logger.Warn("微博订阅连接中断", "err", err)

		if shouldRefreshSubscriptionCookies(ctx, cfg, session, err, logger) {
			logger.Warn("订阅认证失效，将临时启动浏览器刷新登录态")
			refreshedSession, refreshErr := groupchat.AcquireAuthSession(ctx, cfg, logger, true)
			if refreshErr != nil {
				logger.Error("刷新微博登录态失败", "err", refreshErr)
				if ctx.Err() == nil && notifier.Enabled() {
					_, alertErr := notifyGroupChatAuthFailureOnce(ctx, notifier, cfg.Subscription.RuntimeAlertStateFile, refreshErr)
					if alertErr != nil {
						logger.Error("发送微博群聊监控运行异常告警失败", "err", alertErr)
					}
				}
				if err := waitForRetry(ctx, time.Duration(cfg.Subscription.RetryDelaySeconds)*time.Second); err != nil {
					break
				}
				continue
			}
			session = refreshedSession
			if err := clearGroupChatAuthFailureAlert(cfg.Subscription.RuntimeAlertStateFile); err != nil {
				logger.Error("清除微博群聊监控运行异常状态失败", "err", err)
			}
		}

		if err := waitForRetry(ctx, time.Duration(cfg.Subscription.RetryDelaySeconds)*time.Second); err != nil {
			break
		}
	}

	logger.Info("群聊订阅模式已停止")
	return 0
}

func runGroupChatNotificationWorker(ctx context.Context, notifier groupChatSummarySender, outbox *groupchat.NotificationOutbox, logger interface {
	Info(string, ...any)
	Error(string, ...any)
}) {
	for ctx.Err() == nil {
		item, ready, delay := outbox.Next(time.Now())
		if !ready {
			if delay <= 0 || delay > time.Minute {
				delay = time.Minute
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-outbox.Wake():
				timer.Stop()
				continue
			case <-timer.C:
				continue
			}
		}

		summaries := groupchat.BuildSenderSummariesWithUIDs(
			time.Now(),
			[]groupchat.OutputRecord{item.Record},
			[]string{item.Record.Sender},
			[]string{item.Record.SenderUID},
		)
		if len(summaries) == 1 {
			senderLabel := strings.TrimSpace(item.Record.Sender)
			if senderLabel == "" {
				senderLabel = strings.TrimSpace(item.Record.SenderUID)
			}
			summaries[0].Header = senderLabel
		}
		if err := sendGroupChatSummaries(ctx, notifier, summaries); err != nil {
			if stateErr := outbox.MarkFailed(item.MessageID, err, time.Now()); stateErr != nil {
				logger.Error("记录群聊 Telegram 推送失败状态失败", "message_id", item.MessageID, "err", stateErr)
				if !waitNotificationWorker(ctx, time.Minute, outbox.Wake()) {
					return
				}
				continue
			}
			logger.Error("群聊消息推送 Telegram 失败，稍后重试", "message_id", item.MessageID, "attempt", item.Attempts+1, "err", err)
			continue
		}
		if err := outbox.MarkSent(item.MessageID); err != nil {
			logger.Error("群聊消息已推送但更新队列失败", "message_id", item.MessageID, "err", err)
			if !waitNotificationWorker(ctx, time.Minute, outbox.Wake()) {
				return
			}
			continue
		}
		logger.Info("群聊消息已实时推送到 Telegram", "message_id", item.MessageID, "sender", item.Record.Sender, "sender_uid", item.Record.SenderUID)
	}
}

func waitNotificationWorker(ctx context.Context, delay time.Duration, wake <-chan struct{}) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-wake:
		return true
	case <-timer.C:
		return true
	}
}

func runSubscriptionSession(ctx context.Context, cfg *config.GroupChatModeConfig, session *groupchat.AuthSession, processor *groupchat.SubscriptionEventProcessor, eventLogger *subscriptionEventLogger, logger interface {
	Info(string, ...any)
	Error(string, ...any)
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
	client.SetConnectTimeout(time.Duration(cfg.Subscription.ConnectTimeoutSeconds) * time.Second)
	channel := "/im/" + session.UID
	if _, err := client.Handshake(ctx); err != nil {
		return err
	}
	if err := client.Subscribe(ctx, channel); err != nil {
		return err
	}
	if result, err := groupchat.RunSubscriptionBackfill(ctx, cfg, session.Cookies, processor); err != nil {
		logger.Error("群聊断线补漏失败，将继续实时订阅", "err", err)
	} else if result.Pages > 0 {
		logger.Info(
			"群聊断线补漏完成",
			"pages", result.Pages,
			"fetched", result.Fetched,
			"saved", result.Saved,
			"duplicates", result.Duplicates,
			"reached_boundary", result.ReachedBoundary,
			"initialized_boundary", result.InitializedBoundary,
			"history_exhausted", result.HistoryExhausted,
		)
	}
	return client.ReceiveWithError(ctx, channel, func(message groupchat.BayeuxMessage) error {
		event := loggedSubscriptionEvent{
			ReceivedAt: time.Now().Format(time.RFC3339Nano),
			Channel:    message.Channel,
			Data:       message.Data,
		}
		if err := eventLogger.append(event); err != nil {
			logger.Error("写入订阅事件失败", "err", err)
			return fmt.Errorf("写入订阅事件失败: %w", err)
		}
		result, err := processor.Process(message.Data)
		if err != nil {
			logger.Error("处理微博实时事件失败", "err", err)
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
			if len(result.MediaErrors) > 0 {
				logger.Error("微博实时消息存在媒体下载失败", "message_id", result.MessageID, "error_count", len(result.MediaErrors))
			}
		}
		return nil
	})
}

func newSubscriptionEventLogger(configuredPath string, retentionDays int) *subscriptionEventLogger {
	dir := strings.TrimSpace(configuredPath)
	if strings.EqualFold(filepath.Ext(dir), ".jsonl") {
		dir = strings.TrimSuffix(dir, filepath.Ext(dir))
	}
	return &subscriptionEventLogger{dir: dir, retentionDays: retentionDays}
}

func (l *subscriptionEventLogger) append(event loggedSubscriptionEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	receivedAt, err := time.Parse(time.RFC3339Nano, event.ReceivedAt)
	if err != nil {
		receivedAt = time.Now()
	}
	date := receivedAt.In(time.Local).Format("2006-01-02")
	if l.lastCleanupDate != date {
		if err := l.cleanupLocked(receivedAt); err != nil {
			return err
		}
		l.lastCleanupDate = date
	}
	path := filepath.Join(l.dir, date+".jsonl")
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
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

func (l *subscriptionEventLogger) cleanup(now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.cleanupLocked(now); err != nil {
		return err
	}
	l.lastCleanupDate = now.In(time.Local).Format("2006-01-02")
	return nil
}

func (l *subscriptionEventLogger) cleanupLocked(now time.Time) error {
	if l.retentionDays <= 0 {
		return nil
	}
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return err
	}
	localNow := now.In(time.Local)
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.Local)
	oldestKept := today.AddDate(0, 0, -(l.retentionDays - 1))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		date, err := time.ParseInLocation("2006-01-02", strings.TrimSuffix(entry.Name(), ".jsonl"), time.Local)
		if err != nil || !date.Before(oldestKept) {
			continue
		}
		if err := os.Remove(filepath.Join(l.dir, entry.Name())); err != nil {
			return err
		}
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
		strings.Contains(message, "403") ||
		strings.Contains(message, "401") ||
		strings.Contains(message, "unauthorized") ||
		strings.Contains(message, "login required")
}

func shouldRefreshSubscriptionCookies(ctx context.Context, cfg *config.GroupChatModeConfig, session *groupchat.AuthSession, subscriptionErr error, logger interface {
	Info(string, ...any)
}) bool {
	if errors.Is(subscriptionErr, errProactiveCookieRefresh) || errors.Is(subscriptionErr, errConfirmedAuthInvalid) {
		return true
	}
	if !isSubscriptionAuthError(subscriptionErr) || session == nil {
		return false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, validationErr := groupchat.ValidateStoredCookies(checkCtx, session.Cookies)
	if validationErr == nil {
		logger.Info("订阅返回认证类错误，但当前 Cookie 登录验证仍成功；不启动 Playwright", "subscription_err", subscriptionErr)
		return false
	}
	if !isDefinitiveAuthValidationError(validationErr) {
		logger.Info("Cookie 二次验证遇到临时错误；本轮不启动 Playwright", "validation_err", validationErr)
		return false
	}
	logger.Info("Cookie 二次验证确认登录态失效", "validation_err", validationErr)
	return true
}

func monitorSubscriptionAuth(ctx context.Context, cancelSession context.CancelFunc, cfg *config.GroupChatModeConfig, session *groupchat.AuthSession, logger interface {
	Info(string, ...any)
}) <-chan error {
	events := make(chan error, 1)
	interval := time.Duration(cfg.Subscription.AuthCheckIntervalHours) * time.Hour
	if interval <= 0 || session == nil {
		return events
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				_, validationErr := groupchat.ValidateStoredCookies(checkCtx, session.Cookies)
				cancel()
				if validationErr != nil {
					if isDefinitiveAuthValidationError(validationErr) {
						select {
						case events <- fmt.Errorf("%w: %v", errConfirmedAuthInvalid, validationErr):
							cancelSession()
						default:
						}
						return
					}
					logger.Info("定期 Cookie 验证遇到临时错误，将保留当前订阅", "err", validationErr)
					continue
				}
				logger.Info("定期 Cookie 登录验证成功")
				fingerprint, expiresAt, due := groupchat.ALFProactiveRefreshDue(
					session.Cookies,
					now,
					time.Duration(cfg.Subscription.ProactiveRefreshBeforeExpiryHours)*time.Hour,
				)
				if !due {
					continue
				}
				claimed, claimErr := groupchat.ClaimALFProactiveRefresh(cfg.Subscription.AuthRefreshStateFile, fingerprint, now)
				if claimErr != nil {
					logger.Info("记录 ALF 预刷新状态失败", "err", claimErr)
					continue
				}
				if claimed {
					select {
					case events <- fmt.Errorf("%w: expires_at=%s", errProactiveCookieRefresh, expiresAt.Format(time.RFC3339)):
						cancelSession()
					default:
					}
					return
				}
			}
		}
	}()
	return events
}

func isDefinitiveAuthValidationError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "http 401") ||
		strings.Contains(message, "http 403") ||
		strings.Contains(message, "cookie 为空") ||
		strings.Contains(message, "unauthorized") ||
		strings.Contains(message, "login") ||
		strings.Contains(message, "登录")
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
