package groupchat

import (
	"strings"
	"testing"
	"time"
)

func TestBuildSenderSummaryMessages(t *testing.T) {
	now := time.Date(2026, 3, 31, 9, 0, 0, 0, time.Local)
	records := []OutputRecord{
		{
			ID:       "2",
			Time:     "2026-03-31 08:01:00",
			Sender:   "alice",
			Message:  "早上好",
			HasImage: true,
		},
		{
			ID:      "3",
			Time:    "2026-03-31 08:02:00",
			Sender:  "bob",
			Message: "不应被推送",
		},
		{
			ID:      "1",
			Time:    "2026-03-31 08:00:00",
			Sender:  "alice",
			Message: "第一条",
		},
	}

	messages := BuildSenderSummaryMessages(now, records, []string{"alice"})
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}

	got := messages[0]
	if !strings.Contains(got, "2026年03月31日") {
		t.Fatalf("expected date header, got %q", got)
	}
	if !strings.Contains(got, "alice发送了2条消息") {
		t.Fatalf("expected sender summary, got %q", got)
	}
	if !strings.Contains(got, "08:00:00 第一条") {
		t.Fatalf("expected first line, got %q", got)
	}
	if !strings.Contains(got, "08:01:00 早上好[图片]") {
		t.Fatalf("expected image marker, got %q", got)
	}
}
