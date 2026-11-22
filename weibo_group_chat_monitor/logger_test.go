package main

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactTelegramToken(t *testing.T) {
	for _, attr := range []slog.Attr{
		slog.String("msg", "Post https://api.telegram.org/bot123:fake-token/sendPhoto failed"),
		slog.Any("err", fmt.Errorf("wrapped: %w", fmt.Errorf("https://api.telegram.org/bot123:fake-token/sendPhoto: DNS failure"))),
	} {
		got := redactLogAttr(nil, attr).Value.String()
		if strings.Contains(got, "fake-token") || !strings.Contains(got, "bot[REDACTED]/sendPhoto") {
			t.Fatalf("unexpected redaction: %s", got)
		}
	}
	attr := slog.Int("attempt", 3)
	if !redactLogAttr(nil, attr).Equal(attr) {
		t.Fatal("numeric attribute changed")
	}
}
