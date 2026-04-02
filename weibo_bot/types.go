package main

import (
	"bytes"
	"encoding/json"
)

type TelegramAPIResponse[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	Description string `json:"description"`
	ErrorCode   int    `json:"error_code"`
}

type TelegramUpdate struct {
	UpdateID      int64            `json:"update_id"`
	Message       *TelegramMessage `json:"message"`
	EditedMessage *TelegramMessage `json:"edited_message"`
}

type TelegramMessage struct {
	MessageID       int64            `json:"message_id"`
	MessageThreadID int64            `json:"message_thread_id"`
	Chat            TelegramChat     `json:"chat"`
	From            *TelegramUser    `json:"from"`
	Text            string           `json:"text"`
	Entities        []TelegramEntity `json:"entities"`
}

type TelegramChat struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

type TelegramUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	UserName  string `json:"username"`
}

type TelegramEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

type TelegramBotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type TelegramMenuButton struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	URL  string `json:"url,omitempty"`
}

type Target struct {
	ChatID   int64
	ThreadID int64
}

type Record struct {
	ID              string   `json:"id"`
	CreatedAt       string   `json:"created_at"`
	Text            string   `json:"text"`
	MediaURLs       []string `json:"media_urls"`
	LocalMediaPaths []string `json:"local_media_paths"`
	FailedMediaURLs []string `json:"failed_media_urls"`
	IsRetweet       bool     `json:"is_retweet"`
	SourceURL       string   `json:"source_url"`
}

type WeiboStatus struct {
	ID              int64                   `json:"id"`
	IDStr           string                  `json:"idstr"`
	MblogID         string                  `json:"mblogid"`
	CreatedAt       string                  `json:"created_at"`
	Text            string                  `json:"text"`
	TextRaw         string                  `json:"text_raw"`
	IsLongText      bool                    `json:"isLongText"`
	User            WeiboUser               `json:"user"`
	RetweetedStatus *WeiboStatus            `json:"retweeted_status"`
	PageInfo        *WeiboPageInfo          `json:"page_info"`
	PicInfos        map[string]WeiboPicInfo `json:"pic_infos"`
	PicIDs          []string                `json:"pic_ids"`
}

type WeiboUser struct {
	ScreenName string `json:"screen_name"`
}

type WeiboPageInfo struct {
	Type      FlexibleString  `json:"type"`
	MediaInfo *WeiboMediaInfo `json:"media_info"`
}

type WeiboMediaInfo struct {
	MP4720p string `json:"mp4_720p_mp4"`
	MP4SD   string `json:"mp4_sd_url"`
	Stream  string `json:"stream_url"`
}

type WeiboPicInfo struct {
	MW2000   *WeiboPicURL `json:"mw2000"`
	Original *WeiboPicURL `json:"original"`
	Large    *WeiboPicURL `json:"large"`
}

type WeiboPicURL struct {
	URL string `json:"url"`
}

type WeiboErrorResponse struct {
	ErrorType string `json:"error_type"`
	ErrorCode int    `json:"error_code"`
	Message   string `json:"msg"`
}

type WeiboLongTextResponse struct {
	Data struct {
		LongTextContent string `json:"longTextContent"`
	} `json:"data"`
}

type FlexibleString string

func (s *FlexibleString) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		*s = ""
		return nil
	}

	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*s = FlexibleString(text)
		return nil
	}

	*s = FlexibleString(string(data))
	return nil
}

type telegramRawResult = json.RawMessage
