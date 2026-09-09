package groupchat

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"weibo_group_chat_monitor/config"
)

type BackfillResult struct {
	Pages               int
	Fetched             int
	Saved               int
	Duplicates          int
	ReachedBoundary     bool
	InitializedBoundary bool
	HistoryExhausted    bool
	LimitReached        bool
}

func RunSubscriptionBackfill(ctx context.Context, cfg *config.GroupChatModeConfig, cookies []StoredCookie, processor *SubscriptionEventProcessor) (BackfillResult, error) {
	var result BackfillResult
	if cfg == nil || processor == nil || !cfg.Subscription.IsBackfillEnabled() {
		return result, nil
	}
	client := NewGroupHistoryClient(cfg, cookies)
	boundaryID := strings.TrimSpace(processor.Checkpoint().ID)
	maxMID := ""
	seenPageIDs := make(map[string]struct{})
	var collected []ChatMessage

	for page := 0; page < cfg.Subscription.BackfillMaxPages; page++ {
		messages, err := client.Fetch(ctx, maxMID)
		if err != nil {
			return result, err
		}
		result.Pages++
		result.Fetched += len(messages)
		if len(messages) == 0 {
			result.HistoryExhausted = true
			break
		}
		if boundaryID == "" && !cfg.Subscription.BackfillOnFirstStart {
			latest := latestBoundary(messages)
			for _, message := range messages {
				if message.IDString() == latest.ID {
					if err := processor.EstablishCheckpoint(message); err != nil {
						return result, err
					}
					result.InitializedBoundary = true
					return result, nil
				}
			}
			return result, fmt.Errorf("群历史响应无法建立初始消息边界")
		}

		var oldest ChatMessage
		for _, message := range messages {
			id := message.IDString()
			if id == "" {
				continue
			}
			if id == boundaryID {
				result.ReachedBoundary = true
				break
			}
			if _, exists := seenPageIDs[id]; exists {
				continue
			}
			seenPageIDs[id] = struct{}{}
			collected = append(collected, message)
		}
		if result.ReachedBoundary {
			break
		}
		if len(messages) < cfg.Chat.BatchSize {
			result.HistoryExhausted = true
			break
		}
		for _, message := range messages {
			if message.IDString() == "" {
				continue
			}
			if oldest.IDString() == "" || chatMessageLess(message, oldest) {
				oldest = message
			}
		}
		oldestID := oldest.IDString()
		if oldestID == "" {
			return result, fmt.Errorf("群历史分页缺少可用的最旧消息 ID；本轮不推进边界")
		}
		if oldestID == maxMID {
			return result, fmt.Errorf("群历史分页停滞在 max_mid=%s；本轮不推进边界", maxMID)
		}
		maxMID = oldestID
	}
	if boundaryID != "" && !result.ReachedBoundary && !result.HistoryExhausted && result.Pages >= cfg.Subscription.BackfillMaxPages {
		result.LimitReached = true
		return result, fmt.Errorf("补漏达到最大页数 %d 仍未追到消息边界 %s；为避免跳过缺口，本轮不推进边界", cfg.Subscription.BackfillMaxPages, boundaryID)
	}

	sort.Slice(collected, func(i, j int) bool { return chatMessageLess(collected[i], collected[j]) })
	for _, message := range collected {
		processed, err := processor.ProcessHistoryMessage(message)
		if err != nil {
			return result, err
		}
		if processed.Duplicate {
			result.Duplicates++
		} else if !processed.Ignored {
			result.Saved++
		}
	}
	return result, nil
}
