package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"weibo_group_chat_monitor/config"
	"weibo_group_chat_monitor/groupchat"
	"weibo_group_chat_monitor/telegram"
)

func runGroupChatMode(args []string) int {
	fs := flag.NewFlagSet("groupchat", flag.ContinueOnError)
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
	logger.Info("群聊模式配置加载完成", "config", *configPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	notifier := telegram.NewClient(cfg.Telegram, logger)

	if err := executeGroupChatOnce(ctx, cfg, notifier); err != nil {
		logger.Error("执行群聊抓取失败", "err", err)
		return 1
	}
	return 0
}

func executeGroupChatOnce(ctx context.Context, cfg *config.GroupChatModeConfig, notifier *telegram.Client) error {
	logger := newLogger(cfg.Log.Level)
	scraper := groupchat.NewScraper(cfg, logger)

	_, err := scraper.Run(ctx)
	if err != nil {
		return err
	}

	state, err := groupchat.LoadRunState(cfg.State.StateFile)
	if err != nil {
		return fmt.Errorf("加载状态文件失败: %w", err)
	}

	if notifier.Enabled() {
		var startDate string
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", state.LastPushedTime, time.Local); err == nil {
			startDate = t.Format("2006-01-02")
		} else {
			startDate = time.Now().Format("2006-01-02")
		}

		records, err := groupchat.LoadLocalHistoryRecords(cfg.Output.HistoryFile, groupchat.LocalHistoryReadOptions{
			TargetSenders: cfg.Filters.TargetSenders,
			StartDate:     startDate,
			EndDate:       time.Now().Format("2006-01-02"),
		})
		if err != nil {
			return fmt.Errorf("加载本地群聊历史失败: %w", err)
		}

		var toPush []groupchat.OutputRecord
		for _, rec := range records {
			if rec.Time > state.LastPushedTime {
				toPush = append(toPush, rec)
			}
		}

		if len(toPush) > 0 {
			summaries := groupchat.BuildSenderSummaries(time.Now(), toPush, cfg.Filters.TargetSenders)
			if err := sendGroupChatSummaries(ctx, notifier, summaries); err != nil {
				return fmt.Errorf("推送群聊摘要失败: %w", err)
			}
			logger.Info("群聊摘要推送完成", "message_count", len(toPush), "summaries_count", len(summaries))

			latestRec := toPush[len(toPush)-1]
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", latestRec.Time, time.Local); err == nil {
				state.SetPushedBoundary(latestRec.ID, t)
				if err := groupchat.SaveRunState(cfg.State.StateFile, state); err != nil {
					logger.Warn("更新推送水位状态失败", "err", err)
				}
			}
		} else {
			logger.Info("没有新消息需要推送")
			// 兜底对齐
			if state.LastPushedTime != state.LastMessageTime || state.LastPushedID != state.LastMessageID {
				if t, err := time.ParseInLocation("2006-01-02 15:04:05", state.LastMessageTime, time.Local); err == nil {
					state.SetPushedBoundary(state.LastMessageID, t)
					_ = groupchat.SaveRunState(cfg.State.StateFile, state)
				}
			}
		}
	} else {
		// 未开启通知时，自动向前对齐推送边界
		if state.LastPushedTime != state.LastMessageTime || state.LastPushedID != state.LastMessageID {
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", state.LastMessageTime, time.Local); err == nil {
				state.SetPushedBoundary(state.LastMessageID, t)
				_ = groupchat.SaveRunState(cfg.State.StateFile, state)
			}
		}
	}

	return nil
}

func defaultGroupChatConfigPath() string {
	candidates := []string{
		"config.groupchat.yaml",
		"config.group_chat.yaml",
		"config.yaml",
		"weibo_group_chat_monitor/config.groupchat.yaml",
		"weibo_group_chat_monitor/config.group_chat.yaml",
		"weibo_group_chat_monitor/config.yaml",
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "config.groupchat.yaml"
}
