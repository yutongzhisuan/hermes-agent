package agent

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"unicode/utf8"
)

const maxLogPayloadRunes = 512

// NewSlogLogger builds a slog.Logger writing to w.
// level is debug|info|warn|error (case-insensitive). When json is true, use JSONHandler.
func NewSlogLogger(w io.Writer, level string, json bool) (*slog.Logger, error) {
	if w == nil {
		return nil, fmt.Errorf("slog writer is required")
	}
	lvl, err := ParseSlogLevel(level)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if json {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h), nil
}

// ParseSlogLevel maps CLI level names to slog.Level.
func ParseSlogLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unsupported log level %q (use debug|info|warn|error|off)", level)
	}
}

func truncateRunes(s string, max int) string {
	if max <= 0 || s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}
