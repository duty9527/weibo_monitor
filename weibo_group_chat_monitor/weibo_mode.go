package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"log/slog"

	"weibo_group_chat_monitor/config"
	"weibo_group_chat_monitor/telegram"
	"weibo_group_chat_monitor/weibo"
)

func runWeiboMode(args []string) int {
	fs := flag.NewFlagSet("weibo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	configPath := fs.String("config", defaultWeiboConfigPath(), "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.LoadWeibo(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		return 1
	}

	logger := newLogger(cfg.Log.Level)
	logger.Info("微博模式配置加载完成", "config", *configPath)

	state, err := weibo.LoadRunState(cfg.Weibo.StateFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载状态文件失败: %v\n", err)
		return 1
	}
	if err := ensureInitialWeiboState(cfg, state, logger); err != nil {
		fmt.Fprintf(os.Stderr, "初始化状态文件失败: %v\n", err)
		return 1
	}
	applyWeiboStateSinceTime(cfg, state, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	notifier := telegram.NewClient(cfg.Telegram, logger)
	if err := executeWeiboOnce(ctx, cfg, state, logger, notifier); err != nil {
		logger.Error("执行微博任务失败", "err", err)
		return 1
	}
	return 0
}

func executeWeiboOnce(
	ctx context.Context,
	cfg *config.WeiboModeConfig,
	state *weibo.RunState,
	logger *slog.Logger,
	notifier *telegram.Client,
) error {
	logger.Info(
		"开始执行微博抓取任务",
		"target_uid", cfg.Weibo.TargetUID,
		"since_time", cfg.Weibo.SinceTime,
		"state_file", cfg.Weibo.StateFile,
		"telegram_enabled", cfg.Telegram.Enabled,
	)

	cookieStr, usedPlaywright, err := loadWeiboCookies(ctx, cfg, logger)
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

	// 1. 抓到新博文立即全部落盘并保存抓取水位线
	if len(records) > 0 {
		for _, record := range records {
			if err := scraper.AppendRecord(record); err != nil {
				return fmt.Errorf("保存微博 %s 失败: %w", record.ID, err)
			}
			logger.Info("微博落盘成功", "id", record.ID)
		}

		if latestFetchedAt, ok := scraper.LatestFetchedTime(); ok {
			state.SetLastFetchedTime(latestFetchedAt)
			if err := weibo.SaveRunState(cfg.Weibo.StateFile, state); err != nil {
				logger.Warn("写入最新抓取时间失败", "err", err)
			} else {
				cfg.Weibo.SinceTime = latestFetchedAt.In(time.Local).Format(time.RFC3339)
			}
		}
	}

	// 2. 推送阶段：读取本地历史做断点补偿发送
	if notifier.Enabled() {
		historyRecords, err := weibo.LoadLocalHistoryRecords(cfg.Weibo.HistoryFile)
		if err != nil {
			return fmt.Errorf("加载本地微博历史失败: %w", err)
		}

		lastPushedTime, hasPushed := state.LastPushedTime()

		var toPush []*weibo.WeiboRecord
		for _, rec := range historyRecords {
			recTime, err := weibo.ParseWeiboTime(rec.CreatedAt)
			if err != nil {
				continue
			}
			if !hasPushed || recTime.After(lastPushedTime) {
				toPush = append(toPush, rec)
			}
		}

		if len(toPush) > 0 {
			// 按 CreatedAt 从旧到新排序
			sort.Slice(toPush, func(i, j int) bool {
				tI, _ := weibo.ParseWeiboTime(toPush[i].CreatedAt)
				tJ, _ := weibo.ParseWeiboTime(toPush[j].CreatedAt)
				return tI.Before(tJ)
			})

			for _, record := range toPush {
				sendRecord, newMediaKeys, err := weibo.FilterSentMedia(record, state)
				if err != nil {
					return fmt.Errorf("过滤微博 %s 的已发送媒体失败: %w", record.ID, err)
				}

				skippedMediaCount := len(record.LocalMediaPaths) - len(sendRecord.LocalMediaPaths)
				sendRecord.SkippedMediaCount = skippedMediaCount
				if skippedMediaCount > 0 {
					logger.Info(
						"检测到已发送媒体，发送时跳过",
						"id", record.ID,
						"media_total", len(record.LocalMediaPaths),
						"media_skipped", skippedMediaCount,
					)
				}

				logger.Info("推送微博到 Telegram", "id", record.ID, "media_to_send", len(sendRecord.LocalMediaPaths))
				if err := notifier.SendRecord(ctx, sendRecord); err != nil {
					return fmt.Errorf("推送微博 %s 失败: %w", record.ID, err)
				}

				if len(newMediaKeys) > 0 {
					state.MarkMediaSent(newMediaKeys)
				}

				// 发送成功，更新推送水位线并保存
				if t, err := weibo.ParseWeiboTime(record.CreatedAt); err == nil {
					state.SetLastPushedTime(t)
					if err := weibo.SaveRunState(cfg.Weibo.StateFile, state); err != nil {
						logger.Warn("更新微博推送水位状态失败", "err", err)
					}
				}
			}
			logger.Info("微博推送完成", "sent_count", len(toPush))
		} else {
			logger.Info("没有新微博需要推送")
			// 兜底对齐
			if latestFetchedAt, ok := scraper.LatestFetchedTime(); ok {
				state.SetLastPushedTime(latestFetchedAt)
				_ = weibo.SaveRunState(cfg.Weibo.StateFile, state)
			}
		}
	} else {
		// 未开启通知时，自动向前对齐推送水位
		if latestFetchedAt, ok := scraper.LatestFetchedTime(); ok {
			state.SetLastPushedTime(latestFetchedAt)
			_ = weibo.SaveRunState(cfg.Weibo.StateFile, state)
		}
	}

	logger.Info("微博任务完成", "new_count", len(records))
	return nil
}

func loadWeiboCookies(ctx context.Context, cfg *config.WeiboModeConfig, logger *slog.Logger) (string, bool, error) {
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

func applyWeiboStateSinceTime(cfg *config.WeiboModeConfig, state *weibo.RunState, logger *slog.Logger) {
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

func ensureInitialWeiboState(cfg *config.WeiboModeConfig, state *weibo.RunState, logger *slog.Logger) error {
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

func defaultWeiboConfigPath() string {
	candidates := []string{
		"config.weibo.yaml",
		"config.yaml",
		"weibo_group_chat_monitor/config.weibo.yaml",
		"weibo_group_chat_monitor/config.yaml",
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "config.weibo.yaml"
}
