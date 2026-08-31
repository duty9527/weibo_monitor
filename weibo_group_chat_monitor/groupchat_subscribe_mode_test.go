package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsSubscriptionAuthError(t *testing.T) {
	for _, message := range []string{"403:auth fail:create_denied", "HTTP 403", "create_denied"} {
		if !isSubscriptionAuthError(assertError(message)) {
			t.Fatalf("expected auth error for %q", message)
		}
	}
	if isSubscriptionAuthError(assertError("connection reset")) {
		t.Fatal("network error must not be classified as auth error")
	}
}

func TestAppendSubscriptionEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events", "events.jsonl")
	event := loggedSubscriptionEvent{ReceivedAt: "2026-08-31T12:00:00+08:00", Channel: "/im/42", Data: json.RawMessage(`{"gid":"123"}`)}
	if err := appendSubscriptionEvent(path, event); err != nil {
		t.Fatalf("appendSubscriptionEvent failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read event log failed: %v", err)
	}
	if !strings.Contains(string(data), `"channel":"/im/42"`) || !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("unexpected event log: %s", data)
	}
	if mode := mustFileMode(t, path); mode != 0o600 {
		t.Fatalf("unexpected event log permissions: %o", mode)
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
