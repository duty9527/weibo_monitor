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

	state, err := weibo.LoadRunState(cfg.Weibo.StateFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载状态文件失败: %v\n", err)
		os.Exit(1)
	}
	if err := ensureInitialState(cfg, state, logger); err != nil {
		fmt.Fprintf(os.Stderr, "初始化状态文件失败: %v\n", err)
		os.Exit(1)
	}
	applyStateSinceTime(cfg, state, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	notifier := telegram.NewClient(cfg.Telegram, logger)

	if err := executeOnce(ctx, cfg, state, logger, notifier); err != nil {
		logger.Error("执行任务失败", "err", err)
		os.Exit(1)
	}
}

func executeOnce(
	ctx context.Context,
	cfg *config.Config,
	state *weibo.RunState,
	logger *slog.Logger,
	notifier *telegram.Client,
) error {
	logger.Info(
		"开始执行抓取任务",
		"target_uid", cfg.Weibo.TargetUID,
		"since_time", cfg.Weibo.SinceTime,
		"state_file", cfg.Weibo.StateFile,
		"telegram_enabled", cfg.Telegram.Enabled,
	)

	if err := maybeRefreshCookiesViaPlaywright(ctx, cfg, state, logger); err != nil {
		logger.Warn("按计划刷新 Playwright Cookie 失败，将继续使用现有 Cookie", "err", err)
	}

	cookieStr, usedPlaywright, err := loadCookies(ctx, cfg, logger)
	if err != nil {
		return err
	}
	if usedPlaywright {
		state.SetLastPlaywrightRefreshTime(time.Now())
		if err := weibo.SaveRunState(cfg.Weibo.StateFile, state); err != nil {
			logger.Warn("更新状态文件失败", "err", err)
		}
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
	if latestFetchedAt, ok := scraper.LatestFetchedTime(); ok {
		state.SetLastFetchedTime(latestFetchedAt)
		if err := weibo.SaveRunState(cfg.Weibo.StateFile, state); err != nil {
			logger.Warn("写入最新抓取时间失败", "err", err)
		} else {
			cfg.Weibo.SinceTime = latestFetchedAt.In(time.Local).Format(time.RFC3339)
		}
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

func loadCookies(ctx context.Context, cfg *config.Config, logger *slog.Logger) (string, bool, error) {
	if cookieStr := strings.TrimSpace(cfg.Weibo.CookieString); cookieStr != "" {
		logger.Info("使用配置中的 cookie_string")
		return cookieStr, false, nil
	}

	if cookieFile := strings.TrimSpace(cfg.Weibo.CookieFile); cookieFile != "" {
		logger.Info("从 cookie_file 加载 Cookie", "path", cookieFile)
		cookieStr, err := weibo.ReadNetscapeCookies(cookieFile)
		return cookieStr, false, err
	}

	extractor := weibo.NewCookieExtractor(cfg.Weibo.UserDataDir, logger)
	result, err := extractor.ExtractOrLoginResult(ctx, cfg.Weibo)
	if err != nil {
		return "", false, err
	}
	return result.CookieString, result.UsedPlaywright, nil
}

func maybeRefreshCookiesViaPlaywright(ctx context.Context, cfg *config.Config, state *weibo.RunState, logger *slog.Logger) error {
	if strings.TrimSpace(cfg.Weibo.CookieString) != "" || strings.TrimSpace(cfg.Weibo.CookieFile) != "" {
		return nil
	}

	refreshEvery := time.Duration(cfg.Weibo.PlaywrightRefreshHours) * time.Hour
	if refreshEvery <= 0 {
		return nil
	}

	lastRefresh, ok := state.LastPlaywrightRefreshTime()
	if ok && time.Since(lastRefresh) < refreshEvery {
		return nil
	}

	logger.Info("达到 Playwright Cookie 刷新时间，开始保活", "refresh_hours", cfg.Weibo.PlaywrightRefreshHours)

	extractor := weibo.NewCookieExtractor(cfg.Weibo.UserDataDir, logger)
	if _, err := extractor.RefreshViaPlaywright(ctx, cfg.Weibo); err != nil {
		return err
	}

	state.SetLastPlaywrightRefreshTime(time.Now())
	if err := weibo.SaveRunState(cfg.Weibo.StateFile, state); err != nil {
		return err
	}

	return nil
}

func applyStateSinceTime(cfg *config.Config, state *weibo.RunState, logger *slog.Logger) {
	if state == nil {
		return
	}

	lastFetchedAt, ok := state.LastFetchedTime()
	if !ok {
		return
	}

	cfg.Weibo.SinceTime = lastFetchedAt.In(time.Local).Format(time.RFC3339)
	logger.Info("应用状态文件中的增量抓取时间", "since_time", cfg.Weibo.SinceTime)
}

func ensureInitialState(cfg *config.Config, state *weibo.RunState, logger *slog.Logger) error {
	if state == nil {
		return nil
	}
	if _, ok := state.LastFetchedTime(); ok {
		return nil
	}

	initialSince, err := weibo.ParseConfigTime(cfg.Weibo.SinceTime)
	if err != nil {
		return fmt.Errorf("解析初始 since_time 失败: %w", err)
	}

	state.SetLastFetchedTime(initialSince)
	if err := weibo.SaveRunState(cfg.Weibo.StateFile, state); err != nil {
		return err
	}

	logger.Info("首次运行，已按配置初始化状态文件", "since_time", state.LastFetchedAt, "state_file", cfg.Weibo.StateFile)
	return nil
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
