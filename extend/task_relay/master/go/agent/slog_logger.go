package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxLogPayloadRunes = 512
	maxLogArrayItems   = 8
)

// NewSlogLogger builds a slog.Logger writing to w.
// level is debug|info|warn|error (case-insensitive). When json is true, use JSONHandler.
func NewSlogLogger(w io.Writer, level string, jsonOut bool) (*slog.Logger, error) {
	if w == nil {
		return nil, fmt.Errorf("slog writer is required")
	}
	lvl, err := ParseSlogLevel(level)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if jsonOut {
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

// logPayloadValue prepares tool/model payloads for slog:
// valid JSON becomes nested values (no \" escaping); newlines are collapsed for readability.
func logPayloadValue(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if json.Valid([]byte(s)) {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err == nil {
			return sanitizeLogValue(v)
		}
	}
	return collapseLogText(truncateRunes(s, maxLogPayloadRunes))
}

func sanitizeLogValue(v any) any {
	switch t := v.(type) {
	case string:
		return truncateRunes(collapseLogText(t), maxLogPayloadRunes)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = sanitizeLogValue(val)
		}
		return out
	case []any:
		n := len(t)
		trimmed := false
		if n > maxLogArrayItems {
			n = maxLogArrayItems
			trimmed = true
		}
		out := make([]any, 0, n+1)
		for i := 0; i < n; i++ {
			out = append(out, sanitizeLogValue(t[i]))
		}
		if trimmed {
			out = append(out, fmt.Sprintf("…(%d more)", len(t)-maxLogArrayItems))
		}
		return out
	default:
		return v
	}
}

func collapseLogText(s string) string {
	if s == "" || !strings.ContainsAny(s, "\r\n\t") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || unicode.IsSpace(r) {
			if !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
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
