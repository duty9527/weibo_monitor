package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type runtimeAlertSender interface {
	SendText(context.Context, string) error
}

type runtimeAlertState struct {
	Active     bool   `json:"active"`
	Sent       bool   `json:"sent"`
	OccurredAt string `json:"occurred_at,omitempty"`
	AlertedAt  string `json:"alerted_at,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

func notifyGroupChatAuthFailureOnce(ctx context.Context, sender runtimeAlertSender, statePath string, authErr error) (bool, error) {
	state, err := loadRuntimeAlertState(statePath)
	if err != nil {
		return false, err
	}
	if state.Active && state.Sent {
		return false, nil
	}
	now := time.Now()
	if !state.Active {
		state = runtimeAlertState{
			Active:     true,
			OccurredAt: now.Format(time.RFC3339),
			LastError:  compactRuntimeAlertError(authErr),
		}
		if err := saveRuntimeAlertState(statePath, state); err != nil {
			return false, err
		}
	}
	message := strings.Join([]string{
		"⚠️ #运行异常",
		"微博群聊实时监控认证恢复失败",
		"缓存 Cookie、无头浏览器恢复和扫码登录均未成功，实时消息可能已经中断。",
		"时间：" + now.In(time.Local).Format("2006-01-02 15:04:05"),
		"错误：" + compactRuntimeAlertError(authErr),
	}, "\n")
	if err := sender.SendText(ctx, message); err != nil {
		return false, fmt.Errorf("发送 Telegram 运行异常告警失败: %w", err)
	}
	state.Sent = true
	state.AlertedAt = time.Now().Format(time.RFC3339)
	if err := saveRuntimeAlertState(statePath, state); err != nil {
		return true, err
	}
	return true, nil
}

func clearGroupChatAuthFailureAlert(statePath string) error {
	state, err := loadRuntimeAlertState(statePath)
	if err != nil {
		return err
	}
	if !state.Active {
		return nil
	}
	return saveRuntimeAlertState(statePath, runtimeAlertState{})
}

func loadRuntimeAlertState(path string) (runtimeAlertState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return runtimeAlertState{}, nil
	}
	if err != nil {
		return runtimeAlertState{}, fmt.Errorf("读取运行异常告警状态失败: %w", err)
	}
	var state runtimeAlertState
	if err := json.Unmarshal(data, &state); err != nil {
		return runtimeAlertState{}, fmt.Errorf("解析运行异常告警状态失败: %w", err)
	}
	return state, nil
}

func saveRuntimeAlertState(path string, state runtimeAlertState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".runtime-alert-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func compactRuntimeAlertError(err error) string {
	if err == nil {
		return "未知认证错误"
	}
	message := strings.Join(strings.Fields(err.Error()), " ")
	const maxRunes = 600
	runes := []rune(message)
	if len(runes) > maxRunes {
		message = string(runes[:maxRunes]) + "…"
	}
	return message
}
