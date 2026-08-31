package groupchat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"weibo_group_chat_monitor/config"
)

const maxProcessedEventKeys = 10000

type SubscriptionProcessResult struct {
	Kind         string
	MessageID    string
	RecalledIDs  []string
	UpdatedCount int
	Duplicate    bool
	Ignored      bool
	MediaPaths   []string
	MediaErrors  []string
}

type subscriptionEnvelope struct {
	SubType FlexibleInt64   `json:"sub_type"`
	PushDID FlexibleString  `json:"push_did"`
	Type    string          `json:"type"`
	Info    json.RawMessage `json:"info"`
}

type recallInfo struct {
	GID        FlexibleString      `json:"gid"`
	IDs        FlexibleStringSlice `json:"ids"`
	Time       FlexibleInt64       `json:"time"`
	OpType     FlexibleInt64       `json:"op_type"`
	RecallText string              `json:"recall_text"`
}

type processedEventState struct {
	Keys []string `json:"keys"`
}

type SubscriptionEventProcessor struct {
	cfg       *config.GroupChatModeConfig
	statePath string
	keys      []string
	seenKeys  map[string]struct{}
	seenIDs   map[string]struct{}
	media     *RealtimeMediaDownloader
	mu        sync.Mutex
}

func (p *SubscriptionEventProcessor) SetMediaCookies(cookies []StoredCookie) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.media = NewRealtimeMediaDownloader(p.cfg, cookies)
}

func NewSubscriptionEventProcessor(cfg *config.GroupChatModeConfig) (*SubscriptionEventProcessor, error) {
	if cfg == nil {
		return nil, fmt.Errorf("订阅事件处理配置不能为空")
	}
	seenIDs, err := loadSeenIDs(cfg.Output.HistoryFile)
	if err != nil {
		return nil, err
	}
	state, err := loadProcessedEventState(cfg.Subscription.ProcessedStateFile)
	if err != nil {
		return nil, err
	}
	processor := &SubscriptionEventProcessor{
		cfg:       cfg,
		statePath: cfg.Subscription.ProcessedStateFile,
		keys:      append([]string(nil), state.Keys...),
		seenKeys:  make(map[string]struct{}, len(state.Keys)),
		seenIDs:   seenIDs,
	}
	for _, key := range state.Keys {
		processor.seenKeys[key] = struct{}{}
	}
	return processor, nil
}

func (p *SubscriptionEventProcessor) Process(data json.RawMessage) (SubscriptionProcessResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var envelope subscriptionEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return SubscriptionProcessResult{}, fmt.Errorf("解析订阅事件失败: %w", err)
	}
	switch envelope.SubType.Int64() {
	case 321:
		return p.processMessage(envelope)
	case 331:
		return p.processRecall(envelope)
	default:
		return SubscriptionProcessResult{Kind: strconv.FormatInt(envelope.SubType.Int64(), 10), Ignored: true}, nil
	}
}

func (p *SubscriptionEventProcessor) processMessage(envelope subscriptionEnvelope) (SubscriptionProcessResult, error) {
	result := SubscriptionProcessResult{Kind: "message"}
	var header struct {
		GID FlexibleString `json:"gid"`
	}
	if err := json.Unmarshal(envelope.Info, &header); err != nil {
		return result, fmt.Errorf("解析 321 消息失败: %w", err)
	}
	var message ChatMessage
	if err := json.Unmarshal(envelope.Info, &message); err != nil {
		return result, fmt.Errorf("解析 321 消息内容失败: %w", err)
	}
	if header.GID.String() != strings.TrimSpace(p.cfg.Chat.GroupID) {
		result.Ignored = true
		return result, nil
	}
	messageID := message.IDString()
	result.MessageID = messageID
	if messageID == "" {
		return result, fmt.Errorf("321 消息缺少 id")
	}
	key := "message:" + header.GID.String() + ":" + messageID
	if p.hasProcessed(key) {
		result.Duplicate = true
		return result, nil
	}
	if _, exists := p.seenIDs[messageID]; exists {
		result.Duplicate = true
		return result, p.remember(key)
	}

	var mediaPaths []string
	if p.media != nil {
		var failures []MediaDownloadFailure
		mediaPaths, failures = p.media.DownloadMessage(message)
		result.MediaPaths = append([]string(nil), mediaPaths...)
		for _, failure := range failures {
			if failure.Err != nil {
				result.MediaErrors = append(result.MediaErrors, failure.Err.Error())
			}
			if err := appendRealtimeMediaFailure(p.cfg.Output.HistoryFile, message, failure); err != nil {
				result.MediaErrors = append(result.MediaErrors, err.Error())
			}
		}
	}
	now := time.Now()
	record := buildOutputRecord(message, message.ReadableTime(now), message.SenderName(), message.TextContent(), mediaPaths)
	if err := AppendRecords(p.cfg.Output.HistoryFile, []OutputRecord{record}); err != nil {
		return result, err
	}
	p.seenIDs[messageID] = struct{}{}
	if err := p.remember(key); err != nil {
		return result, err
	}
	return result, nil
}

func (p *SubscriptionEventProcessor) processRecall(envelope subscriptionEnvelope) (SubscriptionProcessResult, error) {
	result := SubscriptionProcessResult{Kind: "recall"}
	var info recallInfo
	if err := json.Unmarshal(envelope.Info, &info); err != nil {
		return result, fmt.Errorf("解析 331 撤回事件失败: %w", err)
	}
	if info.GID.String() != strings.TrimSpace(p.cfg.Chat.GroupID) {
		result.Ignored = true
		return result, nil
	}
	result.RecalledIDs = append([]string(nil), info.IDs...)
	if len(result.RecalledIDs) == 0 {
		return result, fmt.Errorf("331 撤回事件缺少 ids")
	}
	key := "recall:" + info.GID.String() + ":" + strings.Join(result.RecalledIDs, ",") + ":" + strconv.FormatInt(info.OpType.Int64(), 10)
	if p.hasProcessed(key) {
		result.Duplicate = true
		return result, nil
	}
	recalledAt := time.Now().Format(outputTimeLayout)
	if info.Time.Int64() > 0 {
		recalledAt = time.Unix(info.Time.Int64(), 0).In(time.Local).Format(outputTimeLayout)
	}
	updated, err := MarkRecordsRecalled(p.cfg.Output.HistoryFile, result.RecalledIDs, recalledAt, info.RecallText)
	if err != nil {
		return result, err
	}
	result.UpdatedCount = updated
	if err := p.remember(key); err != nil {
		return result, err
	}
	return result, nil
}

func (p *SubscriptionEventProcessor) hasProcessed(key string) bool {
	_, exists := p.seenKeys[key]
	return exists
}

func (p *SubscriptionEventProcessor) remember(key string) error {
	if p.hasProcessed(key) {
		return nil
	}
	p.seenKeys[key] = struct{}{}
	p.keys = append(p.keys, key)
	if len(p.keys) > maxProcessedEventKeys {
		removed := p.keys[0]
		p.keys = p.keys[len(p.keys)-maxProcessedEventKeys:]
		delete(p.seenKeys, removed)
	}
	return saveProcessedEventState(p.statePath, processedEventState{Keys: p.keys})
}

func loadProcessedEventState(path string) (*processedEventState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &processedEventState{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取订阅去重状态失败: %w", err)
	}
	var state processedEventState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("解析订阅去重状态失败: %w", err)
	}
	return &state, nil
}

func saveProcessedEventState(path string, state processedEventState) error {
	if err := ensureParentDir(path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".subscription-state-*")
	if err != nil {
		return fmt.Errorf("创建订阅状态临时文件失败: %w", err)
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
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("保存订阅去重状态失败: %w", err)
	}
	return nil
}

func MarkRecordsRecalled(historyPath string, ids []string, recalledAt, recallText string) (int, error) {
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = struct{}{}
		}
	}
	files, err := historyRecordFiles(historyPath)
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, path := range files {
		count, err := markFileRecordsRecalled(path, wanted, recalledAt, recallText)
		if err != nil {
			return updated, err
		}
		updated += count
	}
	return updated, nil
}

func historyRecordFiles(historyPath string) ([]string, error) {
	var files []string
	if filepath.Ext(historyPath) == ".jsonl" {
		if _, err := os.Stat(historyPath); err == nil {
			files = append(files, historyPath)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	dir := historyOutputDir(historyPath)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && dateFilePattern.MatchString(entry.Name()) {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(files)
	return uniqueStrings(files), nil
}

func markFileRecordsRecalled(path string, wanted map[string]struct{}, recalledAt, recallText string) (int, error) {
	input, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".recall-*")
	if err != nil {
		input.Close()
		return 0, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	info, statErr := input.Stat()
	if statErr == nil {
		_ = tmp.Chmod(info.Mode().Perm())
	}
	updated := 0
	scanner := bufio.NewScanner(input)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 4*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var record map[string]json.RawMessage
		if json.Unmarshal(line, &record) == nil {
			var id string
			_ = json.Unmarshal(record["id"], &id)
			if _, ok := wanted[id]; ok {
				record["recalled"] = json.RawMessage("true")
				record["recalled_at"], _ = json.Marshal(recalledAt)
				record["recall_text"], _ = json.Marshal(recallText)
				line, err = json.Marshal(record)
				if err != nil {
					input.Close()
					tmp.Close()
					return updated, err
				}
				updated++
			}
		}
		if _, err := tmp.Write(append(line, '\n')); err != nil {
			input.Close()
			tmp.Close()
			return updated, err
		}
	}
	scanErr := scanner.Err()
	closeInputErr := input.Close()
	closeTmpErr := tmp.Close()
	if scanErr != nil {
		return updated, scanErr
	}
	if closeInputErr != nil {
		return updated, closeInputErr
	}
	if closeTmpErr != nil {
		return updated, closeTmpErr
	}
	if updated == 0 {
		return 0, nil
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return updated, err
	}
	return updated, nil
}
