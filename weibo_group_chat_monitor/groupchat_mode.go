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

	result, err := scraper.Run(ctx)
	if err != nil {
		return err
	}

	if notifier.Enabled() {
		summaries := groupchat.BuildSenderSummaries(time.Now(), result.NewRecords, cfg.Filters.TargetSenders)
		for _, summary := range summaries {
			entries := make([]telegram.GroupChatSummaryEntry, 0, len(summary.Entries))
			for _, entry := range summary.Entries {
				entries = append(entries, telegram.GroupChatSummaryEntry{
					Text:       entry.Text,
					MediaPaths: entry.MediaPaths,
				})
			}
			if err := notifier.SendGroupChatSummary(ctx, summary.Header, entries); err != nil {
				return fmt.Errorf("推送群聊摘要失败: %w", err)
			}
		}
		logger.Info("群聊摘要推送完成", "message_count", len(summaries))
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
