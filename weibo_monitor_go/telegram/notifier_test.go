package telegram

import (
	"strings"
	"testing"

	"weibo_monitor/weibo"
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
