package main

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

func newLogger(level string) *slog.Logger {
	var slogLevel slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}

	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slogLevel, ReplaceAttr: redactLogAttr}))
}

var telegramTokenPattern = regexp.MustCompile(`bot[0-9]+:[A-Za-z0-9_-]+`)

func redactLogAttr(_ []string, attr slog.Attr) slog.Attr {
	if attr.Value.Kind() == slog.KindString || attr.Value.Kind() == slog.KindAny {
		value := fmt.Sprint(attr.Value.Any())
		redacted := telegramTokenPattern.ReplaceAllString(value, "bot[REDACTED]")
		if redacted != value {
			attr.Value = slog.StringValue(redacted)
		}
	}
	return attr
}
