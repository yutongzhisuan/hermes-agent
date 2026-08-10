package agent

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

type captureChatModel struct {
	mu        sync.Mutex
	responses []*schema.Message
	idx       int
	toolNames map[string]struct{}
}

func (m *captureChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.toolNames == nil {
		m.toolNames = map[string]struct{}{}
	}
	for _, t := range model.GetCommonOptions(nil, opts...).Tools {
		m.toolNames[t.Name] = struct{}{}
	}
	if m.idx >= len(m.responses) {
		return schema.AssistantMessage("", nil), nil
	}
	msg := m.responses[m.idx]
	m.idx++
	return msg, nil
}

func (m *captureChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *captureChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *captureChatModel) names() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.toolNames))
	for name := range m.toolNames {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func writeMasterConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "master.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func TestMasterRound3ToolsRegistered(t *testing.T) {
	root := t.TempDir()
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	todosPath := filepath.Join(t.TempDir(), "todos.json")

	cfgPath := writeMasterConfig(t, `
exec:
  enabled: true
  audit:
    path: `+auditPath+`
  policy:
    mode: allow_with_deny_list
file:
  enabled: true
  root: `+root+`
fetch:
  enabled: true
  limits:
    max_bytes: 1024
    timeout_seconds: 5
todos:
  enabled: true
  path: `+todosPath+`
`)

	cm := &captureChatModel{responses: []*schema.Message{schema.AssistantMessage("done", nil)}}
	master, err := New(context.Background(), Config{
		Mode:       ModeReAct,
		ChatModel:  cm,
		ConfigPath: cfgPath,
	})
	require.NoError(t, err)
	defer master.Close()

	_, err = master.Run(context.Background(), "ping")
	require.NoError(t, err)

	names := cm.names()
	for _, want := range []string{"bash", "view", "write", "edit", "multiedit", "fetch", "download", "todos"} {
		require.Contains(t, names, want)
	}
}

func TestMasterRound3ToolsDisabledByDefault(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	cfgPath := writeMasterConfig(t, `
exec:
  enabled: true
  audit:
    path: `+auditPath+`
  policy:
    mode: allow_with_deny_list
`)

	cm := &captureChatModel{responses: []*schema.Message{schema.AssistantMessage("done", nil)}}
	master, err := New(context.Background(), Config{
		Mode:       ModeReAct,
		ChatModel:  cm,
		ConfigPath: cfgPath,
	})
	require.NoError(t, err)
	defer master.Close()

	_, err = master.Run(context.Background(), "ping")
	require.NoError(t, err)

	names := cm.names()
	require.Contains(t, names, "bash")
	for _, absent := range []string{"view", "write", "edit", "multiedit", "fetch", "download", "todos"} {
		require.NotContains(t, names, absent)
	}
}

func TestMasterHooksBlockToolCall(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	todosPath := filepath.Join(t.TempDir(), "todos.json")
	script := filepath.Join(t.TempDir(), "deny.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho denied-by-test\nexit 1\n"), 0o755))

	cfgPath := writeMasterConfig(t, `
exec:
  enabled: false
  audit:
    path: `+auditPath+`
todos:
  enabled: true
  path: `+todosPath+`
hooks:
  pre_tool_use:
    - command: `+script+`
      timeout_seconds: 5
`)

	cm := &captureChatModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "c1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "todos",
				Arguments: `{"items":[{"content":"step one","status":"pending"}]}`,
			},
		}}),
		schema.AssistantMessage("final", nil),
	}}
	master, err := New(context.Background(), Config{
		Mode:       ModeReAct,
		ChatModel:  cm,
		ConfigPath: cfgPath,
	})
	require.NoError(t, err)
	defer master.Close()

	_, runErr := master.Run(context.Background(), "plan something")
	if runErr != nil {
		require.Contains(t, runErr.Error(), "blocked by hook")
	}

	auditData, err := os.ReadFile(auditPath)
	require.NoError(t, err)
	require.Contains(t, string(auditData), `"op":"hook_block"`)

	_, statErr := os.Stat(todosPath)
	require.True(t, os.IsNotExist(statErr))
}

func TestMasterExampleConfigParses(t *testing.T) {
	cfg, err := LoadMasterConfigFile(filepath.Join("..", "cmd", "master-demo", "master.example.yaml"))
	require.NoError(t, err)

	require.NotNil(t, cfg.Fetch)
	require.False(t, cfg.Fetch.Enabled)
	require.NotNil(t, cfg.Fetch.Policy)
	require.False(t, cfg.Fetch.Policy.AllowPrivateNetworks)
	require.NotNil(t, cfg.Fetch.Limits)
	require.Equal(t, int64(1048576), cfg.Fetch.Limits.MaxBytes)
	require.Equal(t, 30, cfg.Fetch.Limits.TimeoutSeconds)

	require.NotNil(t, cfg.Todos)
	require.False(t, cfg.Todos.Enabled)

	require.NotNil(t, cfg.Hooks)
	require.Empty(t, cfg.Hooks.PreToolUse)

	require.NotNil(t, cfg.Exec)
	require.NotNil(t, cfg.Exec.Approval)
	require.Equal(t, 120, cfg.Exec.Approval.TimeoutSeconds)

	require.NotNil(t, cfg.OpenAI)

	merged, _, err := MergeFileIntoConfig(Config{MasterSession: "it"}, cfg)
	require.NoError(t, err)
	require.NotNil(t, merged.Fetch)
	require.NotNil(t, merged.Todos)
	require.NotNil(t, merged.Hooks)
}
