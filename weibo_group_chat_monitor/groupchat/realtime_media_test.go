package groupchat

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weibo_group_chat_monitor/config"
)

func TestHasMediaAuthenticationFailure(t *testing.T) {
	for _, err := range []error{
		ErrMediaAuthentication,
		errors.New(`HTTP 403: {"error":"auth failed! sub invalid"}`),
		errors.New("HTTP 401"),
	} {
		if !HasMediaAuthenticationFailure([]MediaDownloadFailure{{Err: err}}) {
			t.Fatalf("expected authentication failure for %v", err)
		}
	}
	if HasMediaAuthenticationFailure([]MediaDownloadFailure{{Err: errors.New("HTTP 404")}}) {
		t.Fatal("ordinary media failure must not be classified as authentication failure")
	}
}

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

func TestRealtimeMediaDownloaderAllowsTextFIDDocument(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/csv;charset=utf-8")
		_, _ = w.Write([]byte("name,value\nfoo,42\n"))
	}))
	defer server.Close()

	dir := t.TempDir()
	downloader := NewRealtimeMediaDownloader(&config.GroupChatModeConfig{
		Chat:   config.ChatConfig{DirectDownloadTimeoutSeconds: 2},
		Output: config.GroupChatOutputConfig{MediaDir: dir},
	}, nil)
	downloader.client = server.Client()
	path := filepath.Join(dir, "report.csv")
	got, err := downloader.downloadURLToPath(server.URL+"/report.csv", path, "https://api.weibo.com/chat/", true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "name,value\nfoo,42\n" {
		t.Fatalf("unexpected document content: %q", data)
	}
}

func TestRealtimeMediaDownloaderRejectsTextForPageMedia(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html;charset=utf-8")
		_, _ = w.Write([]byte("<html>not an image</html>"))
	}))
	defer server.Close()

	downloader := NewRealtimeMediaDownloader(&config.GroupChatModeConfig{
		Chat:   config.ChatConfig{DirectDownloadTimeoutSeconds: 2},
		Output: config.GroupChatOutputConfig{MediaDir: t.TempDir()},
	}, nil)
	downloader.client = server.Client()
	_, err := downloader.downloadURLToPath(server.URL+"/bad.jpg", filepath.Join(t.TempDir(), "bad.jpg"), "https://weibo.com/", false)
	if err == nil || !strings.Contains(err.Error(), "媒体响应类型异常") {
		t.Fatalf("unexpected page media error: %v", err)
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
