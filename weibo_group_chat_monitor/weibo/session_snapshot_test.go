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
	if err := saveSessionSnapshot(path, []playwright.Cookie{
		{Name: "SUB", Value: "test", Domain: ".weibo.com", Path: "/"},
		{Name: "ALC", Value: "alc-token", Domain: ".login.sina.com.cn", Path: "/"},
		{Name: "tid", Value: "tid-token", Domain: ".passport.weibo.com", Path: "/"},
		{Name: "SUB_CN", Value: "cn-token", Domain: ".weibo.cn", Path: "/"},
		{Name: "unrelated", Value: "secret", Domain: "example.com"},
		{Name: "lookalike", Value: "secret2", Domain: "evilsina.com.cn"},
	}); err != nil {
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
	if len(cookies) != 4 {
		t.Fatalf("expected 4 exported cookies, got %d: %#v", len(cookies), cookies)
	}
	gotDomains := make([]string, 0, len(cookies))
	for _, c := range cookies {
		dom, _ := c["domain"].(string)
		gotDomains = append(gotDomains, dom)
	}
	wantDomains := []string{".weibo.com", ".login.sina.com.cn", ".passport.weibo.com", ".weibo.cn"}
	for i, want := range wantDomains {
		if gotDomains[i] != want {
			t.Fatalf("got domains %v, want %v", gotDomains, wantDomains)
		}
	}
}
