package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Browser       BrowserConfig `yaml:"browser"`
	Chat          ChatConfig    `yaml:"chat"`
	Output        OutputConfig  `yaml:"output"`
	StopCondition StopCondition `yaml:"stop_condition"`
	Log           LogConfig     `yaml:"log"`
}

type BrowserConfig struct {
	UserDataDir               string `yaml:"user_data_dir"`
	BrowserChannel            string `yaml:"browser_channel"`
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

type OutputConfig struct {
	HistoryFile string `yaml:"history_file"`
	MediaDir    string `yaml:"media_dir"`
}

type StopCondition struct {
	Enabled       bool   `yaml:"enabled"`
	TargetTime    string `yaml:"target_time"`
	TargetSender  string `yaml:"target_sender"`
	TargetMessage string `yaml:"target_message"`
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

	if strings.TrimSpace(c.Output.HistoryFile) == "" {
		c.Output.HistoryFile = "clean_history.jsonl"
	}
	if strings.TrimSpace(c.Output.MediaDir) == "" {
		c.Output.MediaDir = "media_downloads"
	}
	if strings.TrimSpace(c.Log.Level) == "" {
		c.Log.Level = "info"
	}

	return nil
}

func (c *Config) resolvePaths(baseDir string) {
	c.Browser.UserDataDir = resolvePath(baseDir, c.Browser.UserDataDir)
	c.Output.HistoryFile = resolvePath(baseDir, c.Output.HistoryFile)
	c.Output.MediaDir = resolvePath(baseDir, c.Output.MediaDir)
}

func resolvePath(baseDir, path string) string {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Clean(filepath.Join(baseDir, path))
}
