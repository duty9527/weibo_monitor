package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type recordingRuntimeAlertSender struct {
	messages []string
	err      error
}

func (s *recordingRuntimeAlertSender) SendText(_ context.Context, message string) error {
	if s.err != nil {
		return s.err
	}
	s.messages = append(s.messages, message)
	return nil
}

func TestRuntimeAuthAlertIsSentOnceUntilRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-alert.json")
	sender := &recordingRuntimeAlertSender{}
	sent, err := notifyGroupChatAuthFailureOnce(t.Context(), sender, path, errors.New("cookie invalid"))
	if err != nil || !sent {
		t.Fatalf("first alert: sent=%v err=%v", sent, err)
	}
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0], "#运行异常") || !strings.Contains(sender.messages[0], "cookie invalid") {
		t.Fatalf("unexpected alert: %#v", sender.messages)
	}
	if mode := mustFileMode(t, path); mode != 0o600 {
		t.Fatalf("alert state mode = %o", mode)
	}

	sent, err = notifyGroupChatAuthFailureOnce(t.Context(), sender, path, errors.New("still invalid"))
	if err != nil || sent || len(sender.messages) != 1 {
		t.Fatalf("duplicate alert: sent=%v err=%v messages=%d", sent, err, len(sender.messages))
	}
	if err := clearGroupChatAuthFailureAlert(path); err != nil {
		t.Fatal(err)
	}
	sent, err = notifyGroupChatAuthFailureOnce(t.Context(), sender, path, errors.New("new incident"))
	if err != nil || !sent || len(sender.messages) != 2 {
		t.Fatalf("new incident alert: sent=%v err=%v messages=%d", sent, err, len(sender.messages))
	}
}

func TestRuntimeAuthAlertRetriesWhenTelegramFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-alert.json")
	sender := &recordingRuntimeAlertSender{err: errors.New("telegram unavailable")}
	if sent, err := notifyGroupChatAuthFailureOnce(t.Context(), sender, path, errors.New("cookie invalid")); err == nil || sent {
		t.Fatalf("failed send: sent=%v err=%v", sent, err)
	}
	state, err := loadRuntimeAlertState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Active || state.Sent {
		t.Fatalf("unexpected pending alert state: %#v", state)
	}
	sender.err = nil
	if sent, err := notifyGroupChatAuthFailureOnce(t.Context(), sender, path, errors.New("cookie invalid")); err != nil || !sent {
		t.Fatalf("retry alert: sent=%v err=%v", sent, err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("sent messages = %d", len(sender.messages))
	}
}

func TestCompactRuntimeAlertError(t *testing.T) {
	message := compactRuntimeAlertError(errors.New(strings.Repeat("x", 700) + "\nsecret"))
	if len([]rune(message)) > 601 || strings.Contains(message, "\n") {
		t.Fatalf("error was not compacted: %q", message)
	}
}
