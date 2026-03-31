package groupchat

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weibo_group_chat_monitor/config"
)

func TestDownloadByFIDViaDirectHTTPSucceeds(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "test.jpg")

	scraper := &Scraper{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		downloadClient: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if got := r.Header.Get("Referer"); got != "https://api.weibo.com/chat/" {
					t.Fatalf("unexpected referer: %q", got)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("image-bytes")),
				}, nil
			}),
		},
	}

	gotPath, err := scraper.downloadByFIDViaDirectHTTP("fid", targetPath, []string{"https://example.com/file"})
	if err != nil {
		t.Fatalf("downloadByFIDViaDirectHTTP failed: %v", err)
	}
	if gotPath != targetPath {
		t.Fatalf("unexpected path: %q", gotPath)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func TestRecordMediaDownloadFailureWritesRawMessage(t *testing.T) {
	dir := t.TempDir()
	scraper := &Scraper{
		cfg: &config.GroupChatModeConfig{
			Output: config.GroupChatOutputConfig{
				HistoryFile: filepath.Join(dir, "clean_history"),
			},
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	msg := ChatMessage{
		ID:       FlexibleString("123"),
		Time:     FlexibleInt64(1710000000),
		FromUser: &ChatUser{ScreenName: "alice"},
		Text:     "hello",
		Raw:      json.RawMessage(`{"id":"123","text":"hello","nested":{"a":1}}`),
	}

	scraper.recordMediaDownloadFailure(msg, "fid", "abc", http.ErrMissingFile)

	recordPath := filepath.Join(dir, "clean_history", "failed_media_messages.jsonl")
	content, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read record file failed: %v", err)
	}
	if !strings.Contains(string(content), `"media_type":"fid"`) {
		t.Fatalf("expected media_type in record: %s", string(content))
	}
	if !strings.Contains(string(content), `"nested":{"a":1}`) {
		t.Fatalf("expected raw message in record: %s", string(content))
	}
}
