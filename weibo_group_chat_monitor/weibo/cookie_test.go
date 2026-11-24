package weibo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"weibo_group_chat_monitor/config"
)

func newTestCookieExtractor(t *testing.T, userDataDir, baseURL string) *CookieExtractor {
	t.Helper()
	extractor := NewCookieExtractor(userDataDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	extractor.verifyBaseURL = baseURL
	extractor.verifyRetryDelay = time.Millisecond
	return extractor
}

func TestReadSessionSnapshotFiltersDomainAndExpiry(t *testing.T) {
	now := time.Unix(1795000000, 0)
	rows := []sessionCookieSnapshot{
		{Name: "SUB", Value: "sub-dot", Domain: ".weibo.com", Expires: float64(now.Add(time.Hour).Unix())},
		{Name: "SUB", Value: "sub-exact", Domain: "weibo.com", Expires: float64(now.Add(time.Hour).Unix())},
		{Name: "XSRF-TOKEN", Value: "xsrf-val", Domain: "weibo.com", Expires: -1},
		{Name: "EXPIRED", Value: "old", Domain: ".weibo.com", Expires: float64(now.Add(-time.Minute).Unix())},
		{Name: "tid", Value: "passport-tid", Domain: ".passport.weibo.com", Expires: float64(now.Add(time.Hour).Unix())},
		{Name: "ALC", Value: "login-alc", Domain: ".login.sina.com.cn", Expires: float64(now.Add(time.Hour).Unix())},
		{Name: "SINA_ONLY", Value: "ignore-sina", Domain: ".sina.com.cn", Expires: float64(now.Add(time.Hour).Unix())},
		{Name: "WEIBO_CN", Value: "ignore-cn", Domain: ".weibo.cn", Expires: float64(now.Add(time.Hour).Unix())},
		{Name: "OTHER", Value: "ignore-other", Domain: ".evilweibo.com", Expires: float64(now.Add(time.Hour).Unix())},
	}

	path := filepath.Join(t.TempDir(), sessionSnapshotFilename)
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readSessionSnapshot(path, now)
	if err != nil {
		t.Fatalf("readSessionSnapshot failed: %v", err)
	}
	want := "SUB=sub-exact; XSRF-TOKEN=xsrf-val"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExtractOrLoginResultReusesSessionSnapshotWithoutPlaywright(t *testing.T) {
	var seenCookie string
	var seenXSRF string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenCookie = r.Header.Get("Cookie")
		seenXSRF = r.Header.Get("x-xsrf-token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":1}`))
	}))
	defer server.Close()

	userDataDir := t.TempDir()
	snapshotPath := filepath.Join(userDataDir, sessionSnapshotFilename)
	rows := []sessionCookieSnapshot{
		{Name: "SUB", Value: "valid-sub", Domain: ".weibo.com", Expires: float64(time.Now().Add(time.Hour).Unix())},
		{Name: "XSRF-TOKEN", Value: "tok123", Domain: "weibo.com", Expires: -1},
	}
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	extractor := newTestCookieExtractor(t, userDataDir, server.URL)
	extractor.launchSession = func(ctx context.Context, cfg config.WeiboConfig, headless bool, waitForLogin bool) (string, error) {
		t.Fatalf("unexpected Playwright launch when snapshot is valid")
		return "", nil
	}

	res, err := extractor.ExtractOrLoginResult(context.Background(), config.WeiboConfig{TargetUID: "1401527553"})
	if err != nil {
		t.Fatalf("ExtractOrLoginResult failed: %v", err)
	}
	if res.UsedPlaywright {
		t.Fatalf("expected UsedPlaywright=false")
	}
	if seenCookie != "SUB=valid-sub; XSRF-TOKEN=tok123" {
		t.Fatalf("unexpected Cookie header: %q", seenCookie)
	}
	if seenXSRF != "tok123" {
		t.Fatalf("unexpected x-xsrf-token header: %q", seenXSRF)
	}
}

func TestExtractOrLoginResultRetriesTransientVerifyErrorAndDoesNotLaunchBrowser(t *testing.T) {
	userDataDir := t.TempDir()
	snapshotPath := filepath.Join(userDataDir, sessionSnapshotFilename)
	data, err := json.Marshal([]sessionCookieSnapshot{
		{Name: "SUB", Value: "valid-sub", Domain: ".weibo.com", Expires: -1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":1}`))
	}))
	defer server.Close()

	extractor := newTestCookieExtractor(t, userDataDir, server.URL)
	extractor.launchSession = func(ctx context.Context, cfg config.WeiboConfig, headless bool, waitForLogin bool) (string, error) {
		t.Fatalf("unexpected Playwright launch on transient verify error")
		return "", nil
	}

	res, err := extractor.ExtractOrLoginResult(context.Background(), config.WeiboConfig{TargetUID: "1401527553"})
	if err != nil {
		t.Fatalf("expected retry to recover: %v", err)
	}
	if res.UsedPlaywright || attempts.Load() != 2 {
		t.Fatalf("UsedPlaywright=%v attempts=%d, want false and 2", res.UsedPlaywright, attempts.Load())
	}

	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failServer.Close()

	failExtractor := newTestCookieExtractor(t, userDataDir, failServer.URL)
	failExtractor.launchSession = func(ctx context.Context, cfg config.WeiboConfig, headless bool, waitForLogin bool) (string, error) {
		t.Fatalf("unexpected Playwright launch when all verify attempts fail with 503")
		return "", nil
	}

	_, err = failExtractor.ExtractOrLoginResult(context.Background(), config.WeiboConfig{TargetUID: "1401527553"})
	if err == nil || !isTransientCookieVerifyError(err) {
		t.Fatalf("expected transient verify error, got: %v", err)
	}
}

func TestVerifyCookiesRejectsHTMLRedirectPage(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>login</html>"))
	}))
	defer server.Close()

	extractor := newTestCookieExtractor(t, t.TempDir(), server.URL)
	err := extractor.verifyCookies(context.Background(), "SUB=expired", "1401527553")
	if err == nil {
		t.Fatalf("expected HTML response to fail verification")
	}
	if isTransientCookieVerifyError(err) {
		t.Fatalf("expected non-transient error for HTML login page, got: %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected 1 attempt without retry, got %d", attempts.Load())
	}
}

func TestExtractViaPlaywrightSkipsHeadedFallbackWithoutDisplay(t *testing.T) {
	origDisplayCheck := hasInteractiveDisplay
	t.Cleanup(func() {
		hasInteractiveDisplay = origDisplayCheck
	})

	headlessErr := fmt.Errorf("headless verify timeout")

	t.Run("no interactive display skips headed chrome", func(t *testing.T) {
		hasInteractiveDisplay = func() bool { return false }
		var calls []bool
		extractor := newTestCookieExtractor(t, t.TempDir(), "https://example.invalid")
		extractor.launchSession = func(ctx context.Context, cfg config.WeiboConfig, headless bool, waitForLogin bool) (string, error) {
			calls = append(calls, headless)
			return "", headlessErr
		}

		_, err := extractor.ExtractViaPlaywright(context.Background(), config.WeiboConfig{TargetUID: "1401527553"})
		if len(calls) != 1 || !calls[0] {
			t.Fatalf("expected single headless call, got calls=%#v", calls)
		}
		if !errors.Is(err, headlessErr) {
			t.Fatalf("expected wrapped headless error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "无图形会话") {
			t.Fatalf("expected clear non-GUI message, got: %v", err)
		}
	})

	t.Run("interactive display falls back to headed login", func(t *testing.T) {
		hasInteractiveDisplay = func() bool { return true }
		var calls []bool
		extractor := newTestCookieExtractor(t, t.TempDir(), "https://example.invalid")
		extractor.launchSession = func(ctx context.Context, cfg config.WeiboConfig, headless bool, waitForLogin bool) (string, error) {
			calls = append(calls, headless)
			if headless {
				return "", headlessErr
			}
			if !waitForLogin {
				t.Fatalf("expected waitForLogin=true on headed fallback")
			}
			return "SUB=fresh", nil
		}

		got, err := extractor.ExtractViaPlaywright(context.Background(), config.WeiboConfig{TargetUID: "1401527553"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "SUB=fresh" {
			t.Fatalf("got %q, want SUB=fresh", got)
		}
		if len(calls) != 2 || !calls[0] || calls[1] {
			t.Fatalf("expected [true, false], got %#v", calls)
		}
	})
}

func TestVerifyCookiesRejectsOKMinus100LoginJSON(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":-100,"url":"https://weibo.com/login.php?url=https%3A%2F%2Fweibo.com%2Fu%2F1401527553"}`))
	}))
	defer server.Close()

	userDataDir := t.TempDir()
	snapshotPath := filepath.Join(userDataDir, sessionSnapshotFilename)
	data, err := json.Marshal([]sessionCookieSnapshot{
		{Name: "SUB", Value: "expired-on-server", Domain: ".weibo.com", Expires: -1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	extractor := newTestCookieExtractor(t, userDataDir, server.URL)
	var launchedHeadless bool
	extractor.launchSession = func(ctx context.Context, cfg config.WeiboConfig, headless bool, waitForLogin bool) (string, error) {
		launchedHeadless = headless && !waitForLogin
		return "SUB=renewed", nil
	}

	err = extractor.verifyCookies(context.Background(), "SUB=expired-on-server", "1401527553")
	if err == nil {
		t.Fatalf("expected ok=-100 response to fail verification")
	}
	if isTransientCookieVerifyError(err) {
		t.Fatalf("expected non-transient error for ok=-100, got: %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected 1 verify attempt for ok=-100, got %d", attempts.Load())
	}

	res, err := extractor.ExtractOrLoginResult(context.Background(), config.WeiboConfig{TargetUID: "1401527553"})
	if err != nil {
		t.Fatalf("ExtractOrLoginResult failed: %v", err)
	}
	if !launchedHeadless || !res.UsedPlaywright || res.CookieString != "SUB=renewed" {
		t.Fatalf("expected headless Playwright renewal after ok=-100, got launched=%v res=%+v", launchedHeadless, res)
	}
}

func TestVerifyCookiesRejectsMissingOKAndInvalidJSON(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "missing ok", body: `{"data":{"list":[]}}`},
		{name: "ok zero", body: `{"ok":0,"msg":"err"}`},
		{name: "invalid json", body: `not-json`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			extractor := newTestCookieExtractor(t, t.TempDir(), server.URL)
			err := extractor.verifyCookies(context.Background(), "SUB=test", "1401527553")
			if err == nil {
				t.Fatalf("expected verification error for %s", tc.name)
			}
			if isTransientCookieVerifyError(err) {
				t.Fatalf("expected non-transient error for %s, got: %v", tc.name, err)
			}
		})
	}
}
