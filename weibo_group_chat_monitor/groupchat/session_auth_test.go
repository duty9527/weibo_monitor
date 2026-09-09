package groupchat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestALFProactiveRefreshDue(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cookies := []StoredCookie{{Name: "ALF", Domain: ".weibo.com", Expires: float64(now.Add(72 * time.Hour).Unix())}}
	fingerprint, expiresAt, due := ALFProactiveRefreshDue(cookies, now, 96*time.Hour)
	if !due || fingerprint == "" || !expiresAt.Equal(now.Add(72*time.Hour)) {
		t.Fatalf("fingerprint=%q expires=%v due=%v", fingerprint, expiresAt, due)
	}
	if _, _, due := ALFProactiveRefreshDue(cookies, now, 48*time.Hour); due {
		t.Fatal("ALF should not be due outside refresh window")
	}
}

func TestClaimALFProactiveRefreshOnlyOncePerExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-refresh.json")
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	claimed, err := ClaimALFProactiveRefresh(path, "ALF|weibo|1", now)
	if err != nil || !claimed {
		t.Fatalf("first claim=%v err=%v", claimed, err)
	}
	claimed, err = ClaimALFProactiveRefresh(path, "ALF|weibo|1", now.Add(time.Hour))
	if err != nil || claimed {
		t.Fatalf("duplicate claim=%v err=%v", claimed, err)
	}
	claimed, err = ClaimALFProactiveRefresh(path, "ALF|weibo|2", now.Add(2*time.Hour))
	if err != nil || !claimed {
		t.Fatalf("new expiry claim=%v err=%v", claimed, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode=%o", info.Mode().Perm())
	}
}

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
