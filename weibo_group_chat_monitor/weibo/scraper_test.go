package weibo

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"weibo_group_chat_monitor/config"
)

func newTestScraper(t *testing.T, mediaDir string) *Scraper {
	t.Helper()
	cfg := &config.WeiboModeConfig{
		Weibo: config.WeiboConfig{
			TargetUID: "1401527553",
			MediaDir:  mediaDir,
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scraper, err := NewScraper(cfg, logger)
	if err != nil {
		t.Fatalf("NewScraper failed: %v", err)
	}
	scraper.mediaRetryDelay = time.Millisecond
	return scraper
}

func TestExtractMediaAcceptsNumericVideoType(t *testing.T) {
	item := &WeiboItem{
		PageInfo: &PageInfo{
			Type: "11",
			MediaInfo: &MediaInfo{
				MP4720P:  "https://example.test/video-720.mp4",
				MP4HDURL: "https://example.test/video-hd.mp4",
			},
		},
	}

	got := extractMedia(item)
	if len(got) != 1 || got[0] != "https://example.test/video-720.mp4" {
		t.Fatalf("unexpected media URLs: %#v", got)
	}
}

func TestExtractMediaSupportsMixedImagesAndVideos(t *testing.T) {
	raw := json.RawMessage(`{
		"items": [
			{"type":"pic","data":{"mw2000":{"url":"https://example.test/image.jpg"}}},
			{"type":"video","data":{"page_info":{"type":11,"page_pic":{"large":{"url":"https://example.test/video-cover.jpg"}},"media_info":{"mp4_hd_url":"https://example.test/video.mp4"}}}}
		]
	}`)
	item := &WeiboItem{MixMediaInfo: raw}

	got := extractMedia(item)
	want := []string{"https://example.test/image.jpg", "https://example.test/video.mp4"}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}
}

func TestMediaFilenameUsesResponseContentType(t *testing.T) {
	got := mediaFilename("https://example.test/playback?id=1", "video/mp4")
	if got != "playback.mp4" {
		t.Fatalf("unexpected filename: %q", got)
	}

	got = mediaFilename("https://example.test/playback.bin?id=1", "video/mp4")
	if got != "playback.mp4" {
		t.Fatalf("unexpected corrected filename: %q", got)
	}
}

func TestFailedMediaURLsArePersisted(t *testing.T) {
	record := WeiboRecord{ID: "1", FailedMediaURLs: []string{"https://example.test/video.mp4"}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}

	var restored WeiboRecord
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.FailedMediaURLs) != 1 || restored.FailedMediaURLs[0] != record.FailedMediaURLs[0] {
		t.Fatalf("failed media URLs were not persisted: %s", data)
	}
}

func TestDownloadMediaRetriesTransientFailure(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("bad gateway"))
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("mp4-payload"))
	}))
	defer server.Close()

	mediaDir := t.TempDir()
	scraper := newTestScraper(t, mediaDir)
	got := scraper.DownloadMedia(server.URL + "/sample.mp4?Expires=123")
	want := filepath.Join(mediaDir, "sample.mp4")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if attempts.Load() != 3 {
		t.Fatalf("got %d attempts, want 3", attempts.Load())
	}
	content, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(content) != "mp4-payload" {
		t.Fatalf("unexpected file content: %q", content)
	}
}

func TestDownloadMediaRetriesMidStreamReset(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		w.Header().Set("Content-Type", "video/mp4")
		if n == 1 {
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("partial"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			if hijacker, ok := w.(http.Hijacker); ok {
				conn, _, err := hijacker.Hijack()
				if err == nil {
					_ = conn.Close()
					return
				}
			}
			return
		}
		_, _ = w.Write([]byte("complete-video"))
	}))
	defer server.Close()

	mediaDir := t.TempDir()
	scraper := newTestScraper(t, mediaDir)
	got := scraper.DownloadMedia(server.URL + "/clip.mp4")
	if got == "" {
		t.Fatalf("expected retry to recover mid-stream reset")
	}
	if attempts.Load() != 2 {
		t.Fatalf("got %d attempts, want 2", attempts.Load())
	}
	entries, err := os.ReadDir(mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".media-") {
			t.Fatalf("leftover temp file found: %s", entry.Name())
		}
	}
	content, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "complete-video" {
		t.Fatalf("unexpected file content: %q", content)
	}
}

func TestDownloadMediaDoesNotRetry404(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	scraper := newTestScraper(t, t.TempDir())
	if got := scraper.DownloadMedia(server.URL + "/missing.jpg"); got != "" {
		t.Fatalf("expected empty path on 404, got %q", got)
	}
	if attempts.Load() != 1 {
		t.Fatalf("got %d attempts, want 1", attempts.Load())
	}
}

func TestDownloadMediaSkipsRequestWhenKnownFileExists(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	mediaDir := t.TempDir()
	existing := filepath.Join(mediaDir, "existing.jpg")
	if err := os.WriteFile(existing, []byte("cached"), 0o600); err != nil {
		t.Fatal(err)
	}

	scraper := newTestScraper(t, mediaDir)
	got := scraper.DownloadMedia(server.URL + "/existing.jpg")
	if got != existing {
		t.Fatalf("got %q, want %q", got, existing)
	}
	if attempts.Load() != 0 {
		t.Fatalf("expected 0 HTTP attempts for cached file, got %d", attempts.Load())
	}
}

func TestDownloadMediaDoesNotLeakCookieToNonWeiboHost(t *testing.T) {
	var seenCookie string
	var seenReferer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenCookie = r.Header.Get("Cookie")
		seenReferer = r.Header.Get("Referer")
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpg-data"))
	}))
	defer server.Close()

	scraper := newTestScraper(t, t.TempDir())
	scraper.SetCookies("SUB=secret; XSRF-TOKEN=tok")
	got := scraper.DownloadMedia(server.URL + "/photo.jpg")
	if got == "" {
		t.Fatalf("expected download to succeed")
	}
	if seenCookie != "" {
		t.Fatalf("expected no Cookie header to non-weibo host, got %q", seenCookie)
	}
	if seenReferer != "https://weibo.com/" {
		t.Fatalf("got Referer %q, want https://weibo.com/", seenReferer)
	}
}

func TestNormalizeMediaDownloadURLAndCookieScoping(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   "http://f.video.weibocdn.com/o0/abc.mp4?Expires=123",
			want: "https://f.video.weibocdn.com/o0/abc.mp4?Expires=123",
		},
		{
			in:   "http://wx1.sinaimg.cn/mw2000/abc.jpg",
			want: "https://wx1.sinaimg.cn/mw2000/abc.jpg",
		},
		{
			in:   "http://127.0.0.1:8080/test.mp4",
			want: "http://127.0.0.1:8080/test.mp4",
		},
	}
	for _, tc := range cases {
		if got := normalizeMediaDownloadURL(tc.in); got != tc.want {
			t.Fatalf("normalizeMediaDownloadURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	cookieHosts := map[string]bool{
		"weibo.com":            true,
		"wx.weibo.com":         true,
		"weibo.cn":             true,
		"m.weibo.cn":           true,
		"weibocdn.com":         false,
		"f.video.weibocdn.com": false,
		"wx1.sinaimg.cn":       false,
		"evilweibo.com":        false,
		"127.0.0.1":            false,
	}
	for host, want := range cookieHosts {
		if got := shouldSendWeiboCookie(host); got != want {
			t.Fatalf("shouldSendWeiboCookie(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestExtractMediaSkipsDuplicatePicIDWhenPresentInMixMedia(t *testing.T) {
	raw := json.RawMessage(`{
		"items": [
			{"type":"pic","data":{"mw2000":{"url":"https://wx4.sinaimg.cn/mw2000/53899d01ly1ijaz3rgxo0j20np0s3ds9.jpg"}}},
			{"type":"video","data":{"media_info":{"mp4_720p_mp4":"http://f.video.weibocdn.com/o0/xAL7Yntzlx08CzdMAhLO01041200nVr60E010.mp4?label=mp4_720p"}}}
		]
	}`)
	item := &WeiboItem{
		PicIDs:       []string{"53899d01ly1ijaz3rgxo0j20np0s3ds9", "53899d01ly1extra"},
		MixMediaInfo: raw,
	}

	got := extractMedia(item)
	want := []string{
		"https://wx4.sinaimg.cn/mw2000/53899d01ly1ijaz3rgxo0j20np0s3ds9.jpg",
		"http://f.video.weibocdn.com/o0/xAL7Yntzlx08CzdMAhLO01041200nVr60E010.mp4?label=mp4_720p",
		"https://wx1.sinaimg.cn/mw2000/53899d01ly1extra.jpg",
	}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}
}

func TestFetchPageRejectsNonOKAPIResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":-100,"url":"https://weibo.com/login.php"}`))
	}))
	defer server.Close()

	scraper := newTestScraper(t, t.TempDir())
	scraper.apiBaseURL = server.URL

	_, err := scraper.fetchPage(context.Background(), 1)
	if err == nil {
		t.Fatalf("expected error on ok=-100 response")
	}
	if !strings.Contains(err.Error(), "ok=-100") {
		t.Fatalf("unexpected error: %v", err)
	}
}
