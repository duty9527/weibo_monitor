package main

import (
	"context"

	"weibo_group_chat_monitor/groupchat"
	"weibo_group_chat_monitor/telegram"
)

type groupChatSummarySender interface {
	SendGroupChatSummary(context.Context, string, []telegram.GroupChatSummaryEntry) error
}

func sendGroupChatSummaries(ctx context.Context, notifier groupChatSummarySender, summaries []groupchat.SenderSummary) error {
	for _, summary := range summaries {
		entries := make([]telegram.GroupChatSummaryEntry, 0, len(summary.Entries))
		for _, entry := range summary.Entries {
			entries = append(entries, telegram.GroupChatSummaryEntry{
				Text:       entry.Text,
				MediaPaths: entry.MediaPaths,
			})
		}
		header := "#群聊\n" + summary.Header
		if err := notifier.SendGroupChatSummary(ctx, header, entries); err != nil {
			return err
		}
	}
	return nil
}
