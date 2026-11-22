package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
	"weibo_group_chat_monitor/config"
	"weibo_group_chat_monitor/weibo"
)

type deliveryTestSender struct {
	fail bool
	ids  []string
}

func (s *deliveryTestSender) Enabled() bool { return true }
func (s *deliveryTestSender) SendRecord(_ context.Context, r *weibo.WeiboRecord) error {
	s.ids = append(s.ids, r.ID)
	if s.fail && r.ID == "bad" {
		return errors.New("network failure")
	}
	return nil
}
func TestDeliveryFailureDoesNotBlockOrDisappearAfterRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.WeiboModeConfig{Weibo: config.WeiboConfig{HistoryFile: filepath.Join(dir, "history.jsonl"), StateFile: filepath.Join(dir, "state.json")}}
	f, err := os.Create(cfg.Weibo.HistoryFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []weibo.WeiboRecord{{ID: "bad", CreatedAt: "2026-10-07 12:00:00"}, {ID: "good", CreatedAt: "2026-10-07 12:01:00"}} {
		if err := json.NewEncoder(f).Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	state := &weibo.RunState{LastPushedAt: "2026-10-07T11:00:00+08:00"}
	sender := &deliveryTestSender{fail: true}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := drainWeiboHistory(t.Context(), cfg, state, logger, sender); err == nil {
		t.Fatal("expected partial failure")
	}
	if len(sender.ids) != 2 || !state.Deliveries["good"].Sent || state.Deliveries["bad"].Sent {
		t.Fatal(sender.ids, state.Deliveries)
	}
	loaded, err := weibo.LoadRunState(cfg.Weibo.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	d := loaded.Deliveries["bad"]
	d.NextAttemptAt = time.Time{}
	loaded.Deliveries["bad"] = d
	sender = &deliveryTestSender{}
	if err := drainWeiboHistory(t.Context(), cfg, loaded, logger, sender); err != nil {
		t.Fatal(err)
	}
	if len(sender.ids) != 1 || sender.ids[0] != "bad" {
		t.Fatal("failed record lost or success duplicated", sender.ids)
	}
}
