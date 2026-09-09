package groupchat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const notificationRetryMaxDelay = 15 * time.Minute

type NotificationItem struct {
	MessageID     string       `json:"message_id"`
	Record        OutputRecord `json:"record"`
	CreatedAt     string       `json:"created_at"`
	Attempts      int          `json:"attempts"`
	NextAttemptAt string       `json:"next_attempt_at,omitempty"`
	LastError     string       `json:"last_error,omitempty"`
}

type notificationOutboxState struct {
	Items []NotificationItem `json:"items"`
}

type NotificationOutbox struct {
	path  string
	items []NotificationItem
	wake  chan struct{}
	mu    sync.Mutex
}

func NewNotificationOutbox(path string) (*NotificationOutbox, error) {
	outbox := &NotificationOutbox{path: path, wake: make(chan struct{}, 1)}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return outbox, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取群聊通知队列失败: %w", err)
	}
	var state notificationOutboxState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("解析群聊通知队列失败: %w", err)
	}
	seen := make(map[string]struct{}, len(state.Items))
	for _, item := range state.Items {
		item.MessageID = strings.TrimSpace(item.MessageID)
		if item.MessageID == "" {
			return nil, fmt.Errorf("群聊通知队列包含空消息 ID")
		}
		if _, exists := seen[item.MessageID]; exists {
			continue
		}
		seen[item.MessageID] = struct{}{}
		outbox.items = append(outbox.items, item)
	}
	return outbox, nil
}

func (q *NotificationOutbox) Enqueue(record OutputRecord) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	messageID := strings.TrimSpace(record.ID)
	if messageID == "" {
		return false, fmt.Errorf("待推送群聊消息缺少 ID")
	}
	for _, item := range q.items {
		if item.MessageID == messageID {
			return false, nil
		}
	}
	q.items = append(q.items, NotificationItem{
		MessageID: messageID,
		Record:    record,
		CreatedAt: time.Now().Format(time.RFC3339Nano),
	})
	if err := q.saveLocked(); err != nil {
		q.items = q.items[:len(q.items)-1]
		return false, err
	}
	q.signal()
	return true, nil
}

func (q *NotificationOutbox) Next(now time.Time) (NotificationItem, bool, time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return NotificationItem{}, false, 0
	}
	item := q.items[0]
	if strings.TrimSpace(item.NextAttemptAt) == "" {
		return item, true, 0
	}
	next, err := time.Parse(time.RFC3339Nano, item.NextAttemptAt)
	if err != nil || !next.After(now) {
		return item, true, 0
	}
	return item, false, next.Sub(now)
}

func (q *NotificationOutbox) MarkSent(messageID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for index, item := range q.items {
		if item.MessageID != messageID {
			continue
		}
		previous := append([]NotificationItem(nil), q.items...)
		q.items = append(q.items[:index], q.items[index+1:]...)
		if err := q.saveLocked(); err != nil {
			q.items = previous
			return err
		}
		q.signal()
		return nil
	}
	return nil
}

func (q *NotificationOutbox) MarkFailed(messageID string, sendErr error, now time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for index := range q.items {
		if q.items[index].MessageID != messageID {
			continue
		}
		previous := q.items[index]
		q.items[index].Attempts++
		delay := notificationRetryDelay(q.items[index].Attempts)
		q.items[index].NextAttemptAt = now.Add(delay).Format(time.RFC3339Nano)
		if sendErr != nil {
			q.items[index].LastError = sendErr.Error()
		}
		if err := q.saveLocked(); err != nil {
			q.items[index] = previous
			return err
		}
		q.signal()
		return nil
	}
	return nil
}

func (q *NotificationOutbox) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func (q *NotificationOutbox) Wake() <-chan struct{} {
	return q.wake
}

func (q *NotificationOutbox) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *NotificationOutbox) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(q.path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(notificationOutboxState{Items: q.items})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(q.path), ".notification-outbox-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, q.path)
}

func notificationRetryDelay(attempts int) time.Duration {
	if attempts <= 0 {
		return 0
	}
	delay := 5 * time.Second
	for index := 1; index < attempts && delay < notificationRetryMaxDelay; index++ {
		delay *= 2
		if delay > notificationRetryMaxDelay {
			delay = notificationRetryMaxDelay
		}
	}
	return delay
}
