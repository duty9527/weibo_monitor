package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"weibo_monitor/config"
	"weibo_monitor/telegram"
	"weibo_monitor/weibo"
)

func main() {
	configPath := flag.String("config", defaultConfigPath(), "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	logger := newLogger(cfg.Log.Level)
	logger.Info("配置加载完成", "config", *configPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	notifier := telegram.NewClient(cfg.Telegram, logger)

	runOnce := func() error {
		return executeOnce(ctx, cfg, logger, notifier)
	}

	if err := runOnce(); err != nil {
		logger.Error("执行任务失败", "err", err)
		os.Exit(1)
	}

	if cfg.Weibo.PollIntervalSeconds <= 0 {
		return
	}

	ticker := time.NewTicker(time.Duration(cfg.Weibo.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()

	logger.Info("进入轮询模式", "interval_seconds", cfg.Weibo.PollIntervalSeconds)

	for {
		select {
		case <-ctx.Done():
			logger.Info("收到退出信号，程序结束")
			return
		case <-ticker.C:
			if err := runOnce(); err != nil {
				logger.Error("轮询执行失败", "err", err)
			}
		}
	}
}

func executeOnce(
	ctx context.Context,
	cfg *config.Config,
	logger *slog.Logger,
	notifier *telegram.Client,
) error {
	logger.Info(
		"开始执行抓取任务",
		"target_uid", cfg.Weibo.TargetUID,
		"since_time", cfg.Weibo.SinceTime,
		"telegram_enabled", cfg.Telegram.Enabled,
	)

	cookieStr, err := loadCookies(ctx, cfg, logger)
	if err != nil {
		return err
	}
	if !weibo.VerifyCookies(ctx, cookieStr, cfg.Weibo.TargetUID) {
		return fmt.Errorf("微博 Cookie 校验失败，请重新登录或更新 cookie 配置")
	}

	scraper, err := weibo.NewScraper(cfg, logger)
	if err != nil {
		return err
	}
	scraper.SetCookies(cookieStr)

	records, err := scraper.FetchNewRecords(ctx)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		logger.Info("本轮没有发现新微博")
		return nil
	}

	for _, record := range records {
		if notifier.Enabled() {
			logger.Info("推送微博到 Telegram", "id", record.ID)
			if err := notifier.SendRecord(ctx, record); err != nil {
				return fmt.Errorf("推送微博 %s 失败: %w", record.ID, err)
			}
		}

		if err := scraper.AppendRecord(record); err != nil {
			return fmt.Errorf("保存微博 %s 失败: %w", record.ID, err)
		}
		logger.Info("微博处理完成", "id", record.ID)
	}

	logger.Info("本轮任务完成", "new_count", len(records))
	return nil
}

func loadCookies(ctx context.Context, cfg *config.Config, logger *slog.Logger) (string, error) {
	if cookieStr := strings.TrimSpace(cfg.Weibo.CookieString); cookieStr != "" {
		logger.Info("使用配置中的 cookie_string")
		return cookieStr, nil
	}

	if cookieFile := strings.TrimSpace(cfg.Weibo.CookieFile); cookieFile != "" {
		logger.Info("从 cookie_file 加载 Cookie", "path", cookieFile)
		return weibo.ReadNetscapeCookies(cookieFile)
	}

	extractor := weibo.NewCookieExtractor(cfg.Weibo.UserDataDir, logger)
	return extractor.ExtractOrLogin(ctx, cfg.Weibo)
}

func newLogger(level string) *slog.Logger {
	var slogLevel slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}

	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slogLevel}))
}

func defaultConfigPath() string {
	candidates := []string{
		"config.yaml",
		"weibo_monitor_go/config.yaml",
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "config.yaml"
}
