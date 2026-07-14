package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/playwright-community/playwright-go"
)

func TestExtractWeiboID(t *testing.T) {
	cases := map[string]string{
		"https://weibo.com/1401527553/NB4vXy3aP":       "NB4vXy3aP",
		"https://weibo.com/detail/4962291583582458":    "4962291583582458",
		"https://m.weibo.cn/status/NB4vXy3aP":          "NB4vXy3aP",
		"https://weibo.com/u/123?mid=5033881810701546": "5033881810701546",
	}

	for input, want := range cases {
		got, err := extractWeiboID(input)
		if err != nil {
			t.Fatalf("extractWeiboID(%q) returned error: %v", input, err)
		}
		if got != want {
			t.Fatalf("extractWeiboID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCleanHTML(t *testing.T) {
	input := "<p>Hello<br />World</p>&nbsp;<a href=\"https://weibo.com\">link</a>"
	want := "Hello\nWorld\n link"
	if got := cleanHTML(input); got != want {
		t.Fatalf("cleanHTML() = %q, want %q", got, want)
	}
}

func TestSplitText(t *testing.T) {
	got := splitText("第一行\n第二行\n第三行", 5)
	want := []string{"第一行", "第二行", "第三行"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitText() = %#v, want %#v", got, want)
	}
}

func TestSerializeCookies(t *testing.T) {
	cookies := []playwright.Cookie{
		{Name: "SUB", Value: "a"},
		{Name: "SUBP", Value: "b"},
		{Name: "SUB", Value: "override-ignored"},
	}
	want := "SUB=a; SUBP=b"
	if got := serializeCookies(cookies); got != want {
		t.Fatalf("serializeCookies() = %q, want %q", got, want)
	}
}

func TestFlexibleStringUnmarshal(t *testing.T) {
	var got FlexibleString
	if err := json.Unmarshal([]byte(`"video"`), &got); err != nil {
		t.Fatalf("unmarshal string failed: %v", err)
	}
	if got != "video" {
		t.Fatalf("got %q, want %q", got, "video")
	}

	if err := json.Unmarshal([]byte(`123`), &got); err != nil {
		t.Fatalf("unmarshal number failed: %v", err)
	}
	if got != "123" {
		t.Fatalf("got %q, want %q", got, "123")
	}
}

func TestEnsureUserDataDirAvailable(t *testing.T) {
	dir := t.TempDir()
	if err := ensureUserDataDirAvailable(dir); err != nil {
		t.Fatalf("ensureUserDataDirAvailable() returned error: %v", err)
	}
}

func TestEnsureUserDataDirAvailableWithLockFiles(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "SingletonLock")
	if err := os.WriteFile(lockPath, []byte("locked"), 0o644); err != nil {
		t.Fatalf("write lock file failed: %v", err)
	}

	err := ensureUserDataDirAvailable(dir)
	if err == nil {
		t.Fatal("ensureUserDataDirAvailable() expected error, got nil")
	}
	if want := "正在被其他进程占用"; !strings.Contains(err.Error(), want) {
		t.Fatalf("ensureUserDataDirAvailable() error = %q, want substring %q", err.Error(), want)
	}
}

func TestShouldAutoScrapeTopic(t *testing.T) {
	cfg := Config{
		Telegram: TelegramConfig{
			AutoScrapeTopics: []TelegramTopicWatchConfig{
				{ChatID: -1001, TopicIDs: []int64{11, 22}},
				{ChatID: -1002},
			},
		},
	}

	if !cfg.shouldAutoScrapeTopic(-1001, 11) {
		t.Fatal("expected configured topic to match")
	}
	if cfg.shouldAutoScrapeTopic(-1001, 99) {
		t.Fatal("unexpected match for unconfigured topic")
	}
	if !cfg.shouldAutoScrapeTopic(-1002, 123) {
		t.Fatal("expected chat-wide topic rule to match")
	}
	if cfg.shouldAutoScrapeTopic(-1003, 11) {
		t.Fatal("unexpected match for unconfigured chat")
	}
	if cfg.shouldAutoScrapeTopic(-1001, 0) {
		t.Fatal("unexpected match for non-topic message")
	}
}

func TestMessageTextUsesCaptionFallback(t *testing.T) {
	msg := TelegramMessage{Caption: " https://weibo.com/abc "}
	if got := messageText(msg); got != "https://weibo.com/abc" {
		t.Fatalf("messageText() = %q, want %q", got, "https://weibo.com/abc")
	}
}

func TestResolvePathsUsesConfigDirectory(t *testing.T) {
	cfg := Config{
		Weibo: WeiboConfig{
			CookieFile:    "cookies.txt",
			DownloadDir:   "downloads",
			SaveRecordDir: "records",
			UserDataDir:   "weibo_user_data",
		},
	}

	configPath := filepath.Join("/tmp/project/conf", "config.yaml")
	if err := cfg.resolvePaths(configPath); err != nil {
		t.Fatalf("resolvePaths() returned error: %v", err)
	}

	if got, want := cfg.Weibo.CookieFile, "/tmp/project/conf/cookies.txt"; got != want {
		t.Fatalf("CookieFile = %q, want %q", got, want)
	}
	if got, want := cfg.Weibo.DownloadDir, "/tmp/project/conf/downloads"; got != want {
		t.Fatalf("DownloadDir = %q, want %q", got, want)
	}
	if got, want := cfg.Weibo.SaveRecordDir, "/tmp/project/conf/records"; got != want {
		t.Fatalf("SaveRecordDir = %q, want %q", got, want)
	}
	if got, want := cfg.Weibo.UserDataDir, "/tmp/project/conf/weibo_user_data"; got != want {
		t.Fatalf("UserDataDir = %q, want %q", got, want)
	}
}
