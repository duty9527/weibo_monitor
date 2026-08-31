package groupchat

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weibo_group_chat_monitor/config"
)

func TestRealtimeMediaDownloaderDownloadsPagePicture(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://weibo.com/" {
			t.Errorf("unexpected Referer: %q", r.Header.Get("Referer"))
		}
		if cookie, err := r.Cookie("SUB"); err != nil || cookie.Value != "secret" {
			t.Errorf("expected media cookie, got %q, %v", r.Header.Get("Cookie"), err)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg-data"))
	}))
	defer server.Close()

	dir := t.TempDir()
	cfg := &config.GroupChatModeConfig{
		Chat:   config.ChatConfig{DirectDownloadTimeoutSeconds: 2},
		Output: config.GroupChatOutputConfig{MediaDir: dir},
	}
	downloader := NewRealtimeMediaDownloader(cfg, []StoredCookie{{Name: "SUB", Value: "secret", Domain: "127.0.0.1"}})
	downloader.client = server.Client()
	paths, failures := downloader.DownloadMessage(ChatMessage{
		ID: FlexibleString("123"),
		PageInfo: &ChatPageInfo{
			Type:    FlexibleString("pic"),
			PagePic: &ChatPagePic{URL: server.URL + "/photo.jpg"},
		},
	})
	if len(failures) != 0 || len(paths) != 1 {
		t.Fatalf("paths=%v failures=%v", paths, failures)
	}
	if filepath.Base(paths[0]) != "123_image.jpg" {
		t.Fatalf("unexpected media path: %q", paths[0])
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "jpeg-data" {
		t.Fatalf("unexpected media content: %q", data)
	}
}

func TestRealtimeMediaDownloaderBuildsLegacyFIDCandidates(t *testing.T) {
	downloader := NewRealtimeMediaDownloader(&config.GroupChatModeConfig{
		Chat: config.ChatConfig{Source: "123", GroupID: "456"},
	}, nil)
	urls := downloader.fidCandidateURLs("abc")
	if len(urls) != 3 {
		t.Fatalf("candidate count = %d", len(urls))
	}
	if !strings.Contains(urls[0], "fid=abc") || !strings.Contains(urls[0], "source=123") || !strings.Contains(urls[1], "touid=456") {
		t.Fatalf("unexpected candidates: %v", urls)
	}
}
