package groupchat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type RunState struct {
	LastMessageID   string `json:"last_message_id"`
	LastMessageTime string `json:"last_message_time"`
	LastRunAt       string `json:"last_run_at"`
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
		return nil, fmt.Errorf("读取群聊状态文件失败: %w", err)
	}

	var state RunState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("解析群聊状态文件失败: %w", err)
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
			return fmt.Errorf("创建群聊状态目录失败: %w", err)
		}
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化群聊状态文件失败: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("写入群聊状态文件失败: %w", err)
	}
	return nil
}

func (s *RunState) LastMessageParsedTime() (time.Time, bool) {
	value := strings.TrimSpace(s.LastMessageTime)
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.ParseInLocation(outputTimeLayout, value, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func (s *RunState) SetBoundary(id string, t time.Time) {
	if s == nil {
		return
	}
	s.LastMessageID = strings.TrimSpace(id)
	if !t.IsZero() {
		s.LastMessageTime = t.In(time.Local).Format(outputTimeLayout)
	}
}

func (s *RunState) SetLastRunAt(t time.Time) {
	if s == nil || t.IsZero() {
		return
	}
	s.LastRunAt = t.In(time.Local).Format(time.RFC3339)
}
