package agent

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	ucallbacks "github.com/cloudwego/eino/utils/callbacks"
)

type timingKey struct{}

// NewSlogCallbackHandler returns an Eino callback handler that logs ChatModel and Tool
// lifecycle with call index (round), model, duration, tokens, and truncated payloads.
func NewSlogCallbackHandler(logger *slog.Logger) callbacks.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var llmN, toolN atomic.Int64
	return ucallbacks.NewHandlerHelper().
		ChatModel(&ucallbacks.ModelCallbackHandler{
			OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *model.CallbackInput) context.Context {
				n := llmN.Add(1)
				attrs := []any{
					"component", "chat_model",
					"llm_n", n,
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
					"llm_n", llmN.Load(),
					"name", runName(info),
					"duration", elapsed(ctx).String(),
				}
				attrs = append(attrs, chatModelEndAttrs(output)...)
				logger.InfoContext(ctx, "chat_model end", attrs...)
				return ctx
			},
			OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
				logger.ErrorContext(ctx, "chat_model error",
					"component", "chat_model",
					"llm_n", llmN.Load(),
					"name", runName(info),
					"duration", elapsed(ctx).String(),
					"err", err,
				)
				return ctx
			},
		}).
		Tool(&ucallbacks.ToolCallbackHandler{
			OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *tool.CallbackInput) context.Context {
				n := toolN.Add(1)
				attrs := []any{
					"component", "tool",
					"tool_n", n,
					"llm_n", llmN.Load(),
					"name", runName(info),
				}
				if input != nil && input.ArgumentsInJSON != "" {
					attrs = append(attrs, "arguments", logPayloadValue(input.ArgumentsInJSON))
				}
				logger.InfoContext(ctx, "tool start", attrs...)
				return context.WithValue(ctx, timingKey{}, time.Now())
			},
			OnEnd: func(ctx context.Context, info *callbacks.RunInfo, output *tool.CallbackOutput) context.Context {
				attrs := []any{
					"component", "tool",
					"tool_n", toolN.Load(),
					"llm_n", llmN.Load(),
					"name", runName(info),
					"duration", elapsed(ctx).String(),
				}
				if output != nil && output.Response != "" {
					attrs = append(attrs, "response", logPayloadValue(output.Response))
				}
				logger.InfoContext(ctx, "tool end", attrs...)
				return ctx
			},
			OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
				logger.ErrorContext(ctx, "tool error",
					"component", "tool",
					"tool_n", toolN.Load(),
					"llm_n", llmN.Load(),
					"name", runName(info),
					"duration", elapsed(ctx).String(),
					"err", err,
				)
				return ctx
			},
		}).
		Handler()
}

func chatModelEndAttrs(output *model.CallbackOutput) []any {
	if output == nil {
		return nil
	}
	var attrs []any
	if u := output.TokenUsage; u != nil {
		attrs = append(attrs,
			"prompt_tokens", u.PromptTokens,
			"completion_tokens", u.CompletionTokens,
			"total_tokens", u.TotalTokens,
		)
	}
	msg := output.Message
	if msg == nil {
		return attrs
	}
	if msg.ResponseMeta != nil && msg.ResponseMeta.FinishReason != "" {
		attrs = append(attrs, "finish_reason", msg.ResponseMeta.FinishReason)
	}
	if names := toolCallNames(msg.ToolCalls); len(names) > 0 {
		attrs = append(attrs, "tool_calls", names)
	}
	if msg.Content != "" {
		attrs = append(attrs, "content", logPayloadValue(msg.Content))
	}
	return attrs
}

func toolCallNames(calls []schema.ToolCall) []string {
	if len(calls) == 0 {
		return nil
	}
	names := make([]string, 0, len(calls))
	for _, c := range calls {
		name := c.Function.Name
		if name == "" {
			name = c.ID
		}
		names = append(names, name)
	}
	return names
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
