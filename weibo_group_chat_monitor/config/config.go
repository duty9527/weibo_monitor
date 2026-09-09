package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type WeiboModeConfig struct {
	Weibo    WeiboConfig    `yaml:"weibo"`
	Telegram TelegramConfig `yaml:"telegram"`
	Log      LogConfig      `yaml:"log"`
}

type GroupChatModeConfig struct {
	Browser       BrowserConfig          `yaml:"browser"`
	Chat          ChatConfig             `yaml:"chat"`
	Subscription  SubscriptionConfig     `yaml:"subscription"`
	Output        GroupChatOutputConfig  `yaml:"output"`
	State         GroupChatStateConfig   `yaml:"state"`
	Filters       GroupChatFilterConfig  `yaml:"filters"`
	LocalHistory  LocalHistoryPushConfig `yaml:"local_history_push"`
	StopCondition StopCondition          `yaml:"stop_condition"`
	Telegram      TelegramConfig         `yaml:"telegram"`
	Log           LogConfig              `yaml:"log"`
}

type SubscriptionConfig struct {
	CookieCacheFile                   string `yaml:"cookie_cache_file"`
	EventLogFile                      string `yaml:"event_log_file"`
	EventLogRetentionDays             int    `yaml:"event_log_retention_days"`
	NotificationQueueFile             string `yaml:"notification_queue_file"`
	ProcessedStateFile                string `yaml:"processed_state_file"`
	AuthRefreshStateFile              string `yaml:"auth_refresh_state_file"`
	RuntimeAlertStateFile             string `yaml:"runtime_alert_state_file"`
	LoginTimeoutSeconds               int    `yaml:"login_timeout_seconds"`
	RetryDelaySeconds                 int    `yaml:"retry_delay_seconds"`
	ConnectTimeoutSeconds             int    `yaml:"connect_timeout_seconds"`
	AuthCheckIntervalHours            int    `yaml:"auth_check_interval_hours"`
	ProactiveRefreshBeforeExpiryHours int    `yaml:"proactive_refresh_before_expiry_hours"`
	BackfillEnabled                   *bool  `yaml:"backfill_enabled"`
	BackfillMaxPages                  int    `yaml:"backfill_max_pages"`
	BackfillOnFirstStart              bool   `yaml:"backfill_on_first_start"`
	TLSInsecureSkipVerify             bool   `yaml:"tls_insecure_skip_verify"`
}

func (c SubscriptionConfig) IsBackfillEnabled() bool {
	return c.BackfillEnabled == nil || *c.BackfillEnabled
}

type WeiboConfig struct {
	TargetUID           string `yaml:"target_uid"`
	SinceTime           string `yaml:"since_time"`
	StateFile           string `yaml:"state_file"`
	HistoryFile         string `yaml:"history_file"`
	MediaDir            string `yaml:"media_dir"`
	UserDataDir         string `yaml:"user_data_dir"`
	CookieFile          string `yaml:"cookie_file"`
	CookieString        string `yaml:"cookie_string"`
	BrowserApp          string `yaml:"browser_app"`
	BrowserChannel      string `yaml:"browser_channel"`
	LoginURL            string `yaml:"login_url"`
	LoginTimeoutSeconds int    `yaml:"login_timeout_seconds"`
	LoginCheckInterval  int    `yaml:"login_check_interval_seconds"`
	// PlaywrightRefreshHours is retained only so older configuration files still parse.
	// Scheduled browser refresh is disabled; Playwright now runs only after validation fails.
	PlaywrightRefreshHours int `yaml:"playwright_refresh_hours"`
	MaxPages               int `yaml:"max_pages"`
	PageIntervalSeconds    int `yaml:"page_interval_seconds"`
	PollIntervalSeconds    int `yaml:"poll_interval_seconds"`
}

type BrowserConfig struct {
	UserDataDir               string `yaml:"user_data_dir"`
	BrowserChannel            string `yaml:"browser_channel"`
	ExecutablePath            string `yaml:"executable_path"`
	Headless                  bool   `yaml:"headless"`
	ViewportWidth             int    `yaml:"viewport_width"`
	ViewportHeight            int    `yaml:"viewport_height"`
	InitialLoadTimeoutSeconds int    `yaml:"initial_load_timeout_seconds"`
	KeepOpen                  bool   `yaml:"keep_open"`
}

type ChatConfig struct {
	URL                          string `yaml:"url"`
	APIURLBase                   string `yaml:"api_url_base"`
	GroupID                      string `yaml:"group_id"`
	Source                       string `yaml:"source"`
	BatchSize                    int    `yaml:"batch_size"`
	HistoryFetchTimeoutSeconds   int    `yaml:"history_fetch_timeout_seconds"`
	ImageResponseTimeoutSeconds  int    `yaml:"image_response_timeout_seconds"`
	DirectDownloadTimeoutSeconds int    `yaml:"direct_download_timeout_seconds"`
	RetryDelayMilliseconds       int    `yaml:"retry_delay_milliseconds"`
	HistoryIntervalMilliseconds  int    `yaml:"history_interval_milliseconds"`
}

type GroupChatOutputConfig struct {
	HistoryFile string `yaml:"history_file"`
	MediaDir    string `yaml:"media_dir"`
}

type GroupChatStateConfig struct {
	StateFile string `yaml:"state_file"`
}

type GroupChatFilterConfig struct {
	TargetSenders    []string `yaml:"target_senders"`
	TargetSenderUIDs []string `yaml:"target_sender_uids"`
}

type LocalHistoryPushConfig struct {
	StartDate  string `yaml:"start_date"`
	EndDate    string `yaml:"end_date"`
	MaxRecords int    `yaml:"max_records"`
}

type StopCondition struct {
	Enabled       bool   `yaml:"enabled"`
	TargetTime    string `yaml:"target_time"`
	TargetSender  string `yaml:"target_sender"`
	TargetMessage string `yaml:"target_message"`
}

type TelegramConfig struct {
	BotToken              string `yaml:"bot_token"`
	ChatID                string `yaml:"chat_id"`
	MessageThreadID       int    `yaml:"message_thread_id"`
	DirectMessagesTopicID int    `yaml:"direct_messages_topic_id"`
	Enabled               bool   `yaml:"enabled"`
	TimeoutSeconds        int    `yaml:"timeout_seconds"`
	RetryMaxAttempts      int    `yaml:"retry_max_attempts"`
	RetryInitialDelaySecs int    `yaml:"retry_initial_delay_seconds"`
	ProxyURL              string `yaml:"proxy_url"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

func LoadWeibo(path string) (*WeiboModeConfig, error) {
	absPath, data, err := readConfigFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &WeiboModeConfig{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	cfg.resolvePaths(filepath.Dir(absPath))
	return cfg, nil
}

func LoadGroupChat(path string) (*GroupChatModeConfig, error) {
	absPath, data, err := readConfigFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &GroupChatModeConfig{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	cfg.resolvePaths(filepath.Dir(absPath))
	return cfg, nil
}

func readConfigFile(path string) (string, []byte, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", nil, fmt.Errorf("解析配置文件路径失败: %w", err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	return absPath, data, nil
}

func (c *WeiboModeConfig) validate() error {
	if c.Weibo.TargetUID == "" {
		return fmt.Errorf("weibo.target_uid 不能为空")
	}
	if strings.TrimSpace(c.Weibo.SinceTime) == "" {
		return fmt.Errorf("weibo.since_time 不能为空")
	}
	if c.Weibo.HistoryFile == "" {
		c.Weibo.HistoryFile = "weibo_history.jsonl"
	}
	if c.Weibo.StateFile == "" {
		c.Weibo.StateFile = "weibo_state.json"
	}
	if c.Weibo.MediaDir == "" {
		c.Weibo.MediaDir = "media_downloads"
	}
	if c.Weibo.UserDataDir == "" {
		c.Weibo.UserDataDir = "./weibo_user_data"
	}
	if c.Weibo.BrowserApp == "" {
		c.Weibo.BrowserApp = "Google Chrome"
	}
	if c.Weibo.BrowserChannel == "" {
		c.Weibo.BrowserChannel = inferBrowserChannel(c.Weibo.BrowserApp)
	}
	if c.Weibo.LoginURL == "" {
		c.Weibo.LoginURL = fmt.Sprintf("https://weibo.com/u/%s", c.Weibo.TargetUID)
	}
	if c.Weibo.LoginTimeoutSeconds <= 0 {
		c.Weibo.LoginTimeoutSeconds = 180
	}
	if c.Weibo.LoginCheckInterval <= 0 {
		c.Weibo.LoginCheckInterval = 5
	}
	if c.Weibo.MaxPages < 0 {
		return fmt.Errorf("weibo.max_pages 不能小于 0")
	}
	if c.Weibo.PageIntervalSeconds <= 0 {
		c.Weibo.PageIntervalSeconds = 2
	}

	return validateCommon(&c.Telegram, &c.Log)
}

func (c *WeiboModeConfig) resolvePaths(baseDir string) {
	c.Weibo.HistoryFile = resolvePath(baseDir, c.Weibo.HistoryFile)
	c.Weibo.StateFile = resolvePath(baseDir, c.Weibo.StateFile)
	c.Weibo.MediaDir = resolvePath(baseDir, c.Weibo.MediaDir)
	c.Weibo.UserDataDir = resolvePath(baseDir, c.Weibo.UserDataDir)
	c.Weibo.CookieFile = resolvePath(baseDir, c.Weibo.CookieFile)
}

func (c *GroupChatModeConfig) validate() error {
	if strings.TrimSpace(c.Chat.GroupID) == "" {
		return fmt.Errorf("chat.group_id 不能为空")
	}
	if strings.TrimSpace(c.Chat.Source) == "" {
		return fmt.Errorf("chat.source 不能为空")
	}

	if strings.TrimSpace(c.Browser.UserDataDir) == "" {
		c.Browser.UserDataDir = "./weibo_user_data"
	}
	if strings.TrimSpace(c.Browser.BrowserChannel) == "" {
		c.Browser.BrowserChannel = "chrome"
	}
	if c.Browser.ViewportWidth <= 0 {
		c.Browser.ViewportWidth = 1280
	}
	if c.Browser.ViewportHeight <= 0 {
		c.Browser.ViewportHeight = 800
	}
	if c.Browser.InitialLoadTimeoutSeconds < 0 {
		return fmt.Errorf("browser.initial_load_timeout_seconds 不能小于 0")
	}

	if strings.TrimSpace(c.Chat.APIURLBase) == "" {
		c.Chat.APIURLBase = "https://api.weibo.com/webim/groupchat/query_messages.json"
	}
	if strings.TrimSpace(c.Chat.URL) == "" {
		c.Chat.URL = fmt.Sprintf("https://api.weibo.com/chat/#/chat?check_gid=%s&source_from=11", c.Chat.GroupID)
	}
	if c.Chat.BatchSize <= 0 {
		c.Chat.BatchSize = 20
	}
	if c.Chat.HistoryFetchTimeoutSeconds <= 0 {
		c.Chat.HistoryFetchTimeoutSeconds = 20
	}
	if c.Chat.ImageResponseTimeoutSeconds <= 0 {
		c.Chat.ImageResponseTimeoutSeconds = 15
	}
	if c.Chat.DirectDownloadTimeoutSeconds <= 0 {
		c.Chat.DirectDownloadTimeoutSeconds = 15
	}
	if c.Chat.RetryDelayMilliseconds <= 0 {
		c.Chat.RetryDelayMilliseconds = 5000
	}
	if c.Chat.HistoryIntervalMilliseconds <= 0 {
		c.Chat.HistoryIntervalMilliseconds = 1500
	}
	if strings.TrimSpace(c.Subscription.CookieCacheFile) == "" {
		c.Subscription.CookieCacheFile = "groupchat_cookies.json"
	}
	if strings.TrimSpace(c.Subscription.EventLogFile) == "" {
		c.Subscription.EventLogFile = "groupchat_events"
	}
	if c.Subscription.EventLogRetentionDays <= 0 {
		c.Subscription.EventLogRetentionDays = 7
	}
	if strings.TrimSpace(c.Subscription.ProcessedStateFile) == "" {
		c.Subscription.ProcessedStateFile = "groupchat_subscription_state.json"
	}
	if strings.TrimSpace(c.Subscription.NotificationQueueFile) == "" {
		c.Subscription.NotificationQueueFile = "groupchat_notification_outbox.json"
	}
	if strings.TrimSpace(c.Subscription.AuthRefreshStateFile) == "" {
		c.Subscription.AuthRefreshStateFile = "groupchat_auth_refresh_state.json"
	}
	if strings.TrimSpace(c.Subscription.RuntimeAlertStateFile) == "" {
		c.Subscription.RuntimeAlertStateFile = "groupchat_runtime_alert_state.json"
	}
	if c.Subscription.LoginTimeoutSeconds <= 0 {
		c.Subscription.LoginTimeoutSeconds = 180
	}
	if c.Subscription.RetryDelaySeconds <= 0 {
		c.Subscription.RetryDelaySeconds = 5
	}
	if c.Subscription.ConnectTimeoutSeconds <= 0 {
		c.Subscription.ConnectTimeoutSeconds = 90
	}
	if c.Subscription.AuthCheckIntervalHours <= 0 {
		c.Subscription.AuthCheckIntervalHours = 6
	}
	if c.Subscription.ProactiveRefreshBeforeExpiryHours <= 0 {
		c.Subscription.ProactiveRefreshBeforeExpiryHours = 96
	}
	if c.Subscription.BackfillMaxPages <= 0 {
		c.Subscription.BackfillMaxPages = 20
	}

	if strings.TrimSpace(c.Output.HistoryFile) == "" {
		c.Output.HistoryFile = "clean_history.jsonl"
	}
	if strings.TrimSpace(c.Output.MediaDir) == "" {
		c.Output.MediaDir = "media_downloads"
	}
	if strings.TrimSpace(c.State.StateFile) == "" {
		c.State.StateFile = "group_chat_state.json"
	}

	c.Filters.TargetSenders = trimNonEmptyStrings(c.Filters.TargetSenders)
	c.Filters.TargetSenderUIDs = trimNonEmptyStrings(c.Filters.TargetSenderUIDs)
	c.LocalHistory.StartDate = strings.TrimSpace(c.LocalHistory.StartDate)
	c.LocalHistory.EndDate = strings.TrimSpace(c.LocalHistory.EndDate)
	if c.LocalHistory.MaxRecords < 0 {
		return fmt.Errorf("local_history_push.max_records 不能小于 0")
	}
	startDate, startOK := parseDateOnly(c.LocalHistory.StartDate)
	endDate, endOK := parseDateOnly(c.LocalHistory.EndDate)
	if c.LocalHistory.StartDate != "" && !startOK {
		return fmt.Errorf("local_history_push.start_date 格式非法，应为 YYYY-MM-DD")
	}
	if c.LocalHistory.EndDate != "" && !endOK {
		return fmt.Errorf("local_history_push.end_date 格式非法，应为 YYYY-MM-DD")
	}
	if startOK && endOK && startDate.After(endDate) {
		return fmt.Errorf("local_history_push.start_date 不能晚于 end_date")
	}

	if err := validateCommon(&c.Telegram, &c.Log); err != nil {
		return err
	}
	if c.Telegram.Enabled && len(c.Filters.TargetSenders) == 0 && len(c.Filters.TargetSenderUIDs) == 0 {
		return fmt.Errorf("telegram.enabled=true 时 filters.target_senders 和 filters.target_sender_uids 不能同时为空")
	}
	return nil
}

func (c *GroupChatModeConfig) resolvePaths(baseDir string) {
	c.Browser.UserDataDir = resolvePath(baseDir, c.Browser.UserDataDir)
	c.Browser.ExecutablePath = resolvePath(baseDir, c.Browser.ExecutablePath)
	c.Output.HistoryFile = resolvePath(baseDir, c.Output.HistoryFile)
	c.Output.MediaDir = resolvePath(baseDir, c.Output.MediaDir)
	c.State.StateFile = resolvePath(baseDir, c.State.StateFile)
	c.Subscription.CookieCacheFile = resolvePath(baseDir, c.Subscription.CookieCacheFile)
	c.Subscription.EventLogFile = resolvePath(baseDir, c.Subscription.EventLogFile)
	c.Subscription.NotificationQueueFile = resolvePath(baseDir, c.Subscription.NotificationQueueFile)
	c.Subscription.ProcessedStateFile = resolvePath(baseDir, c.Subscription.ProcessedStateFile)
	c.Subscription.AuthRefreshStateFile = resolvePath(baseDir, c.Subscription.AuthRefreshStateFile)
	c.Subscription.RuntimeAlertStateFile = resolvePath(baseDir, c.Subscription.RuntimeAlertStateFile)
}

func validateCommon(telegram *TelegramConfig, log *LogConfig) error {
	if telegram.TimeoutSeconds <= 0 {
		telegram.TimeoutSeconds = 30
	}
	if telegram.RetryMaxAttempts <= 0 {
		telegram.RetryMaxAttempts = 3
	}
	if telegram.RetryInitialDelaySecs <= 0 {
		telegram.RetryInitialDelaySecs = 5
	}
	if telegram.Enabled {
		if telegram.BotToken == "" || telegram.BotToken == "YOUR_BOT_TOKEN_HERE" {
			return fmt.Errorf("telegram.bot_token 未配置")
		}
		if telegram.ChatID == "" {
			return fmt.Errorf("telegram.chat_id 未配置")
		}
		if telegram.MessageThreadID > 0 && telegram.DirectMessagesTopicID > 0 {
			return fmt.Errorf("telegram.message_thread_id 和 telegram.direct_messages_topic_id 不能同时设置")
		}
	}
	if log.Level == "" {
		log.Level = "error"
	}
	return nil
}

func resolvePath(baseDir, path string) string {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Clean(filepath.Join(baseDir, path))
}

func inferBrowserChannel(app string) string {
	app = strings.ToLower(strings.TrimSpace(app))
	switch {
	case strings.Contains(app, "edge"):
		return "msedge"
	case strings.Contains(app, "chromium"):
		return "chromium"
	case strings.Contains(app, "chrome"):
		return "chrome"
	default:
		return "chrome"
	}
}

func trimNonEmptyStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		result = append(result, value)
	}
	return result
}

func parseDateOnly(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
