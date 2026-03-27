package weibo

import (
	"bytes"
	"encoding/json"
)

// WeiboRecord 对应保存到 JSONL 文件的单条微博记录
type WeiboRecord struct {
	ID                string   `json:"id"`
	CreatedAt         string   `json:"created_at"`
	Text              string   `json:"text"`
	SourceURL         string   `json:"source_url"`
	MediaURLs         []string `json:"media_urls"`
	LocalMediaPaths   []string `json:"local_media_paths"`
	IsRetweet         bool     `json:"is_retweet"`
	FailedMediaURLs   []string `json:"-"`
	SkippedMediaCount int      `json:"-"`
}

// WeiboItem 对应微博 API 返回的单条微博数据（仅使用字段）
type WeiboItem struct {
	ID              interface{}        `json:"id"`
	MblogID         string             `json:"mblogid"`
	CreatedAt       string             `json:"created_at"`
	TextRaw         string             `json:"text_raw"`
	IsLongText      bool               `json:"isLongText"`
	PicIDs          []string           `json:"pic_ids"`
	PicInfos        map[string]PicInfo `json:"pic_infos"`
	PageInfo        *PageInfo          `json:"page_info"`
	RetweetedStatus *WeiboItem         `json:"retweeted_status"`
	User            *WeiboUser         `json:"user"`
}

type WeiboUser struct {
	ScreenName string `json:"screen_name"`
}

type PicInfo struct {
	Mw2000   *PicDetail `json:"mw2000"`
	Original *PicDetail `json:"original"`
	Large    *PicDetail `json:"large"`
}

type PicDetail struct {
	URL string `json:"url"`
}

type PageInfo struct {
	Type      FlexibleString `json:"type"`
	MediaInfo *MediaInfo     `json:"media_info"`
}

type MediaInfo struct {
	MP4720P   string `json:"mp4_720p_mp4"`
	MP4SdURL  string `json:"mp4_sd_url"`
	StreamURL string `json:"stream_url"`
}

// APIResponse 微博列表 API 响应结构
type APIResponse struct {
	Data struct {
		List []WeiboItem `json:"list"`
	} `json:"data"`
}

// LongTextResponse 长文 API 响应结构
type LongTextResponse struct {
	Data struct {
		LongTextContent string `json:"longTextContent"`
	} `json:"data"`
}

// FlexibleString 兼容微博接口里字符串/数字/null 混用的字段。
type FlexibleString string

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
