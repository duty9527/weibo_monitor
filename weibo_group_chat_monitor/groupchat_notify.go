package main

import (
	"context"

	"weibo_group_chat_monitor/groupchat"
	"weibo_group_chat_monitor/telegram"
)

func sendGroupChatSummaries(ctx context.Context, notifier *telegram.Client, summaries []groupchat.SenderSummary) error {
	for _, summary := range summaries {
		entries := make([]telegram.GroupChatSummaryEntry, 0, len(summary.Entries))
		for _, entry := range summary.Entries {
			entries = append(entries, telegram.GroupChatSummaryEntry{
				Text:       entry.Text,
				MediaPaths: entry.MediaPaths,
			})
		}
		if err := notifier.SendGroupChatSummary(ctx, summary.Header, entries); err != nil {
			return err
		}
	}
	return nil
}
