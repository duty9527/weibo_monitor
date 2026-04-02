package groupchat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadLocalHistoryRecordsReadsDailyFiles(t *testing.T) {
	dir := t.TempDir()
	historyDir := filepath.Join(dir, "clean_history")
	if err := os.MkdirAll(historyDir, 0o755); err != nil {
		t.Fatalf("mkdir history dir: %v", err)
	}

	writeHistoryFile(t, filepath.Join(historyDir, "2026-04-01.jsonl"), []OutputRecord{
		{ID: "1", Time: "2026-04-01 08:00:00", Date: "2026-04-01", Sender: "alice", Message: "第一条"},
		{ID: "2", Time: "2026-04-01 09:00:00", Date: "2026-04-01", Sender: "bob", Message: "不应命中"},
	})
	writeHistoryFile(t, filepath.Join(historyDir, "2026-04-02.jsonl"), []OutputRecord{
		{ID: "3", Time: "2026-04-02 08:00:00", Date: "2026-04-02", Sender: "alice", Message: "第二条"},
	})

	records, err := LoadLocalHistoryRecords(filepath.Join(dir, "clean_history"), LocalHistoryReadOptions{
		TargetSenders: []string{"alice"},
	})
	if err != nil {
		t.Fatalf("LoadLocalHistoryRecords failed: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if records[0].ID != "1" || records[1].ID != "3" {
		t.Fatalf("unexpected record order: %#v", records)
	}
}

func TestLoadLocalHistoryRecordsSupportsLegacyFileDateRangeAndLimit(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "clean_history.jsonl")

	writeHistoryFile(t, legacyPath, []OutputRecord{
		{ID: "1", Time: "2026-03-31 08:00:00", Date: "2026-03-31", Sender: "alice", Message: "太早"},
		{ID: "2", Time: "2026-04-01 09:00:00", Date: "2026-04-01", Sender: "alice", Message: "命中 1"},
		{ID: "3", Time: "2026-04-02 10:00:00", Date: "2026-04-02", Sender: "alice", Message: "命中 2"},
	})

	records, err := LoadLocalHistoryRecords(legacyPath, LocalHistoryReadOptions{
		TargetSenders: []string{"alice"},
		StartDate:     "2026-04-01",
		EndDate:       "2026-04-02",
		MaxRecords:    1,
	})
	if err != nil {
		t.Fatalf("LoadLocalHistoryRecords failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].ID != "3" {
		t.Fatalf("expected latest record after limit, got %#v", records)
	}
}

func TestLoadLocalHistoryRecordsDedupesLegacyAndDailyFiles(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "clean_history.jsonl")
	historyDir := dir

	writeHistoryFile(t, legacyPath, []OutputRecord{
		{ID: "1", Time: "2026-04-01 08:00:00", Date: "2026-04-01", Sender: "alice", Message: "legacy"},
	})
	writeHistoryFile(t, filepath.Join(historyDir, "2026-04-01.jsonl"), []OutputRecord{
		{ID: "1", Time: "2026-04-01 08:00:00", Date: "2026-04-01", Sender: "alice", Message: "daily"},
		{ID: "2", Time: "2026-04-01 09:00:00", Date: "2026-04-01", Sender: "alice", Message: "next"},
	})

	records, err := LoadLocalHistoryRecords(legacyPath, LocalHistoryReadOptions{
		TargetSenders: []string{"alice"},
	})
	if err != nil {
		t.Fatalf("LoadLocalHistoryRecords failed: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 deduped records, got %d", len(records))
	}
	if records[0].ID != "1" || records[1].ID != "2" {
		t.Fatalf("unexpected records: %#v", records)
	}
}

func writeHistoryFile(t *testing.T, path string, records []OutputRecord) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	lines := make([]string, 0, len(records))
	for _, record := range records {
		payload, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal record: %v", err)
		}
		lines = append(lines, string(payload))
	}

	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write history file: %v", err)
	}
}
