package weibo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type RunState struct {
	LastFetchedAt           string `json:"last_fetched_at"`
	LastPlaywrightRefreshAt string `json:"last_playwright_refresh_at"`
}

func LoadRunState(path string) (*RunState, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return &RunState{}, nil
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &RunState{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取状态文件失败: %w", err)
	}

	var state RunState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("解析状态文件失败: %w", err)
	}
	return &state, nil
}

func SaveRunState(path string, state *RunState) error {
	path = strings.TrimSpace(path)
	if path == "" || state == nil {
		return nil
	}

	dir := filepath.Dir(path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建状态目录失败: %w", err)
		}
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化状态文件失败: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("写入状态文件失败: %w", err)
	}
	return nil
}

func (s *RunState) LastFetchedTime() (time.Time, bool) {
	return parseStateTime(s.LastFetchedAt)
}

func (s *RunState) LastPlaywrightRefreshTime() (time.Time, bool) {
	return parseStateTime(s.LastPlaywrightRefreshAt)
}

func (s *RunState) SetLastFetchedTime(value time.Time) {
	if s == nil || value.IsZero() {
		return
	}
	s.LastFetchedAt = value.In(time.Local).Format(time.RFC3339)
}

func (s *RunState) SetLastPlaywrightRefreshTime(value time.Time) {
	if s == nil || value.IsZero() {
		return
	}
	s.LastPlaywrightRefreshAt = value.In(time.Local).Format(time.RFC3339)
}

func parseStateTime(value string) (time.Time, bool) {
	parsed, err := ParseConfigTime(value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
