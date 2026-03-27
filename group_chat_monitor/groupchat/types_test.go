package groupchat

import (
	"encoding/json"
	"testing"

	"group_chat_monitor/config"
)

func TestFlexibleStringUnmarshal(t *testing.T) {
	var value FlexibleString
	if err := json.Unmarshal([]byte(`12345`), &value); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if got := value.String(); got != "12345" {
		t.Fatalf("unexpected value: %q", got)
	}
}

func TestFlexibleInt64Unmarshal(t *testing.T) {
	var value FlexibleInt64
	if err := json.Unmarshal([]byte(`"1700000000"`), &value); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if got := value.Int64(); got != 1700000000 {
		t.Fatalf("unexpected value: %d", got)
	}
}

func TestFlexibleStringSliceUnmarshal(t *testing.T) {
	var values FlexibleStringSlice
	if err := json.Unmarshal([]byte(`["1", 2, " 3 "]`), &values); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	want := []string{"1", "2", "3"}
	if len(values) != len(want) {
		t.Fatalf("unexpected len: %d", len(values))
	}
	for i := range want {
		if values[i] != want[i] {
			t.Fatalf("unexpected value at %d: %q", i, values[i])
		}
	}
}

func TestNormalizeMediaURL(t *testing.T) {
	if got := normalizeMediaURL("//wx1.sinaimg.cn/test.jpg"); got != "https://wx1.sinaimg.cn/test.jpg" {
		t.Fatalf("unexpected normalized url: %q", got)
	}
}

func TestMatchesStopCondition(t *testing.T) {
	cond := config.StopCondition{
		Enabled:       true,
		TargetTime:    "2026-03-10 15:59:48",
		TargetSender:  "germer_123",
		TargetMessage: "开箱即用模型[doge]",
	}

	if !matchesStopCondition(cond, "2026-03-10 15:59:48", "germer_123", "开箱即用模型[doge]") {
		t.Fatal("expected stop condition to match")
	}

	if matchesStopCondition(cond, "2026-03-10 15:59:48", "other", "开箱即用模型[doge]") {
		t.Fatal("expected stop condition to fail")
	}
}

func TestSanitizeFilename(t *testing.T) {
	if got := sanitizeFilename(`a/b:c?.jpg`); got != "a_b_c_.jpg" {
		t.Fatalf("unexpected filename: %q", got)
	}
}
