package agent

import (
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

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
