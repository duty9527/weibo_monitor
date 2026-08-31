package groupchat

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"weibo_group_chat_monitor/config"
)

type MediaDownloadFailure struct {
	MediaType string
	MediaRef  string
	Err       error
}

type RealtimeMediaDownloader struct {
	cfg     *config.GroupChatModeConfig
	cookies []StoredCookie
	client  *http.Client
}

func NewRealtimeMediaDownloader(cfg *config.GroupChatModeConfig, cookies []StoredCookie) *RealtimeMediaDownloader {
	timeout := time.Duration(cfg.Chat.DirectDownloadTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &RealtimeMediaDownloader{
		cfg:     cfg,
		cookies: append([]StoredCookie(nil), cookies...),
		client:  &http.Client{Timeout: timeout},
	}
}

func (d *RealtimeMediaDownloader) DownloadMessage(msg ChatMessage) ([]string, []MediaDownloadFailure) {
	if d == nil || d.cfg == nil {
		return nil, nil
	}
	if err := os.MkdirAll(d.cfg.Output.MediaDir, 0o755); err != nil {
		return nil, []MediaDownloadFailure{{MediaType: "output_dir", MediaRef: d.cfg.Output.MediaDir, Err: err}}
	}
	var paths []string
	var failures []MediaDownloadFailure
	for _, fid := range msg.FIDs {
		path, err := d.downloadFID(fid)
		if err != nil {
			failures = append(failures, MediaDownloadFailure{MediaType: "fid", MediaRef: fid, Err: err})
			continue
		}
		if path != "" {
			paths = append(paths, path)
		}
	}
	if msg.PageInfo != nil {
		switch msg.PageInfo.Type.String() {
		case "pic":
			mediaURL := normalizeMediaURL(msg.PageInfo.PreferredPictureURL())
			if mediaURL != "" {
				path, err := d.downloadURL(mediaURL, sanitizeFilename(msg.IDString()+"_image.jpg"), "https://weibo.com/")
				if err != nil {
					failures = append(failures, MediaDownloadFailure{MediaType: "page_pic", MediaRef: mediaURL, Err: err})
				} else if path != "" {
					paths = append(paths, path)
				}
			}
		case "video":
			mediaURL := normalizeMediaURL(msg.PageInfo.PreferredVideoURL())
			if mediaURL != "" {
				path, err := d.downloadURL(mediaURL, sanitizeFilename(msg.IDString()+"_video.mp4"), "https://weibo.com/")
				if err != nil {
					failures = append(failures, MediaDownloadFailure{MediaType: "page_video", MediaRef: mediaURL, Err: err})
				} else if path != "" {
					paths = append(paths, path)
				}
			}
		}
	}
	return uniqueStrings(paths), failures
}

func (d *RealtimeMediaDownloader) downloadFID(fid string) (string, error) {
	fid = strings.TrimSpace(fid)
	if fid == "" {
		return "", nil
	}
	meta, _ := d.queryFIDMeta(fid)
	ext := strings.TrimPrefix(strings.TrimSpace(meta.Extension), ".")
	if ext == "" {
		ext = "jpg"
	}
	originalName := sanitizeFilename(meta.Filename)
	if originalName == "" || originalName == "file" {
		originalName = fid + "." + ext
	}
	filename := sanitizeFilename("img_" + fid + "_" + originalName)
	path := filepath.Join(d.cfg.Output.MediaDir, filename)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	var lastErr error
	for _, candidate := range d.fidCandidateURLs(fid) {
		if _, err := d.downloadURLToPath(candidate, path, "https://api.weibo.com/chat/"); err == nil {
			return path, nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的 fid 下载地址")
	}
	return "", lastErr
}

func (d *RealtimeMediaDownloader) queryFIDMeta(fid string) (MetaQueryResult, error) {
	endpoint, _ := url.Parse("https://upload.api.weibo.com/2/mss/meta_query.json")
	query := endpoint.Query()
	query.Set("source", d.cfg.Chat.Source)
	query.Set("fid", fid)
	query.Set("replace", "false")
	query.Set("callback", "__group_chat_meta_cb")
	endpoint.RawQuery = query.Encode()
	body, _, err := d.fetch(endpoint.String(), "https://api.weibo.com/chat/")
	if err != nil {
		return MetaQueryResult{}, err
	}
	raw := strings.TrimSpace(string(body))
	if start := strings.IndexByte(raw, '('); start >= 0 {
		if end := strings.LastIndexByte(raw, ')'); end > start {
			raw = raw[start+1 : end]
		}
	}
	var meta MetaQueryResult
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return MetaQueryResult{}, fmt.Errorf("解析 fid 元数据失败: %w", err)
	}
	return meta, nil
}

func (d *RealtimeMediaDownloader) fidCandidateURLs(fid string) []string {
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	values := []url.Values{
		{"fid": {fid}, "source": {d.cfg.Chat.Source}, "imageType": {"origin"}, "ts": {ts}},
		{"fid": {fid}, "touid": {d.cfg.Chat.GroupID}, "ts": {ts}},
		{"fid": {fid}, "source": {d.cfg.Chat.Source}, "touid": {d.cfg.Chat.GroupID}, "imageType": {"origin"}, "ts": {ts}},
	}
	result := make([]string, 0, len(values))
	for _, query := range values {
		result = append(result, "https://upload.api.weibo.com/2/mss/msget?"+query.Encode())
	}
	return result
}

func (d *RealtimeMediaDownloader) downloadURL(mediaURL, filename, referer string) (string, error) {
	mediaURL = normalizeMediaURL(mediaURL)
	if mediaURL == "" {
		return "", nil
	}
	if filename == "" || filename == "file" {
		parsed, _ := url.Parse(mediaURL)
		filename = sanitizeFilename(filepath.Base(parsed.Path))
	}
	if filename == "" || filename == "file" || filename == "." {
		filename = fmt.Sprintf("media_%d.file", time.Now().UnixMilli())
	}
	path := filepath.Join(d.cfg.Output.MediaDir, filename)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	return d.downloadURLToPath(mediaURL, path, referer)
}

func (d *RealtimeMediaDownloader) downloadURLToPath(mediaURL, path, referer string) (string, error) {
	body, contentType, err := d.fetch(mediaURL, referer)
	if err != nil {
		return "", err
	}
	if len(body) == 0 {
		return "", fmt.Errorf("媒体响应为空")
	}
	lowerContentType := strings.ToLower(contentType)
	if strings.HasPrefix(lowerContentType, "text/") || strings.Contains(lowerContentType, "application/json") {
		return "", fmt.Errorf("媒体响应类型异常: %s", contentType)
	}
	if filepath.Ext(path) == ".file" {
		if extensions, _ := mime.ExtensionsByType(strings.Split(contentType, ";")[0]); len(extensions) > 0 {
			path = strings.TrimSuffix(path, ".file") + extensions[0]
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".media-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", err
	}
	return path, nil
}

func (d *RealtimeMediaDownloader) fetch(target, referer string) ([]byte, string, error) {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/140 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Referer", referer)
	if parsed, err := url.Parse(target); err == nil {
		if cookieHeader := CookieHeaderForHost(d.cookies, parsed.Hostname()); cookieHeader != "" {
			req.Header.Set("Cookie", cookieHeader)
		}
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(resp.Body)
	return body, resp.Header.Get("Content-Type"), err
}

func appendRealtimeMediaFailure(historyPath string, msg ChatMessage, failure MediaDownloadFailure) error {
	record := FailedMediaRecord{
		RecordedAt:  time.Now().In(time.Local).Format(time.RFC3339),
		MessageID:   msg.IDString(),
		MessageTime: msg.ReadableTime(time.Now()),
		Sender:      msg.SenderName(),
		MediaType:   failure.MediaType,
		MediaRef:    failure.MediaRef,
		RawMessage:  msg.RawPayload(),
	}
	if failure.Err != nil {
		record.Error = failure.Err.Error()
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	path := failedMediaRecordPath(historyPath)
	if err := ensureParentDir(path); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(data, '\n'))
	return err
}
