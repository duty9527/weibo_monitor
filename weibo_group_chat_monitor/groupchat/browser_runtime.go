package groupchat

import (
	"os/exec"
	"strings"

	"weibo_group_chat_monitor/config"
)

func browserLaunchTarget(cfg config.BrowserConfig) (executablePath, channel string) {
	if path := strings.TrimSpace(cfg.ExecutablePath); path != "" {
		return path, ""
	}

	channel = strings.TrimSpace(cfg.BrowserChannel)
	var preferred []string
	switch strings.ToLower(channel) {
	case "chrome", "chrome-beta", "chrome-dev", "chrome-canary":
		preferred = []string{"google-chrome-stable", "google-chrome", "chrome", "chromium"}
	case "msedge", "msedge-beta", "msedge-dev":
		preferred = []string{"microsoft-edge-stable", "microsoft-edge", "chromium"}
	case "chromium":
		preferred = []string{"chromium", "chromium-browser"}
	default:
		preferred = []string{"chromium", "chromium-browser", "google-chrome-stable", "google-chrome"}
	}
	for _, name := range preferred {
		if path, err := exec.LookPath(name); err == nil {
			return path, ""
		}
	}
	return "", channel
}
