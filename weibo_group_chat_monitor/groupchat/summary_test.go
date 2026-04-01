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
			ID:              "2",
			Time:            "2026-03-31 08:01:00",
			Sender:          "alice",
			Message:         "早上好",
			HasImage:        true,
			DownloadedMedia: stringPtr("/tmp/a.jpg, /tmp/b.mp4"),
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

func TestBuildSenderSummariesCollectsMediaPaths(t *testing.T) {
	now := time.Date(2026, 3, 31, 9, 0, 0, 0, time.Local)
	records := []OutputRecord{
		{
			ID:              "1",
			Time:            "2026-03-31 08:00:00",
			Sender:          "alice",
			Message:         "第一条",
			DownloadedMedia: stringPtr("/tmp/a.jpg"),
		},
		{
			ID:              "2",
			Time:            "2026-03-31 08:01:00",
			Sender:          "alice",
			Message:         "第二条",
			DownloadedMedia: stringPtr("/tmp/a.jpg, /tmp/b.pdf"),
		},
	}

	summaries := BuildSenderSummaries(now, records, []string{"alice"})
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}

	if summaries[0].Header == "" {
		t.Fatalf("expected non-empty header")
	}
	if len(summaries[0].Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(summaries[0].Entries))
	}

	got := summaries[0].Entries[1].MediaPaths
	if len(got) != 2 {
		t.Fatalf("expected 2 unique media paths, got %d: %#v", len(got), got)
	}
	if got[0] != "/tmp/a.jpg" || got[1] != "/tmp/b.pdf" {
		t.Fatalf("unexpected media paths: %#v", got)
	}
	if summaries[0].Entries[0].Text != "08:00:00 第一条[图片]" {
		t.Fatalf("unexpected first entry text: %q", summaries[0].Entries[0].Text)
	}
}

func stringPtr(value string) *string {
	return &value
}
