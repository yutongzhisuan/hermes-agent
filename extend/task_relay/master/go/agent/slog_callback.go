package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	ucallbacks "github.com/cloudwego/eino/utils/callbacks"
)

type timingKey struct{}

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

// NewSlogCallbackHandler returns an Eino callback handler that logs ChatModel and Tool
// lifecycle events via slog (start/end/error, duration, token usage when present).
func NewSlogCallbackHandler(logger *slog.Logger) callbacks.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return ucallbacks.NewHandlerHelper().
		ChatModel(&ucallbacks.ModelCallbackHandler{
			OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *model.CallbackInput) context.Context {
				attrs := []any{
					"component", "chat_model",
					"name", runName(info),
					"messages", messageCount(input),
				}
				if input != nil && input.Config != nil && input.Config.Model != "" {
					attrs = append(attrs, "model", input.Config.Model)
				}
				if input != nil && len(input.Tools) > 0 {
					attrs = append(attrs, "tools", len(input.Tools))
				}
				logger.InfoContext(ctx, "chat_model start", attrs...)
				return context.WithValue(ctx, timingKey{}, time.Now())
			},
			OnEnd: func(ctx context.Context, info *callbacks.RunInfo, output *model.CallbackOutput) context.Context {
				attrs := []any{
					"component", "chat_model",
					"name", runName(info),
					"duration", elapsed(ctx).String(),
				}
				if output != nil && output.TokenUsage != nil {
					u := output.TokenUsage
					attrs = append(attrs,
						"prompt_tokens", u.PromptTokens,
						"completion_tokens", u.CompletionTokens,
						"total_tokens", u.TotalTokens,
					)
				}
				if output != nil && output.Message != nil {
					if output.Message.ResponseMeta != nil && output.Message.ResponseMeta.FinishReason != "" {
						attrs = append(attrs, "finish_reason", output.Message.ResponseMeta.FinishReason)
					}
					if logger.Enabled(ctx, slog.LevelDebug) && output.Message.Content != "" {
						attrs = append(attrs, "content", truncateRunes(output.Message.Content, maxLogPayloadRunes))
					}
				}
				logger.InfoContext(ctx, "chat_model end", attrs...)
				return ctx
			},
			OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
				logger.ErrorContext(ctx, "chat_model error",
					"component", "chat_model",
					"name", runName(info),
					"duration", elapsed(ctx).String(),
					"err", err,
				)
				return ctx
			},
		}).
		Tool(&ucallbacks.ToolCallbackHandler{
			OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *tool.CallbackInput) context.Context {
				attrs := []any{
					"component", "tool",
					"name", runName(info),
				}
				if input != nil && input.ArgumentsInJSON != "" && logger.Enabled(ctx, slog.LevelDebug) {
					attrs = append(attrs, "arguments", truncateRunes(input.ArgumentsInJSON, maxLogPayloadRunes))
				}
				logger.InfoContext(ctx, "tool start", attrs...)
				return context.WithValue(ctx, timingKey{}, time.Now())
			},
			OnEnd: func(ctx context.Context, info *callbacks.RunInfo, output *tool.CallbackOutput) context.Context {
				attrs := []any{
					"component", "tool",
					"name", runName(info),
					"duration", elapsed(ctx).String(),
				}
				if output != nil && output.Response != "" && logger.Enabled(ctx, slog.LevelDebug) {
					attrs = append(attrs, "response", truncateRunes(output.Response, maxLogPayloadRunes))
				}
				logger.InfoContext(ctx, "tool end", attrs...)
				return ctx
			},
			OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
				logger.ErrorContext(ctx, "tool error",
					"component", "tool",
					"name", runName(info),
					"duration", elapsed(ctx).String(),
					"err", err,
				)
				return ctx
			},
		}).
		Handler()
}

func runName(info *callbacks.RunInfo) string {
	if info == nil {
		return "unknown"
	}
	if info.Name != "" {
		return info.Name
	}
	if info.Type != "" {
		return info.Type
	}
	if info.Component != "" {
		return string(info.Component)
	}
	return "unknown"
}

func messageCount(input *model.CallbackInput) int {
	if input == nil {
		return 0
	}
	return len(input.Messages)
}

func elapsed(ctx context.Context) time.Duration {
	if v, ok := ctx.Value(timingKey{}).(time.Time); ok {
		return time.Since(v)
	}
	return 0
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
