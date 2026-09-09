package groupchat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNotificationOutboxPersistsDeduplicatesAndRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	queue, err := NewNotificationOutbox(path)
	if err != nil {
		t.Fatal(err)
	}
	record := OutputRecord{ID: "123", Sender: "new-name", SenderUID: "42", Message: "hello"}
	queued, err := queue.Enqueue(record)
	if err != nil || !queued {
		t.Fatalf("enqueue: queued=%v err=%v", queued, err)
	}
	queued, err = queue.Enqueue(record)
	if err != nil || queued || queue.Len() != 1 {
		t.Fatalf("duplicate enqueue: queued=%v len=%d err=%v", queued, queue.Len(), err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("outbox mode: info=%v err=%v", info, err)
	}

	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.Local)
	item, ready, _ := queue.Next(now)
	if !ready || item.Record.SenderUID != "42" {
		t.Fatalf("unexpected ready item: %#v ready=%v", item, ready)
	}
	if err := queue.MarkFailed(item.MessageID, errors.New("telegram unavailable"), now); err != nil {
		t.Fatal(err)
	}
	if _, ready, wait := queue.Next(now); ready || wait != 5*time.Second {
		t.Fatalf("unexpected retry schedule: ready=%v wait=%s", ready, wait)
	}

	restarted, err := NewNotificationOutbox(path)
	if err != nil {
		t.Fatal(err)
	}
	item, ready, _ = restarted.Next(now.Add(5 * time.Second))
	if !ready || item.Attempts != 1 || item.LastError != "telegram unavailable" {
		t.Fatalf("retry state was not restored: %#v ready=%v", item, ready)
	}
	if err := restarted.MarkSent(item.MessageID); err != nil {
		t.Fatal(err)
	}
	if restarted.Len() != 0 {
		t.Fatalf("sent item remains queued: %d", restarted.Len())
	}
}

func TestMatchesTargetSenderByUID(t *testing.T) {
	if !MatchesTargetSender("renamed", "6355013357", nil, []string{"6355013357"}) {
		t.Fatal("uid should match after sender rename")
	}
	if MatchesTargetSender("renamed", "9", []string{"tombkeeper"}, []string{"8"}) {
		t.Fatal("unexpected sender match")
	}
}
