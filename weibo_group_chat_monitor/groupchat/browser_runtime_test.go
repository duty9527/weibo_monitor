package groupchat

import (
	"testing"

	"weibo_group_chat_monitor/config"
)

func TestBrowserLaunchTargetHonorsExplicitExecutable(t *testing.T) {
	executable, channel := browserLaunchTarget(config.BrowserConfig{
		ExecutablePath: "/opt/custom/chromium",
		BrowserChannel: "chrome",
	})
	if executable != "/opt/custom/chromium" || channel != "" {
		t.Fatalf("unexpected target: executable=%q channel=%q", executable, channel)
	}
}

func TestBrowserLaunchTargetFindsInstalledBrowserOrKeepsChannel(t *testing.T) {
	executable, channel := browserLaunchTarget(config.BrowserConfig{BrowserChannel: "chrome"})
	if executable == "" && channel != "chrome" {
		t.Fatalf("unexpected fallback: executable=%q channel=%q", executable, channel)
	}
}
