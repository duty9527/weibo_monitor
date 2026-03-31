package groupchat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const outputTimeLayout = "2006-01-02 15:04:05"

type ChatAPIResponse struct {
	Messages []ChatMessage `json:"messages"`
}

type ChatMessage struct {
	ID       FlexibleString      `json:"id"`
	Time     FlexibleInt64       `json:"time"`
	FromUser *ChatUser           `json:"from_user"`
	Text     string              `json:"text"`
	Content  string              `json:"content"`
	FIDs     FlexibleStringSlice `json:"fids"`
	PageInfo *ChatPageInfo       `json:"page_info"`
	Raw      json.RawMessage     `json:"-"`
}

type ChatUser struct {
	ScreenName string `json:"screen_name"`
}

type ChatPageInfo struct {
	Type      FlexibleString `json:"type"`
	PagePic   *ChatPagePic   `json:"page_pic"`
	MediaInfo *ChatMediaInfo `json:"media_info"`
}

type ChatPagePic struct {
	URL      string              `json:"url"`
	BMiddle  *ChatPagePicVariant `json:"bmiddle"`
	Original *ChatPagePicVariant `json:"original"`
}

type ChatPagePicVariant struct {
	URL string `json:"url"`
}

type ChatMediaInfo struct {
	MP4720P   string `json:"mp4_720p_mp4"`
	MP4SDURL  string `json:"mp4_sd_url"`
	StreamURL string `json:"stream_url"`
}

type MetaQueryResult struct {
	Extension string `json:"extension"`
	Filename  string `json:"filename"`
}

type OutputRecord struct {
	ID              string   `json:"id"`
	Time            string   `json:"time"`
	Date            string   `json:"date"`
	Hour            int      `json:"hour"`
	Sender          string   `json:"sender"`
	Message         string   `json:"message"`
	MsgType         string   `json:"msg_type"`
	TextClean       string   `json:"text_clean"`
	Emojis          []string `json:"emojis,omitempty"`
	URLs            []string `json:"urls,omitempty"`
	MentionedUsers  []string `json:"mentioned_users,omitempty"`
	DownloadedMedia *string  `json:"downloaded_media"`
	HasImage        bool     `json:"has_image,omitempty"`
}

type FailedMediaRecord struct {
	RecordedAt  string          `json:"recorded_at"`
	MessageID   string          `json:"message_id"`
	MessageTime string          `json:"message_time"`
	Sender      string          `json:"sender"`
	MediaType   string          `json:"media_type"`
	MediaRef    string          `json:"media_ref"`
	Error       string          `json:"error"`
	RawMessage  json.RawMessage `json:"raw_message"`
}

type FlexibleString string

type chatMessageAlias ChatMessage

func (m *ChatMessage) UnmarshalJSON(data []byte) error {
	var alias chatMessageAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}

	*m = ChatMessage(alias)
	m.Raw = append([]byte(nil), data...)
	return nil
}

func (s *FlexibleString) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*s = ""
		return nil
	}

	if data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*s = FlexibleString(value)
		return nil
	}

	*s = FlexibleString(string(data))
	return nil
}

func (s FlexibleString) String() string {
	return strings.TrimSpace(string(s))
}

type FlexibleInt64 int64

func (v *FlexibleInt64) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*v = 0
		return nil
	}

	var raw string
	if data[0] == '"' {
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
	} else {
		raw = string(data)
	}

	raw = strings.TrimSpace(raw)
	if raw == "" {
		*v = 0
		return nil
	}

	if i, err := strconv.ParseInt(raw, 10, 64); err == nil {
		*v = FlexibleInt64(i)
		return nil
	}

	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return err
	}
	*v = FlexibleInt64(int64(f))
	return nil
}

func (v FlexibleInt64) Int64() int64 {
	return int64(v)
}

type FlexibleStringSlice []string

func (s *FlexibleStringSlice) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*s = nil
		return nil
	}

	var values []any
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}

	result := make([]string, 0, len(values))
	for _, value := range values {
		switch v := value.(type) {
		case string:
			v = strings.TrimSpace(v)
			if v != "" {
				result = append(result, v)
			}
		case float64:
			result = append(result, strconv.FormatInt(int64(v), 10))
		default:
			if value == nil {
				continue
			}
			result = append(result, strings.TrimSpace(fmt.Sprintf("%v", value)))
		}
	}

	*s = result
	return nil
}

func (m ChatMessage) IDString() string {
	return m.ID.String()
}

func (m ChatMessage) SenderName() string {
	if m.FromUser == nil || strings.TrimSpace(m.FromUser.ScreenName) == "" {
		return "Unknown"
	}
	return strings.TrimSpace(m.FromUser.ScreenName)
}

func (m ChatMessage) TextContent() string {
	if strings.TrimSpace(m.Text) != "" {
		return m.Text
	}
	return m.Content
}

func (m ChatMessage) ReadableTime(now time.Time) string {
	if m.Time.Int64() <= 0 {
		return now.In(time.Local).Format(outputTimeLayout)
	}
	return time.Unix(m.Time.Int64(), 0).In(time.Local).Format(outputTimeLayout)
}

func (m ChatMessage) TimeValue(now time.Time) time.Time {
	if m.Time.Int64() <= 0 {
		return now.In(time.Local)
	}
	return time.Unix(m.Time.Int64(), 0).In(time.Local)
}

func (m ChatMessage) HasImage() bool {
	if len(m.FIDs) > 0 {
		return true
	}
	return m.PageInfo != nil && m.PageInfo.Type.String() == "pic"
}

func (m ChatMessage) HasVideo() bool {
	return m.PageInfo != nil && m.PageInfo.Type.String() == "video"
}

func (m ChatMessage) RawPayload() json.RawMessage {
	if len(m.Raw) > 0 {
		return append(json.RawMessage(nil), m.Raw...)
	}

	payload, err := json.Marshal(chatMessageAlias(m))
	if err != nil {
		return nil
	}
	return payload
}

func (p *ChatPageInfo) PreferredPictureURL() string {
	if p == nil || p.PagePic == nil {
		return ""
	}
	switch {
	case strings.TrimSpace(p.PagePic.URL) != "":
		return p.PagePic.URL
	case p.PagePic.BMiddle != nil && strings.TrimSpace(p.PagePic.BMiddle.URL) != "":
		return p.PagePic.BMiddle.URL
	case p.PagePic.Original != nil && strings.TrimSpace(p.PagePic.Original.URL) != "":
		return p.PagePic.Original.URL
	default:
		return ""
	}
}

func (p *ChatPageInfo) PreferredVideoURL() string {
	if p == nil || p.MediaInfo == nil {
		return ""
	}
	switch {
	case strings.TrimSpace(p.MediaInfo.MP4720P) != "":
		return p.MediaInfo.MP4720P
	case strings.TrimSpace(p.MediaInfo.MP4SDURL) != "":
		return p.MediaInfo.MP4SDURL
	case strings.TrimSpace(p.MediaInfo.StreamURL) != "":
		return p.MediaInfo.StreamURL
	default:
		return ""
	}
}

func (r OutputRecord) ParsedTime() (time.Time, bool) {
	parsed, err := time.ParseInLocation(outputTimeLayout, strings.TrimSpace(r.Time), time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
