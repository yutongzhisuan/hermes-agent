package agent

import "github.com/infa/task_relay/master/agent/hooks"

type HooksFileConfig struct {
	PreToolUse []hooks.Hook `json:"pre_tool_use" yaml:"pre_tool_use"`
}

type HooksConfig struct {
	PreToolUse []hooks.Hook
}

func hooksConfigFromFile(f *HooksFileConfig) *HooksConfig {
	if f == nil {
		return nil
	}
	return &HooksConfig{PreToolUse: f.PreToolUse}
}
