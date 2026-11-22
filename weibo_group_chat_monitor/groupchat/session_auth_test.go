package groupchat

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"weibo_group_chat_monitor/config"
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
	claimed, err = ClaimALFProactiveRefresh(path, "ALF|weibo|2", now.Add(7*time.Hour))
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

func TestRefreshUsesEarliestCredentialExpiry(t *testing.T) {
	now := time.Now()
	cookies := []StoredCookie{
		{Name: "ALF", Domain: ".weibo.com", Expires: float64(now.Add(30 * 24 * time.Hour).Unix())},
		{Name: "SUBP", Domain: ".sina.com.cn", Expires: float64(now.Add(24 * time.Hour).Unix())},
	}
	key, _, due := ALFProactiveRefreshDue(cookies, now, 96*time.Hour)
	if !due || !strings.HasPrefix(key, "SUBP|") {
		t.Fatalf("missed SSO expiry: %s %v", key, due)
	}
	path := filepath.Join(t.TempDir(), "refresh.json")
	if ok, err := ClaimALFProactiveRefresh(path, key, now); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := ClaimALFProactiveRefresh(path, key, now.Add(7*time.Hour)); err != nil || !ok {
		t.Fatal("failed refresh must be retried", ok, err)
	}
}

func TestPeriodicRefreshScheduleSurvivesReconnects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refresh.json")
	now := time.Now().Truncate(time.Second)
	if _, err := ClaimALFProactiveRefresh(path, "periodic", now); err != nil {
		t.Fatal(err)
	}
	for _, elapsed := range []time.Duration{time.Minute, 3 * time.Hour, 6 * time.Hour, 7 * time.Hour} {
		got, err := PeriodicAuthRefreshDue(path, now.Add(elapsed), 6*time.Hour)
		if err != nil || got != (elapsed >= 6*time.Hour) {
			t.Fatal(elapsed, got, err)
		}
	}
}

func TestFallbackRequiresPinnedMatchingAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := SaveStoredCookies(path, []StoredCookie{{Name: "SUB", Value: "test", Domain: ".weibo.com"}}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.GroupChatModeConfig{Subscription: config.SubscriptionConfig{FallbackCookieFile: path}}
	if _, err := loadFallbackAuth(t.Context(), cfg); err == nil {
		t.Fatal("must require pinned UID")
	}
	previous := http.DefaultClient
	defer func() { http.DefaultClient = previous }()
	http.DefaultClient = &http.Client{Transport: authTestTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"profile":{"id":42}}`))}, nil
	})}
	cfg.Subscription.ExpectedUID = "different"
	if _, err := loadFallbackAuth(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), "expected_uid") {
		t.Fatal("accepted wrong account", err)
	}
}

type authTestTransport func(*http.Request) (*http.Response, error)

func (f authTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
