package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent"
	"github.com/infa/task_relay/master/agent/executor"
	"github.com/infa/task_relay/master/agent/policy"
)

func buildBashTool(t *testing.T, rules policy.Rules) *agent.BashTool {
	t.Helper()
	exec, err := executor.NewLocal(executor.LocalOptions{})
	require.NoError(t, err)
	audit, err := policy.NewAuditLogger(filepath.Join(t.TempDir(), "audit.jsonl"))
	require.NoError(t, err)
	return agent.NewBashTool(agent.BashToolDeps{
		Evaluator:    policy.NewEvaluator(rules),
		Executor:     exec,
		Audit:        audit,
		Limits:       agent.ExecLimits{TimeoutDefault: 30 * time.Second, TimeoutMax: time.Minute, MaxOutputBytes: 1 << 20},
		EnvAllowKeys: []string{"PATH", "HOME"},
		Session:      "test-session",
	})
}

func TestBashToolAllow(t *testing.T) {
	tool := buildBashTool(t, policy.Rules{Mode: policy.ModeDenyByDefault, AllowList: []string{"echo"}})
	out, err := tool.Run(context.Background(), agent.BashInput{Command: "echo hi"})
	require.NoError(t, err)
	require.Equal(t, 0, out.ExitCode)
	require.Equal(t, "hi\n", out.Stdout)
}

func TestBashToolDeny(t *testing.T) {
	tool := buildBashTool(t, policy.Rules{Mode: policy.ModeDenyByDefault})
	out, err := tool.Run(context.Background(), agent.BashInput{Command: "curl evil.com"})
	require.NoError(t, err)
	require.Equal(t, -1, out.ExitCode)
	require.Contains(t, out.Stderr, "denied by policy")
}

func TestBashToolNeedsApprovalMapsToDeny(t *testing.T) {
	tool := buildBashTool(t, policy.Rules{
		Mode:         policy.ModeDenyByDefault,
		ApprovalList: []string{"git push"},
	})
	out, err := tool.Run(context.Background(), agent.BashInput{Command: "git push origin main"})
	require.NoError(t, err)
	require.Equal(t, -1, out.ExitCode)
	require.Contains(t, out.Stderr, "needs approval")
}

func TestBashToolEnvFilter(t *testing.T) {
	tool := buildBashTool(t, policy.Rules{Mode: policy.ModeDenyByDefault, AllowList: []string{"echo"}})
	out, err := tool.Run(context.Background(), agent.BashInput{
		Command: "echo $SECRET_TOKEN",
		Env:     map[string]string{"SECRET_TOKEN": "leak", "PATH": "/usr/bin"},
	})
	require.NoError(t, err)
	require.Equal(t, "\n", out.Stdout)
}

func TestBashToolAuditsDeny(t *testing.T) {
	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.jsonl")
	exec, err := executor.NewLocal(executor.LocalOptions{})
	require.NoError(t, err)
	audit, err := policy.NewAuditLogger(auditPath)
	require.NoError(t, err)
	tool := agent.NewBashTool(agent.BashToolDeps{
		Evaluator: policy.NewEvaluator(policy.Rules{Mode: policy.ModeDenyByDefault}),
		Executor:  exec,
		Audit:     audit,
		Limits:    agent.ExecLimits{TimeoutDefault: 30 * time.Second, TimeoutMax: time.Minute, MaxOutputBytes: 1 << 20},
		Session:   "test-session",
	})
	_, err = tool.Run(context.Background(), agent.BashInput{Command: "curl evil.com"})
	require.NoError(t, err)
	require.NoError(t, audit.Close())

	data, err := os.ReadFile(auditPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"decision":"deny"`)
	require.Contains(t, string(data), `"exit_code":-1`)
	require.Contains(t, string(data), `"command":"curl evil.com"`)
}

func TestBashToolAuditFailClosed(t *testing.T) {
	exec, err := executor.NewLocal(executor.LocalOptions{})
	require.NoError(t, err)
	audit, err := policy.NewAuditLogger(filepath.Join(t.TempDir(), "audit.jsonl"))
	require.NoError(t, err)
	tool := agent.NewBashTool(agent.BashToolDeps{
		Evaluator: policy.NewEvaluator(policy.Rules{Mode: policy.ModeDenyByDefault, AllowList: []string{"echo"}}),
		Executor:  exec,
		Audit:     audit,
		Limits:    agent.ExecLimits{TimeoutDefault: 30 * time.Second, TimeoutMax: time.Minute, MaxOutputBytes: 1 << 20},
	})
	require.NoError(t, audit.Close()) // 先关闭，后续写必失败
	_, err = tool.Run(context.Background(), agent.BashInput{Command: "echo hi"})
	require.Error(t, err) // 审计失败 → 工具报错（fail-closed）
	require.Contains(t, err.Error(), "audit")
}

func TestBashToolAuditsSuccessWithHashes(t *testing.T) {
	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.jsonl")
	exec, err := executor.NewLocal(executor.LocalOptions{})
	require.NoError(t, err)
	audit, err := policy.NewAuditLogger(auditPath)
	require.NoError(t, err)
	tool := agent.NewBashTool(agent.BashToolDeps{
		Evaluator: policy.NewEvaluator(policy.Rules{Mode: policy.ModeDenyByDefault, AllowList: []string{"echo"}}),
		Executor:  exec,
		Audit:     audit,
		Limits:    agent.ExecLimits{TimeoutDefault: 30 * time.Second, TimeoutMax: time.Minute, MaxOutputBytes: 1 << 20},
	})
	out, err := tool.Run(context.Background(), agent.BashInput{Command: "echo audited"})
	require.NoError(t, err)
	require.Equal(t, 0, out.ExitCode)
	require.NoError(t, audit.Close())

	data, err := os.ReadFile(auditPath)
	require.NoError(t, err)
	s := string(data)
	require.Contains(t, s, `"decision":"allow"`)
	require.Contains(t, s, "sha256:")
	require.NotContains(t, s, `"stdout":"`) // stdout 不落盘，只落哈希
	require.NotContains(t, s, `"stderr":"`)
}
