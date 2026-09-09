package groupchat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"weibo_group_chat_monitor/config"
)

func TestSubscriptionBackfillInitializesThenFillsGap(t *testing.T) {
	var mu sync.Mutex
	phase := 1
	baseTime := time.Date(2026, 9, 2, 12, 0, 0, 0, time.Local)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "99" || r.URL.Query().Get("source") != "source-1" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		if cookie, err := r.Cookie("SUB"); err != nil || cookie.Value != "secret" {
			t.Errorf("missing auth cookie: %v", err)
		}
		mu.Lock()
		currentPhase := phase
		mu.Unlock()
		ids := []int{3, 2, 1}
		if currentPhase == 2 {
			ids = []int{5, 4, 3}
		} else if currentPhase == 3 {
			ids = []int{8, 7, 6}
		}
		messages := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			messages = append(messages, map[string]any{
				"id": id, "time": baseTime.Add(time.Duration(id) * time.Second).Unix(),
				"content":   fmt.Sprintf("message-%d", id),
				"from_user": map[string]any{"screen_name": "author"},
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": messages})
	}))
	defer server.Close()

	tempDir := t.TempDir()
	enabled := true
	cfg := &config.GroupChatModeConfig{
		Chat: config.ChatConfig{
			APIURLBase: server.URL, GroupID: "99", Source: "source-1", BatchSize: 20, HistoryFetchTimeoutSeconds: 2,
		},
		Subscription: config.SubscriptionConfig{
			BackfillEnabled: &enabled, BackfillMaxPages: 3,
			ProcessedStateFile: filepath.Join(tempDir, "processed.json"),
		},
		Output: config.GroupChatOutputConfig{HistoryFile: filepath.Join(tempDir, "history")},
		State:  config.GroupChatStateConfig{StateFile: filepath.Join(tempDir, "state.json")},
	}
	cookies := []StoredCookie{{Name: "SUB", Value: "secret", Domain: "127.0.0.1"}}
	processor, err := NewSubscriptionEventProcessor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first, err := RunSubscriptionBackfill(t.Context(), cfg, cookies, processor)
	if err != nil {
		t.Fatal(err)
	}
	if !first.InitializedBoundary || first.Saved != 0 || processor.Checkpoint().ID != "3" {
		t.Fatalf("unexpected initial backfill: %#v checkpoint=%#v", first, processor.Checkpoint())
	}

	mu.Lock()
	phase = 2
	mu.Unlock()
	second, err := RunSubscriptionBackfill(t.Context(), cfg, cookies, processor)
	if err != nil {
		t.Fatal(err)
	}
	if !second.ReachedBoundary || second.Saved != 2 || processor.Checkpoint().ID != "5" {
		t.Fatalf("unexpected gap backfill: %#v checkpoint=%#v", second, processor.Checkpoint())
	}
	date := baseTime.Format("2006-01-02")
	records := readOutputRecords(t, filepath.Join(tempDir, "history", date+".jsonl"))
	if len(records) != 2 || records[0].ID != "4" || records[1].ID != "5" {
		t.Fatalf("unexpected merged records: %#v", records)
	}
}

func TestSubscriptionBackfillDoesNotAdvanceWhenPageLimitHidesBoundary(t *testing.T) {
	baseTime := time.Date(2026, 9, 2, 12, 0, 0, 0, time.Local)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		messages := []map[string]any{}
		for _, id := range []int{8, 7, 6} {
			messages = append(messages, map[string]any{"id": id, "time": baseTime.Add(time.Duration(id) * time.Second).Unix(), "content": "gap"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": messages})
	}))
	defer server.Close()
	tempDir := t.TempDir()
	enabled := true
	cfg := &config.GroupChatModeConfig{
		Chat:         config.ChatConfig{APIURLBase: server.URL, GroupID: "99", Source: "source", BatchSize: 3, HistoryFetchTimeoutSeconds: 2},
		Subscription: config.SubscriptionConfig{BackfillEnabled: &enabled, BackfillMaxPages: 1, ProcessedStateFile: filepath.Join(tempDir, "processed.json")},
		Output:       config.GroupChatOutputConfig{HistoryFile: filepath.Join(tempDir, "history")},
		State:        config.GroupChatStateConfig{StateFile: filepath.Join(tempDir, "state.json")},
	}
	state := &RunState{}
	state.SetBoundary("5", baseTime.Add(5*time.Second))
	if err := SaveRunState(cfg.State.StateFile, state); err != nil {
		t.Fatal(err)
	}
	processor, err := NewSubscriptionEventProcessor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunSubscriptionBackfill(t.Context(), cfg, nil, processor)
	if err == nil || !result.LimitReached {
		t.Fatalf("expected safe page-limit error, result=%#v err=%v", result, err)
	}
	if processor.Checkpoint().ID != "5" {
		t.Fatalf("checkpoint advanced across hidden gap: %#v", processor.Checkpoint())
	}
}
