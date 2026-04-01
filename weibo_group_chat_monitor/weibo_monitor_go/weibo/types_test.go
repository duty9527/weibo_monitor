package weibo

import (
	"encoding/json"
	"testing"
)

func TestFlexibleStringUnmarshal(t *testing.T) {
	tests := []struct {
		name string
		json string
		want FlexibleString
	}{
		{name: "string", json: `"video"`, want: FlexibleString("video")},
		{name: "number", json: `11`, want: FlexibleString("11")},
		{name: "null", json: `null`, want: FlexibleString("")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got FlexibleString
			if err := json.Unmarshal([]byte(tt.json), &got); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
