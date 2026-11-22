package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"
	"weibo_group_chat_monitor/config"
	"weibo_group_chat_monitor/weibo"
)

type weiboRecordSender interface {
	Enabled() bool
	SendRecord(context.Context, *weibo.WeiboRecord) error
}

// History is the durable payload store. Each delivery has its own acknowledgement,
// so a failed record cannot block others or be lost behind a global watermark.
func drainWeiboHistory(ctx context.Context, cfg *config.WeiboModeConfig, state *weibo.RunState, logger *slog.Logger, sender weiboRecordSender) error {
	if !sender.Enabled() {
		return nil
	}
	records, err := weibo.LoadLocalHistoryRecords(cfg.Weibo.HistoryFile)
	if err != nil {
		return fmt.Errorf("加载微博历史失败: %w", err)
	}
	if state.DeliveryBaseline == nil {
		baseline := state.LastPushedAt
		state.DeliveryBaseline = &baseline
	}
	if state.Deliveries == nil {
		state.Deliveries = make(map[string]weibo.DeliveryState)
	}
	baseline := time.Time{}
	if *state.DeliveryBaseline != "" {
		baseline, err = weibo.ParseWeiboTime(*state.DeliveryBaseline)
		if err != nil {
			return fmt.Errorf("微博推送基线无效: %w", err)
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		a, _ := weibo.ParseWeiboTime(records[i].CreatedAt)
		b, _ := weibo.ParseWeiboTime(records[j].CreatedAt)
		if a.Equal(b) {
			return records[i].ID < records[j].ID
		}
		return a.Before(b)
	})
	// Persist migration before any external sends.
	if err := weibo.SaveRunState(cfg.Weibo.StateFile, state); err != nil {
		return err
	}
	var failures []error
	for _, record := range records {
		created, err := weibo.ParseWeiboTime(record.CreatedAt)
		if err != nil {
			failures = append(failures, fmt.Errorf("微博 %s 时间无法解析: %w", record.ID, err))
			continue
		}
		if !created.After(baseline) {
			continue
		}
		delivery := state.Deliveries[record.ID]
		if delivery.Sent || time.Now().Before(delivery.NextAttemptAt) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		sendRecord, keys, sendErr := weibo.FilterSentMedia(record, state)
		if sendErr == nil {
			sendRecord.SkippedMediaCount = len(record.LocalMediaPaths) - len(sendRecord.LocalMediaPaths)
			sendErr = sender.SendRecord(ctx, sendRecord)
		}
		delivery.Attempts++
		if sendErr != nil {
			delivery.LastError = telegramTokenPattern.ReplaceAllString(sendErr.Error(), "bot[REDACTED]")
			delay := 15 * time.Minute
			for n := 1; n < delivery.Attempts && delay < 6*time.Hour; n++ {
				delay *= 2
			}
			if delay > 6*time.Hour {
				delay = 6 * time.Hour
			}
			delivery.NextAttemptAt = time.Now().Add(delay)
			failures = append(failures, fmt.Errorf("推送微博 %s 失败: %w", record.ID, sendErr))
			logger.Error("微博推送失败，已保留重试且继续后续消息", "id", record.ID, "retry_at", delivery.NextAttemptAt, "err", sendErr)
		} else {
			delivery.Sent = true
			delivery.LastError = ""
			delivery.NextAttemptAt = time.Time{}
			if !sendRecord.MediaDeliveryDegraded {
				state.MarkMediaSent(keys)
			}
			if last, ok := state.LastPushedTime(); !ok || created.After(last) {
				state.SetLastPushedTime(created)
			}
			logger.Info("微博推送已确认", "id", record.ID)
		}
		state.Deliveries[record.ID] = delivery
		if err := weibo.SaveRunState(cfg.Weibo.StateFile, state); err != nil {
			return fmt.Errorf("保存微博推送状态失败，停止后续发送: %w", err)
		}
	}
	return errors.Join(failures...)
}
