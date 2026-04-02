package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

const defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"

type Config struct {
	App      AppConfig      `json:"app"`
	Telegram TelegramConfig `json:"telegram"`
	Weibo    WeiboConfig    `json:"weibo"`
}

type AppConfig struct {
	MaxConcurrentScrapes int `json:"max_concurrent_scrapes"`
}

type TelegramConfig struct {
	BotToken           string  `json:"bot_token"`
	APIBase            string  `json:"api_base"`
	PollTimeoutSeconds int     `json:"poll_timeout_seconds"`
	AllowedChatIDs     []int64 `json:"allowed_chat_ids"`
}

type WeiboConfig struct {
	Cookie                      string `json:"cookie"`
	CookieFile                  string `json:"cookie_file"`
	CookieSource                string `json:"cookie_source"`
	UserAgent                   string `json:"user_agent"`
	Referer                     string `json:"referer"`
	RequestTimeoutSeconds       int    `json:"request_timeout_seconds"`
	MediaDownloadTimeoutSeconds int    `json:"media_download_timeout_seconds"`
	DownloadMedia               bool   `json:"download_media"`
	DownloadDir                 string `json:"download_dir"`
	SaveRecord                  bool   `json:"save_record"`
	SaveRecordDir               string `json:"save_record_dir"`
	UserDataDir                 string `json:"user_data_dir"`
	PlaywrightHeadless          bool   `json:"playwright_headless"`
	CookieWarmupURL             string `json:"cookie_warmup_url"`
	CookieWaitMillis            int    `json:"cookie_wait_millis"`
	PlaywrightTimeoutSeconds    int    `json:"playwright_timeout_seconds"`
}

func (c *Config) applyDefaults() {
	if c.App.MaxConcurrentScrapes <= 0 {
		c.App.MaxConcurrentScrapes = 2
	}
	if c.Telegram.APIBase == "" {
		c.Telegram.APIBase = "https://api.telegram.org"
	}
	if c.Telegram.PollTimeoutSeconds <= 0 {
		c.Telegram.PollTimeoutSeconds = 30
	}
	if c.Weibo.UserAgent == "" {
		c.Weibo.UserAgent = defaultUserAgent
	}
	if c.Weibo.Referer == "" {
		c.Weibo.Referer = "https://weibo.com/"
	}
	if c.Weibo.RequestTimeoutSeconds <= 0 {
		c.Weibo.RequestTimeoutSeconds = 20
	}
	if c.Weibo.MediaDownloadTimeoutSeconds <= 0 {
		c.Weibo.MediaDownloadTimeoutSeconds = 60
	}
	if c.Weibo.DownloadDir == "" {
		c.Weibo.DownloadDir = "media_downloads"
	}
	if c.Weibo.SaveRecordDir == "" {
		c.Weibo.SaveRecordDir = "weibo_records"
	}
	if c.Weibo.CookieSource == "" {
		c.Weibo.CookieSource = "playwright"
	}
	if c.Weibo.UserDataDir == "" {
		c.Weibo.UserDataDir = "weibo_user_data"
	}
	if c.Weibo.CookieWarmupURL == "" {
		c.Weibo.CookieWarmupURL = "https://weibo.com/"
	}
	if c.Weibo.CookieWaitMillis <= 0 {
		c.Weibo.CookieWaitMillis = 2000
	}
	if c.Weibo.PlaywrightTimeoutSeconds <= 0 {
		c.Weibo.PlaywrightTimeoutSeconds = 30
	}
}

func (c Config) validate() error {
	if c.Telegram.BotToken == "" {
		return fmt.Errorf("telegram.bot_token 不能为空")
	}
	switch strings.ToLower(strings.TrimSpace(c.Weibo.CookieSource)) {
	case "playwright":
		if c.Weibo.UserDataDir == "" {
			return fmt.Errorf("weibo.user_data_dir 不能为空")
		}
	case "static":
		if c.Weibo.Cookie == "" && c.Weibo.CookieFile == "" {
			return fmt.Errorf("cookie_source=static 时，weibo.cookie 或 weibo.cookie_file 必须至少配置一个")
		}
	default:
		return fmt.Errorf("weibo.cookie_source 仅支持 playwright 或 static")
	}
	return nil
}

func (c Config) staticWeiboCookie() (string, error) {
	if c.Weibo.Cookie != "" {
		return c.Weibo.Cookie, nil
	}
	if c.Weibo.CookieFile == "" {
		return "", fmt.Errorf("未配置微博 Cookie")
	}
	data, err := os.ReadFile(c.Weibo.CookieFile)
	if err != nil {
		return "", fmt.Errorf("读取 cookie 文件失败: %w", err)
	}
	return string(bytesTrimSpace(data)), nil
}

func (c Config) chatAllowed(chatID int64) bool {
	if len(c.Telegram.AllowedChatIDs) == 0 {
		return true
	}
	for _, id := range c.Telegram.AllowedChatIDs {
		if id == chatID {
			return true
		}
	}
	return false
}

type ConfigManager struct {
	path       string
	mu         sync.RWMutex
	cfg        Config
	modTime    time.Time
	hasConfig  bool
	lastErrLog time.Time
}

func NewConfigManager(path string) *ConfigManager {
	return &ConfigManager{path: path}
}

func (m *ConfigManager) Load() (Config, error) {
	info, err := os.Stat(m.path)
	if err != nil {
		return Config{}, fmt.Errorf("读取配置文件状态失败: %w", err)
	}

	m.mu.RLock()
	unchanged := m.hasConfig && info.ModTime().Equal(m.modTime)
	current := m.cfg
	m.mu.RUnlock()
	if unchanged {
		return current, nil
	}

	data, err := os.ReadFile(m.path)
	if err != nil {
		return Config{}, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("解析配置文件失败: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
	m.modTime = info.ModTime()
	m.hasConfig = true
	return cfg, nil
}

func (m *ConfigManager) Current() Config {
	cfg, err := m.Load()
	if err == nil {
		return cfg
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Since(m.lastErrLog) > 10*time.Second {
		log.Printf("配置热重载失败，继续使用上一版配置: %v", err)
		m.lastErrLog = time.Now()
	}
	return m.cfg
}

func bytesTrimSpace(data []byte) []byte {
	start := 0
	end := len(data)
	for start < end {
		switch data[start] {
		case ' ', '\n', '\r', '\t':
			start++
		default:
			goto trimEnd
		}
	}
trimEnd:
	for end > start {
		switch data[end-1] {
		case ' ', '\n', '\r', '\t':
			end--
		default:
			return data[start:end]
		}
	}
	return data[start:end]
}
