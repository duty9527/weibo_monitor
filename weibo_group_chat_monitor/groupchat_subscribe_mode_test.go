package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"weibo_group_chat_monitor/groupchat"
	"weibo_group_chat_monitor/telegram"
)

func TestIsSubscriptionAuthError(t *testing.T) {
	for _, message := range []string{"403:auth fail:create_denied", "HTTP 403", "HTTP 401", "create_denied", "unauthorized", "login required"} {
		if !isSubscriptionAuthError(assertError(message)) {
			t.Fatalf("expected auth error for %q", message)
		}
	}
	if isSubscriptionAuthError(assertError("connection reset")) {
		t.Fatal("network error must not be classified as auth error")
	}
}

func TestIsDefinitiveAuthValidationError(t *testing.T) {
	for _, message := range []string{"HTTP 401", "HTTP 403", "微博 Cookie 为空", "需要登录"} {
		if !isDefinitiveAuthValidationError(assertError(message)) {
			t.Fatalf("expected definitive auth error: %q", message)
		}
	}
	if isDefinitiveAuthValidationError(assertError("connection reset by peer")) {
		t.Fatal("network error must not trigger Playwright")
	}
}

func TestAppendSubscriptionEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events", "events.jsonl")
	logger := newSubscriptionEventLogger(path, 7)
	event := loggedSubscriptionEvent{ReceivedAt: "2026-08-31T12:00:00+08:00", Channel: "/im/42", Data: json.RawMessage(`{"gid":"123"}`)}
	if err := logger.append(event); err != nil {
		t.Fatalf("append event failed: %v", err)
	}
	dailyPath := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "2026-08-31.jsonl")
	data, err := os.ReadFile(dailyPath)
	if err != nil {
		t.Fatalf("read event log failed: %v", err)
	}
	if !strings.Contains(string(data), `"channel":"/im/42"`) || !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("unexpected event log: %s", data)
	}
	if mode := mustFileMode(t, dailyPath); mode != 0o600 {
		t.Fatalf("unexpected event log permissions: %o", mode)
	}
}

func TestSubscriptionEventLoggerCleansExpiredDailyFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "events")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"2026-08-24.jsonl", "2026-08-25.jsonl", "notes.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("event\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logger := newSubscriptionEventLogger(dir, 7)
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.Local)
	if err := logger.cleanup(now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-08-24.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("expired event log was not removed: %v", err)
	}
	for _, name := range []string{"2026-08-25.jsonl", "notes.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("retained file %s is missing: %v", name, err)
		}
	}
}

type recordingGroupChatSender struct {
	sent chan recordedGroupChatSummary
}

type recordedGroupChatSummary struct {
	header  string
	entries []telegram.GroupChatSummaryEntry
}

func (s *recordingGroupChatSender) SendGroupChatSummary(_ context.Context, header string, entries []telegram.GroupChatSummaryEntry) error {
	s.sent <- recordedGroupChatSummary{header: header, entries: entries}
	return nil
}

type discardWorkerLogger struct{}

func (discardWorkerLogger) Info(string, ...any)  {}
func (discardWorkerLogger) Error(string, ...any) {}

func TestGroupChatNotificationWorkerSendsAndAcknowledges(t *testing.T) {
	outbox, err := groupchat.NewNotificationOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = outbox.Enqueue(groupchat.OutputRecord{
		ID: "123", Time: "2026-09-02 12:00:00", Date: "2026-09-02",
		Sender: "renamed", SenderUID: "42", Message: "hello", TextClean: "hello", MsgType: "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	sender := &recordingGroupChatSender{sent: make(chan recordedGroupChatSummary, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runGroupChatNotificationWorker(ctx, sender, outbox, discardWorkerLogger{})
	}()
	select {
	case sent := <-sender.sent:
		if sent.header != "#群聊\nrenamed" {
			t.Fatalf("unexpected realtime header: %q", sent.header)
		}
		if strings.Contains(sent.header, "发送了") || len(sent.entries) != 1 || !strings.Contains(sent.entries[0].Text, "hello") {
			t.Fatalf("unexpected Telegram summary: %#v", sent)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification worker did not send queued message")
	}
	deadline := time.Now().Add(2 * time.Second)
	for outbox.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if outbox.Len() != 0 {
		t.Fatalf("sent message remains in outbox: %d", outbox.Len())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notification worker did not stop")
	}
}

type assertError string

func (e assertError) Error() string { return string(e) }

func mustFileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s failed: %v", path, err)
	}
	return info.Mode().Perm()
}
