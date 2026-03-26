package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Weibo    WeiboConfig    `yaml:"weibo"`
	Telegram TelegramConfig `yaml:"telegram"`
	Log      LogConfig      `yaml:"log"`
}

type WeiboConfig struct {
	TargetUID              string `yaml:"target_uid"`
	SinceTime              string `yaml:"since_time"`
	StateFile              string `yaml:"state_file"`
	HistoryFile            string `yaml:"history_file"`
	MediaDir               string `yaml:"media_dir"`
	UserDataDir            string `yaml:"user_data_dir"`
	CookieFile             string `yaml:"cookie_file"`
	CookieString           string `yaml:"cookie_string"`
	BrowserApp             string `yaml:"browser_app"`
	BrowserChannel         string `yaml:"browser_channel"`
	LoginURL               string `yaml:"login_url"`
	LoginTimeoutSeconds    int    `yaml:"login_timeout_seconds"`
	LoginCheckInterval     int    `yaml:"login_check_interval_seconds"`
	PlaywrightRefreshHours int    `yaml:"playwright_refresh_hours"`
	MaxPages               int    `yaml:"max_pages"`
	PageIntervalSeconds    int    `yaml:"page_interval_seconds"`
	PollIntervalSeconds    int    `yaml:"poll_interval_seconds"`
}

type TelegramConfig struct {
	BotToken              string `yaml:"bot_token"`
	ChatID                string `yaml:"chat_id"`
	MessageThreadID       int    `yaml:"message_thread_id"`
	DirectMessagesTopicID int    `yaml:"direct_messages_topic_id"`
	Enabled               bool   `yaml:"enabled"`
	TimeoutSeconds        int    `yaml:"timeout_seconds"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

func Load(path string) (*Config, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析配置文件路径失败: %w", err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	cfg.resolvePaths(filepath.Dir(absPath))
	return cfg, nil
}

func (c *Config) validate() error {
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
	if c.Weibo.PlaywrightRefreshHours <= 0 {
		c.Weibo.PlaywrightRefreshHours = 6
	}
	if c.Weibo.MaxPages < 0 {
		return fmt.Errorf("weibo.max_pages 不能小于 0")
	}
	if c.Weibo.PageIntervalSeconds <= 0 {
		c.Weibo.PageIntervalSeconds = 2
	}
	if c.Telegram.TimeoutSeconds <= 0 {
		c.Telegram.TimeoutSeconds = 30
	}
	if c.Telegram.Enabled {
		if c.Telegram.BotToken == "" || c.Telegram.BotToken == "YOUR_BOT_TOKEN_HERE" {
			return fmt.Errorf("telegram.bot_token 未配置")
		}
		if c.Telegram.ChatID == "" {
			return fmt.Errorf("telegram.chat_id 未配置")
		}
		if c.Telegram.MessageThreadID > 0 && c.Telegram.DirectMessagesTopicID > 0 {
			return fmt.Errorf("telegram.message_thread_id 和 telegram.direct_messages_topic_id 不能同时设置")
		}
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	return nil
}

func (c *Config) resolvePaths(baseDir string) {
	c.Weibo.HistoryFile = resolvePath(baseDir, c.Weibo.HistoryFile)
	c.Weibo.StateFile = resolvePath(baseDir, c.Weibo.StateFile)
	c.Weibo.MediaDir = resolvePath(baseDir, c.Weibo.MediaDir)
	c.Weibo.UserDataDir = resolvePath(baseDir, c.Weibo.UserDataDir)
	c.Weibo.CookieFile = resolvePath(baseDir, c.Weibo.CookieFile)
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
