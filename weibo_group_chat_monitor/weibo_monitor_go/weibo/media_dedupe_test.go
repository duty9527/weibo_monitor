package weibo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilterSentMediaSkipsPreviouslySentFiles(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.jpg")
	second := filepath.Join(dir, "second.jpg")

	if err := os.WriteFile(first, []byte("first"), 0o644); err != nil {
		t.Fatalf("write first media: %v", err)
	}
	if err := os.WriteFile(second, []byte("second"), 0o644); err != nil {
		t.Fatalf("write second media: %v", err)
	}

	firstKey, err := mediaFingerprint(first)
	if err != nil {
		t.Fatalf("fingerprint first media: %v", err)
	}
	secondKey, err := mediaFingerprint(second)
	if err != nil {
		t.Fatalf("fingerprint second media: %v", err)
	}

	state := &RunState{}
	state.MarkMediaSent([]string{firstKey})

	record := &WeiboRecord{
		LocalMediaPaths: []string{first, second},
	}

	filtered, newKeys, err := FilterSentMedia(record, state)
	if err != nil {
		t.Fatalf("FilterSentMedia failed: %v", err)
	}

	if len(filtered.LocalMediaPaths) != 1 || filtered.LocalMediaPaths[0] != second {
		t.Fatalf("unexpected filtered paths: %#v", filtered.LocalMediaPaths)
	}
	if len(newKeys) != 1 || newKeys[0] != secondKey {
		t.Fatalf("unexpected new keys: %#v", newKeys)
	}
	if len(record.LocalMediaPaths) != 2 {
		t.Fatalf("original record should not be mutated: %#v", record.LocalMediaPaths)
	}
}

func TestFilterSentMediaDedupesSameContentWithinRecord(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.jpg")
	second := filepath.Join(dir, "second.jpg")

	if err := os.WriteFile(first, []byte("same-content"), 0o644); err != nil {
		t.Fatalf("write first media: %v", err)
	}
	if err := os.WriteFile(second, []byte("same-content"), 0o644); err != nil {
		t.Fatalf("write second media: %v", err)
	}

	record := &WeiboRecord{
		LocalMediaPaths: []string{first, second},
	}

	filtered, newKeys, err := FilterSentMedia(record, nil)
	if err != nil {
		t.Fatalf("FilterSentMedia failed: %v", err)
	}

	if len(filtered.LocalMediaPaths) != 1 || filtered.LocalMediaPaths[0] != first {
		t.Fatalf("unexpected filtered paths: %#v", filtered.LocalMediaPaths)
	}
	if len(newKeys) != 1 {
		t.Fatalf("expected one media key, got %#v", newKeys)
	}
}
