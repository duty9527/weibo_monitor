package weibo

import (
	"encoding/json"
	playwright "github.com/mxschmitt/playwright-go"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionSnapshotIsPrivateAndDomainScoped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.json")
	if err := saveSessionSnapshot(path, []playwright.Cookie{{Name: "SUB", Value: "test", Domain: ".weibo.com", Path: "/"}, {Name: "unrelated", Value: "secret", Domain: "example.com"}}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	data, _ := os.ReadFile(path)
	var cookies []map[string]any
	if err := json.Unmarshal(data, &cookies); err != nil {
		t.Fatal(err)
	}
	if len(cookies) != 1 || cookies[0]["domain"] != ".weibo.com" {
		t.Fatal("wrong exported domains")
	}
}
