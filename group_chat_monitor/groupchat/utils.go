package groupchat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"group_chat_monitor/config"

	playwright "github.com/playwright-community/playwright-go"
)

func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "file"
	}

	return strings.Map(func(r rune) rune {
		switch {
		case r < 32:
			return '_'
		case strings.ContainsRune(`\/:*?"<>|`, r):
			return '_'
		default:
			return r
		}
	}, name)
}

func normalizeMediaURL(url string) string {
	url = strings.TrimSpace(url)
	switch {
	case strings.HasPrefix(url, "//"):
		return "https:" + url
	case strings.HasPrefix(url, "http:"):
		return "https:" + strings.TrimPrefix(url, "http:")
	default:
		return url
	}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func joinMediaPaths(paths []string) *string {
	paths = uniqueStrings(paths)
	if len(paths) == 0 {
		return nil
	}
	joined := strings.Join(paths, ", ")
	return &joined
}

func matchesStopCondition(cond config.StopCondition, readableTime, sender, text string) bool {
	if !cond.Enabled {
		return false
	}
	if strings.TrimSpace(cond.TargetTime) == "" &&
		strings.TrimSpace(cond.TargetSender) == "" &&
		strings.TrimSpace(cond.TargetMessage) == "" {
		return false
	}

	timeOK := cond.TargetTime == "" || strings.Contains(readableTime, cond.TargetTime)
	senderOK := cond.TargetSender == "" || strings.Contains(sender, cond.TargetSender)
	messageOK := cond.TargetMessage == "" || strings.Contains(text, cond.TargetMessage)
	return timeOK && senderOK && messageOK
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func timeoutMillisPointer(seconds int) *float64 {
	ms := float64(seconds * 1000)
	return &ms
}

func evaluateInto(page playwright.Page, expression string, arg any, target any) error {
	var (
		value any
		err   error
	)
	if arg == nil {
		value, err = page.Evaluate(expression)
	} else {
		value, err = page.Evaluate(expression, arg)
	}
	if err != nil {
		return err
	}

	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, target)
}

func messageIDValue(id string) (int64, bool) {
	if id == "" {
		return 0, false
	}
	value, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func truncateText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}
