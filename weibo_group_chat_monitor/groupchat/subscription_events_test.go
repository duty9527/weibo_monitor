package groupchat

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"weibo_group_chat_monitor/config"
)

const testMessage321 = `{"sub_type":321,"push_did":"1788196786974","type":"groupchat","info":{"gid":4761715839862414,"content":"[并不简单]","from_uid":6355013357,"from_user":{"screen_name":"非正经程序员"},"id":5338146583347736,"time":1788196786}}`
const testRecall331 = `{"sub_type":331,"push_did":"1788196792714","type":"groupchat","info":{"gid":4761715839862414,"ids":[5338146583347736],"time":1788196792,"op_type":1,"recall_text":"你撤回了一条消息"}}`

func TestSubscriptionProcessorDeduplicatesAndMarksRecall(t *testing.T) {
	tempDir := t.TempDir()
	cfg := subscriptionTestConfig(tempDir)
	processor, err := NewSubscriptionEventProcessor(cfg)
	if err != nil {
		t.Fatal(err)
	}

	first, err := processor.Process(json.RawMessage(testMessage321))
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != "message" || first.MessageID != "5338146583347736" || first.Duplicate {
		t.Fatalf("unexpected first result: %#v", first)
	}
	duplicate, err := processor.Process(json.RawMessage(testMessage321))
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate {
		t.Fatalf("second message should be duplicate: %#v", duplicate)
	}

	recall, err := processor.Process(json.RawMessage(testRecall331))
	if err != nil {
		t.Fatal(err)
	}
	if recall.UpdatedCount != 1 {
		t.Fatalf("updated recall records = %d, want 1", recall.UpdatedCount)
	}
	records := readOutputRecords(t, filepath.Join(tempDir, "2026-09-01.jsonl"))
	if len(records) != 1 || !records[0].Recalled || records[0].RecallText != "你撤回了一条消息" {
		t.Fatalf("unexpected recalled records: %#v", records)
	}

	restarted, err := NewSubscriptionEventProcessor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	afterRestart, err := restarted.Process(json.RawMessage(testMessage321))
	if err != nil {
		t.Fatal(err)
	}
	if !afterRestart.Duplicate {
		t.Fatalf("message should remain duplicate after restart: %#v", afterRestart)
	}
	recallAfterRestart, err := restarted.Process(json.RawMessage(testRecall331))
	if err != nil {
		t.Fatal(err)
	}
	if !recallAfterRestart.Duplicate {
		t.Fatalf("recall should remain duplicate after restart: %#v", recallAfterRestart)
	}
}

func TestSubscriptionProcessorIgnoresAnotherGroup(t *testing.T) {
	cfg := subscriptionTestConfig(t.TempDir())
	processor, err := NewSubscriptionEventProcessor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	data := json.RawMessage(`{"sub_type":321,"info":{"gid":1,"id":2,"content":"other"}}`)
	result, err := processor.Process(data)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ignored {
		t.Fatalf("other group event should be ignored: %#v", result)
	}
}

func subscriptionTestConfig(dir string) *config.GroupChatModeConfig {
	return &config.GroupChatModeConfig{
		Chat:         config.ChatConfig{GroupID: "4761715839862414"},
		Output:       config.GroupChatOutputConfig{HistoryFile: dir},
		Subscription: config.SubscriptionConfig{ProcessedStateFile: filepath.Join(dir, "subscription-state.json")},
	}
}

func readOutputRecords(t *testing.T, path string) []OutputRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var records []OutputRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record OutputRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}
