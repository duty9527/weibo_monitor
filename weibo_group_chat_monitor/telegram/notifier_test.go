package telegram

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weibo_group_chat_monitor/config"
	"weibo_group_chat_monitor/weibo"
)

func TestFormatRecordMessageOriginal(t *testing.T) {
	record := &weibo.WeiboRecord{
		CreatedAt: "Thu Mar 26 18:22:52 +0800 2026",
		Text:      "这是一条原创微博",
		SourceURL: "https://weibo.com/1401527553/abc123",
	}

	got := formatRecordMessage(record)

	if !strings.Contains(got, "#原创 【26-03-26 18:22:52】") {
		t.Fatalf("unexpected header: %q", got)
	}
	if !strings.HasSuffix(got, record.SourceURL) {
		t.Fatalf("expected source url at end: %q", got)
	}
}

func TestFormatRecordMessageRetweet(t *testing.T) {
	record := &weibo.WeiboRecord{
		CreatedAt: "2026-03-26 18:22:52",
		Text:      "转发内容",
		SourceURL: "https://weibo.com/detail/123",
		IsRetweet: true,
	}

	got := formatRecordMessage(record)

	if !strings.Contains(got, "#转发 【26-03-26 18:22:52】") {
		t.Fatalf("unexpected header: %q", got)
	}
}

func TestFormatRecordMessageAddsSeparatorAfterURL(t *testing.T) {
	record := &weibo.WeiboRecord{
		CreatedAt: "2026-03-26 18:22:52",
		Text:      "查看链接https://example.com/abc中文继续",
	}

	got := formatRecordMessage(record)

	if !strings.Contains(got, "https://example.com/abc 中文继续") {
		t.Fatalf("expected url separator in message: %q", got)
	}
}

func TestFormatRecordMessageAddsSeparatorBeforeBracketAfterURL(t *testing.T) {
	record := &weibo.WeiboRecord{
		CreatedAt: "2026-03-26 18:22:52",
		Text:      "查看链接https://example.com/abc（附图）",
	}

	got := formatRecordMessage(record)

	if !strings.Contains(got, "https://example.com/abc （附图）") {
		t.Fatalf("expected bracket separator in message: %q", got)
	}
}

func TestFormatRecordMessageIncludesSkippedMediaNotice(t *testing.T) {
	record := &weibo.WeiboRecord{
		CreatedAt:         "2026-03-26 18:22:52",
		Text:              "转发内容",
		SkippedMediaCount: 2,
	}

	got := formatRecordMessage(record)

	if !strings.Contains(got, "提示：相关媒体已在前文中发送，本次跳过重复发送（2 个）") {
		t.Fatalf("expected skipped media notice in message: %q", got)
	}
}

func TestFormatRecordMessageSeparatesTrailingURLPunctuation(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "ascii punctuation",
			text: "链接https://example.com/abc,继续",
			want: "https://example.com/abc , 继续",
		},
		{
			name: "cjk punctuation",
			text: "链接https://example.com/abc。后文",
			want: "https://example.com/abc 。后文",
		},
		{
			name: "closing bracket",
			text: "链接https://example.com/abc)后文",
			want: "https://example.com/abc ) 后文",
		},
		{
			name: "quote",
			text: "链接https://example.com/abc”后文",
			want: "https://example.com/abc ”后文",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := &weibo.WeiboRecord{
				CreatedAt: "2026-03-26 18:22:52",
				Text:      tc.text,
			}

			got := formatRecordMessage(record)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("expected %q in message: %q", tc.want, got)
			}
		})
	}
}

func TestSendMediaGroupSetsShowCaptionAboveMediaForAllItems(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	first := filepath.Join(dir, "1.jpg")
	second := filepath.Join(dir, "2.jpg")
	if err := os.WriteFile(first, []byte("a"), 0o644); err != nil {
		t.Fatalf("write first media: %v", err)
	}
	if err := os.WriteFile(second, []byte("b"), 0o644); err != nil {
		t.Fatalf("write second media: %v", err)
	}

	var got []inputMedia
	client := NewClient(config.TelegramConfig{
		Enabled: true,
		ChatID:  "123",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/bot/sendMediaGroup" {
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}

			mediaJSON := readMultipartField(t, r, "media")
			if err := json.Unmarshal([]byte(mediaJSON), &got); err != nil {
				t.Fatalf("unmarshal media: %v", err)
			}

			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		}),
	}

	err := client.sendMediaGroup(context.Background(), []mediaItem{
		{Path: first, Type: "photo", Field: "photo"},
		{Path: second, Type: "photo", Field: "photo"},
	}, "caption")
	if err != nil {
		t.Fatalf("sendMediaGroup failed: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 media entries, got %d", len(got))
	}
	if !got[0].ShowCaptionAboveMedia || !got[1].ShowCaptionAboveMedia {
		t.Fatalf("expected show_caption_above_media=true for all items, got %#v", got)
	}
	if got[0].Caption != "caption" {
		t.Fatalf("expected first item caption, got %#v", got[0])
	}
	if got[1].Caption != "" {
		t.Fatalf("expected only first item to carry caption, got %#v", got[1])
	}
}

func TestSendRecordWithoutFailedMediaDoesNotSendFallback(t *testing.T) {
	t.Helper()

	var texts []string
	client := NewClient(config.TelegramConfig{
		Enabled: true,
		ChatID:  "123",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/bot/sendMessage" {
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
			texts = append(texts, readFormField(t, r, "text"))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		}),
	}

	record := &weibo.WeiboRecord{
		Text:      "测试正文",
		SourceURL: "https://weibo.com/detail/123",
		MediaURLs: []string{"https://wx1.sinaimg.cn/mw2000/example.jpg"},
	}

	if err := client.SendRecord(context.Background(), record); err != nil {
		t.Fatalf("SendRecord failed: %v", err)
	}

	if len(texts) != 1 {
		t.Fatalf("expected 1 text message, got %d: %#v", len(texts), texts)
	}
	if strings.Contains(texts[0], "以下媒体未成功下载") {
		t.Fatalf("unexpected fallback text: %q", texts[0])
	}
}

func TestSendRecordUsesFailedMediaURLsForFallback(t *testing.T) {
	t.Helper()

	var texts []string
	client := NewClient(config.TelegramConfig{
		Enabled: true,
		ChatID:  "123",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/bot/sendMessage" {
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
			texts = append(texts, readFormField(t, r, "text"))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		}),
	}

	record := &weibo.WeiboRecord{
		Text:            "测试正文",
		FailedMediaURLs: []string{"https://wx1.sinaimg.cn/mw2000/example.jpg"},
	}

	if err := client.SendRecord(context.Background(), record); err != nil {
		t.Fatalf("SendRecord failed: %v", err)
	}

	if len(texts) != 2 {
		t.Fatalf("expected 2 text messages, got %d: %#v", len(texts), texts)
	}
	if !strings.Contains(texts[1], record.FailedMediaURLs[0]) {
		t.Fatalf("expected fallback to contain failed media URL, got %q", texts[1])
	}
}

func readMultipartField(t *testing.T, r *http.Request, name string) string {
	t.Helper()

	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse content type: %v", err)
	}
	if mediaType != "multipart/form-data" {
		t.Fatalf("unexpected content type: %s", mediaType)
	}

	reader := multipart.NewReader(r.Body, params["boundary"])
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read multipart part: %v", err)
		}

		if part.FormName() != name {
			continue
		}

		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read multipart field %s: %v", name, err)
		}
		return string(body)
	}

	t.Fatalf("multipart field %s not found", name)
	return ""
}

func readFormField(t *testing.T, r *http.Request, name string) string {
	t.Helper()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read form body: %v", err)
	}

	values, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatalf("parse form body: %v", err)
	}
	return values.Get(name)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}
