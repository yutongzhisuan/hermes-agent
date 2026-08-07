package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	ucallbacks "github.com/cloudwego/eino/utils/callbacks"
	"github.com/google/uuid"
)

// NewSlogCallbackHandler returns an Eino callback handler that logs ChatModel and Tool
// lifecycle with run_id / llm_call_id / tool_call_id correlation, round counters,
// duration, tokens, and truncated payloads.
func NewSlogCallbackHandler(logger *slog.Logger) callbacks.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return ucallbacks.NewHandlerHelper().
		ChatModel(&ucallbacks.ModelCallbackHandler{
			OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *model.CallbackInput) context.Context {
				ctx, st := withRunTrace(ctx)
				n := st.nextLLMN()
				callID := uuid.NewString()
				modelName := inputModel(input)
				st.setLLMCall(callID, modelName)
				cs := &callState{
					startedAt: time.Now(),
					runID:     st.runID,
					llmCallID: callID,
					model:     modelName,
					llmN:      n,
				}
				ctx = context.WithValue(ctx, callStateKey{}, cs)

				attrs := []any{
					"component", "chat_model",
					"run_id", cs.runID,
					"llm_call_id", cs.llmCallID,
					"llm_n", cs.llmN,
					"model", cs.model,
					"name", runName(info),
					"messages", messageCount(input),
				}
				if input != nil && len(input.Tools) > 0 {
					attrs = append(attrs, "tools", len(input.Tools))
				}
				logger.InfoContext(ctx, "chat_model start", attrs...)
				return ctx
			},
			OnEnd: func(ctx context.Context, info *callbacks.RunInfo, output *model.CallbackOutput) context.Context {
				cs := callStateFrom(ctx)
				attrs := []any{
					"component", "chat_model",
					"run_id", callRunID(cs, ctx),
					"llm_call_id", callLLMCallID(cs),
					"llm_n", callLLMN(cs),
					"model", callModel(cs),
					"name", runName(info),
					"duration", elapsedFrom(cs).String(),
				}
				attrs = append(attrs, chatModelEndAttrs(output)...)
				logger.InfoContext(ctx, "chat_model end", attrs...)
				return ctx
			},
			OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
				cs := callStateFrom(ctx)
				logger.ErrorContext(ctx, "chat_model error",
					"component", "chat_model",
					"run_id", callRunID(cs, ctx),
					"llm_call_id", callLLMCallID(cs),
					"llm_n", callLLMN(cs),
					"model", callModel(cs),
					"name", runName(info),
					"duration", elapsedFrom(cs).String(),
					"err", err,
				)
				return ctx
			},
		}).
		Tool(&ucallbacks.ToolCallbackHandler{
			OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *tool.CallbackInput) context.Context {
				ctx, st := withRunTrace(ctx)
				n := st.nextToolN()
				toolID := uuid.NewString()
				parentLLM, parentLLMN, modelName := st.parentLLM()
				cs := &callState{
					startedAt:  time.Now(),
					runID:      st.runID,
					llmCallID:  parentLLM,
					toolCallID: toolID,
					model:      modelName,
					llmN:       parentLLMN,
					toolN:      n,
				}
				ctx = context.WithValue(ctx, callStateKey{}, cs)

				attrs := []any{
					"component", "tool",
					"run_id", cs.runID,
					"llm_call_id", cs.llmCallID,
					"tool_call_id", cs.toolCallID,
					"llm_n", cs.llmN,
					"tool_n", cs.toolN,
					"model", cs.model,
					"name", runName(info),
				}
				if input != nil && input.ArgumentsInJSON != "" {
					attrs = append(attrs, "arguments", logPayloadValue(input.ArgumentsInJSON))
				}
				logger.InfoContext(ctx, "tool start", attrs...)
				return ctx
			},
			OnEnd: func(ctx context.Context, info *callbacks.RunInfo, output *tool.CallbackOutput) context.Context {
				cs := callStateFrom(ctx)
				attrs := []any{
					"component", "tool",
					"run_id", callRunID(cs, ctx),
					"llm_call_id", callLLMCallID(cs),
					"tool_call_id", callToolCallID(cs),
					"llm_n", callLLMN(cs),
					"tool_n", callToolN(cs),
					"model", callModel(cs),
					"name", runName(info),
					"duration", elapsedFrom(cs).String(),
				}
				if output != nil && output.Response != "" {
					attrs = append(attrs, "response", logPayloadValue(output.Response))
				}
				logger.InfoContext(ctx, "tool end", attrs...)
				return ctx
			},
			OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
				cs := callStateFrom(ctx)
				logger.ErrorContext(ctx, "tool error",
					"component", "tool",
					"run_id", callRunID(cs, ctx),
					"llm_call_id", callLLMCallID(cs),
					"tool_call_id", callToolCallID(cs),
					"llm_n", callLLMN(cs),
					"tool_n", callToolN(cs),
					"model", callModel(cs),
					"name", runName(info),
					"duration", elapsedFrom(cs).String(),
					"err", err,
				)
				return ctx
			},
		}).
		Handler()
}

func inputModel(input *model.CallbackInput) string {
	if input == nil || input.Config == nil {
		return ""
	}
	return input.Config.Model
}
