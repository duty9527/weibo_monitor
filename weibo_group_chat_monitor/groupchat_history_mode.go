package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"weibo_group_chat_monitor/config"
	"weibo_group_chat_monitor/groupchat"
	"weibo_group_chat_monitor/telegram"
)

func runGroupChatHistoryMode(args []string) int {
	fs := flag.NewFlagSet("groupchat-history", flag.ContinueOnError)
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
	if !cfg.Telegram.Enabled {
		fmt.Fprintln(os.Stderr, "groupchat-history 模式要求 telegram.enabled=true")
		return 1
	}

	logger := newLogger(cfg.Log.Level)
	logger.Info("本地历史推送配置加载完成", "config", *configPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	notifier := telegram.NewClient(cfg.Telegram, logger)
	if err := executeGroupChatHistoryPush(ctx, cfg, notifier); err != nil {
		logger.Error("执行本地历史推送失败", "err", err)
		return 1
	}
	return 0
}

func executeGroupChatHistoryPush(ctx context.Context, cfg *config.GroupChatModeConfig, notifier *telegram.Client) error {
	if !notifier.Enabled() {
		return fmt.Errorf("telegram.enabled=false，无法推送本地历史")
	}
	logger := newLogger(cfg.Log.Level)

	records, err := groupchat.LoadLocalHistoryRecords(cfg.Output.HistoryFile, groupchat.LocalHistoryReadOptions{
		TargetSenders: cfg.Filters.TargetSenders,
		StartDate:     cfg.LocalHistory.StartDate,
		EndDate:       cfg.LocalHistory.EndDate,
		MaxRecords:    cfg.LocalHistory.MaxRecords,
	})
	if err != nil {
		return fmt.Errorf("读取本地历史失败: %w", err)
	}

	summaries := groupchat.BuildLocalHistorySenderSummaries(records, cfg.Filters.TargetSenders)
	if len(summaries) == 0 {
		logger.Info(
			"未命中本地历史筛选结果",
			"history_path", cfg.Output.HistoryFile,
			"target_senders", cfg.Filters.TargetSenders,
			"start_date", cfg.LocalHistory.StartDate,
			"end_date", cfg.LocalHistory.EndDate,
		)
		return nil
	}

	if err := sendGroupChatSummaries(ctx, notifier, summaries); err != nil {
		return fmt.Errorf("推送本地历史摘要失败: %w", err)
	}

	logger.Info(
		"本地历史推送完成",
		"record_count", len(records),
		"sender_count", len(summaries),
		"history_path", cfg.Output.HistoryFile,
		"start_date", cfg.LocalHistory.StartDate,
		"end_date", cfg.LocalHistory.EndDate,
		"max_records", cfg.LocalHistory.MaxRecords,
	)
	return nil
}
