package weibo

import (
	"encoding/json"
	"testing"
)

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
