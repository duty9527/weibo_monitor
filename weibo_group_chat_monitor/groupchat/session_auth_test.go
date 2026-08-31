package groupchat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCookieHeaderForHostPreservesDomainScopeAndSkipsExpired(t *testing.T) {
	cookies := []StoredCookie{
		{Name: "SUB", Value: "valid", Domain: ".weibo.com", Expires: float64(time.Now().Add(time.Hour).Unix())},
		{Name: "API", Value: "yes", Domain: "api.weibo.com"},
		{Name: "OTHER", Value: "no", Domain: ".example.com"},
		{Name: "OLD", Value: "no", Domain: ".weibo.com", Expires: float64(time.Now().Add(-time.Hour).Unix())},
	}

	header := CookieHeaderForHost(cookies, "api.weibo.com")
	if !strings.Contains(header, "SUB=valid") || !strings.Contains(header, "API=yes") {
		t.Fatalf("expected matching cookies, got %q", header)
	}
	if strings.Contains(header, "OTHER=") || strings.Contains(header, "OLD=") {
		t.Fatalf("unexpected cookie in header: %q", header)
	}
	if got := CookieHeaderForHost(cookies, "web.im.weibo.com"); got != "SUB=valid" {
		t.Fatalf("unexpected web.im cookie header: %q", got)
	}
}

func TestSaveStoredCookiesUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth", "cookies.json")
	want := []StoredCookie{{Name: "SUB", Value: "secret", Domain: ".weibo.com", Path: "/"}}
	if err := SaveStoredCookies(path, want); err != nil {
		t.Fatalf("SaveStoredCookies failed: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat cookie cache failed: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("unexpected permissions: got %o, want 600", got)
	}
	got, err := LoadStoredCookies(path)
	if err != nil {
		t.Fatalf("LoadStoredCookies failed: %v", err)
	}
	if len(got) != 1 || got[0].Value != "secret" {
		t.Fatalf("unexpected loaded cookies: %#v", got)
	}
}
