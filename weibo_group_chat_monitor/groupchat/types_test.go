package groupchat

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"weibo_group_chat_monitor/config"
)

func TestFlexibleStringUnmarshal(t *testing.T) {
	var value FlexibleString
	if err := json.Unmarshal([]byte(`12345`), &value); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if got := value.String(); got != "12345" {
		t.Fatalf("unexpected value: %q", got)
	}
}

func TestChatMessageUnmarshalPreservesRawPayload(t *testing.T) {
	var msg ChatMessage
	raw := []byte(`{"id":"123","time":1700000000,"from_user":{"screen_name":"alice"},"text":"hello","extra":{"k":"v"}}`)
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	payload := msg.RawPayload()
	if !strings.Contains(string(payload), `"extra":{"k":"v"}`) {
		t.Fatalf("expected raw payload to preserve extra fields: %s", string(payload))
	}
}

func TestFlexibleInt64Unmarshal(t *testing.T) {
	var value FlexibleInt64
	if err := json.Unmarshal([]byte(`"1700000000"`), &value); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if got := value.Int64(); got != 1700000000 {
		t.Fatalf("unexpected value: %d", got)
	}
}

func TestFlexibleStringSliceUnmarshal(t *testing.T) {
	var values FlexibleStringSlice
	if err := json.Unmarshal([]byte(`["1", 2, " 3 "]`), &values); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	want := []string{"1", "2", "3"}
	if len(values) != len(want) {
		t.Fatalf("unexpected len: %d", len(values))
	}
	for i := range want {
		if values[i] != want[i] {
			t.Fatalf("unexpected value at %d: %q", i, values[i])
		}
	}
}

func TestNormalizeMediaURL(t *testing.T) {
	if got := normalizeMediaURL("//wx1.sinaimg.cn/test.jpg"); got != "https://wx1.sinaimg.cn/test.jpg" {
		t.Fatalf("unexpected normalized url: %q", got)
	}
}

func TestMatchesStopCondition(t *testing.T) {
	cond := config.StopCondition{
		Enabled:       true,
		TargetTime:    "2026-03-10 15:59:48",
		TargetSender:  "germer_123",
		TargetMessage: "开箱即用模型[doge]",
	}

	if !matchesStopCondition(cond, "2026-03-10 15:59:48", "germer_123", "开箱即用模型[doge]") {
		t.Fatal("expected stop condition to match")
	}

	if matchesStopCondition(cond, "2026-03-10 15:59:48", "other", "开箱即用模型[doge]") {
		t.Fatal("expected stop condition to fail")
	}

	if matchesStopCondition(cond, "2026-03-10 16:00:00", "germer_123", "开箱即用模型[doge]") {
		t.Fatal("expected stop condition to fail when message is newer than boundary")
	}
}

func TestMatchesStopConditionDateBoundaryUsesMidnight(t *testing.T) {
	cond := config.StopCondition{
		Enabled:    true,
		TargetTime: "2026-03-10",
	}

	if !matchesStopCondition(cond, "2026-03-10 00:00:00", "alice", "hello") {
		t.Fatal("expected midnight boundary to match")
	}
	if !matchesStopCondition(cond, "2026-03-09 23:59:59", "alice", "hello") {
		t.Fatal("expected older message to match after crossing date boundary")
	}
	if matchesStopCondition(cond, "2026-03-10 00:00:01", "alice", "hello") {
		t.Fatal("expected newer message to be outside date boundary")
	}
}

func TestSanitizeFilename(t *testing.T) {
	if got := sanitizeFilename(`a/b:c?.jpg`); got != "a_b_c_.jpg" {
		t.Fatalf("unexpected filename: %q", got)
	}
}

func TestBuildOutputRecordAddsNormalizedFields(t *testing.T) {
	record := buildOutputRecord(
		ChatMessage{
			ID:       FlexibleString("1"),
			PageInfo: &ChatPageInfo{Type: FlexibleString("pic")},
		},
		"2026-03-31 08:00:00",
		"alice",
		"你好[doge] https://example.com @bob",
		[]string{"a.jpg"},
	)

	if record.Date != "2026-03-31" || record.Hour != 8 {
		t.Fatalf("unexpected normalized time fields: %+v", record)
	}
	if record.MsgType != "image" {
		t.Fatalf("unexpected msg type: %+v", record)
	}
	if record.TextClean != "你好  @bob" && record.TextClean != "你好 @bob" {
		t.Fatalf("unexpected cleaned text: %q", record.TextClean)
	}
	if !record.HasImage {
		t.Fatalf("expected has_image=true: %+v", record)
	}
}

func TestAppendRecordsSplitsByDay(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "clean_history.jsonl")

	records := []OutputRecord{
		{ID: "2", Time: "2026-03-31 09:00:00", Date: "2026-03-31", Sender: "alice", Message: "b"},
		{ID: "1", Time: "2026-03-31 08:00:00", Date: "2026-03-31", Sender: "alice", Message: "a"},
		{ID: "3", Time: "2026-04-01 08:00:00", Date: "2026-04-01", Sender: "bob", Message: "c"},
	}

	if err := appendRecords(basePath, records); err != nil {
		t.Fatalf("appendRecords failed: %v", err)
	}

	firstDay := filepath.Join(dir, "2026-03-31.jsonl")
	secondDay := filepath.Join(dir, "2026-04-01.jsonl")
	if _, err := os.Stat(firstDay); err != nil {
		t.Fatalf("expected first day file: %v", err)
	}
	if _, err := os.Stat(secondDay); err != nil {
		t.Fatalf("expected second day file: %v", err)
	}

	seen, err := loadSeenIDs(basePath)
	if err != nil {
		t.Fatalf("loadSeenIDs failed: %v", err)
	}
	for _, id := range []string{"1", "2", "3"} {
		if _, ok := seen[id]; !ok {
			t.Fatalf("expected id %s in seen set", id)
		}
	}

	content, err := os.ReadFile(firstDay)
	if err != nil {
		t.Fatalf("read first day file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 2 {
		t.Fatalf("unexpected line count: %d", len(lines))
	}

	var firstRecord OutputRecord
	if err := json.Unmarshal([]byte(lines[0]), &firstRecord); err != nil {
		t.Fatalf("unmarshal first record: %v", err)
	}
	if firstRecord.ID != "1" {
		t.Fatalf("expected earliest record first, got id %s", firstRecord.ID)
	}
}

func TestCollectMessagesStopsWithoutWritingOlderBoundaryRecords(t *testing.T) {
	cfg := &config.GroupChatModeConfig{
		StopCondition: config.StopCondition{
			Enabled:    true,
			TargetTime: "2026-03-31",
		},
	}

	scraper := NewScraper(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	collected := make(map[string]OutputRecord)

	messages := []ChatMessage{
		{ID: FlexibleString("1"), Time: FlexibleInt64(time.Date(2026, 3, 30, 23, 59, 59, 0, time.Local).Unix()), Text: "older"},
		{ID: FlexibleString("2"), Time: FlexibleInt64(time.Date(2026, 3, 31, 0, 0, 30, 0, time.Local).Unix()), Text: "keep 1"},
		{ID: FlexibleString("3"), Time: FlexibleInt64(time.Date(2026, 3, 31, 0, 10, 0, 0, time.Local).Unix()), Text: "keep 2"},
	}

	_, shouldStop, err := scraper.collectMessages(messages, nil, map[string]struct{}{}, collected)
	if err != nil {
		t.Fatalf("collectMessages failed: %v", err)
	}
	if !shouldStop {
		t.Fatal("expected shouldStop=true")
	}
	if len(collected) != 2 {
		t.Fatalf("expected only 2 newer records, got %d", len(collected))
	}
	if _, ok := collected["1"]; ok {
		t.Fatal("expected boundary/older record to be skipped")
	}
}
