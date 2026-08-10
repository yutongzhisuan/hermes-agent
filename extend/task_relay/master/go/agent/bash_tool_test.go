package agent_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	sum := sha256.Sum256([]byte("audited\n"))
	require.Contains(t, s, "sha256:"+hex.EncodeToString(sum[:]))
	require.NotContains(t, s, `"stdout":"`) // stdout 不落盘，只落哈希
	require.NotContains(t, s, `"stderr":"`)
}

func TestBashToolDangerousEnvStripped(t *testing.T) {
	tool := buildBashTool(t, policy.Rules{Mode: policy.ModeDenyByDefault, AllowList: []string{"echo"}})
	out, err := tool.Run(context.Background(), agent.BashInput{
		Command: "echo $PATH",
		Env:     map[string]string{"PATH": "/tmp/evil"},
	})
	require.NoError(t, err)
	require.NotContains(t, out.Stdout, "/tmp/evil") // PATH override 被剥离，保留最小 base env 的 PATH
}

type fakeExecutor struct {
	name     string
	runCalls int
	lastSpec executor.Spec
	result   executor.JobResult
	err      error
}

func (f *fakeExecutor) Run(_ context.Context, spec executor.Spec) (executor.JobResult, error) {
	f.runCalls++
	f.lastSpec = spec
	if f.result.Backend == "" {
		f.result.Backend = f.name
	}
	return f.result, f.err
}

func (f *fakeExecutor) Name() string    { return f.name }
func (f *fakeExecutor) Sandboxed() bool { return false }

func mustAudit(t *testing.T) *policy.AuditLogger {
	t.Helper()
	audit, err := policy.NewAuditLogger(filepath.Join(t.TempDir(), "audit.jsonl"))
	require.NoError(t, err)
	return audit
}

func buildBackendTool(t *testing.T, local, remote executor.Executor, defaultBackend string) *agent.BashTool {
	t.Helper()
	return agent.NewBashTool(agent.BashToolDeps{
		Evaluator:      policy.NewEvaluator(policy.Rules{Mode: policy.ModeDenyByDefault, AllowList: []string{"echo"}}),
		Executor:       local,
		Remote:         remote,
		DefaultBackend: defaultBackend,
		Audit:          mustAudit(t),
		Limits:         agent.ExecLimits{TimeoutDefault: 30 * time.Second, TimeoutMax: time.Minute, MaxOutputBytes: 1 << 20},
		Session:        "test-session",
	})
}

func TestBashRemoteSelected(t *testing.T) {
	remote := &fakeExecutor{name: "remote", result: executor.JobResult{ExitCode: 0, Stdout: "remote\n", Backend: "remote"}}
	local := &fakeExecutor{name: "local"}
	tool := buildBackendTool(t, local, remote, "")
	out, err := tool.Run(context.Background(), agent.BashInput{Command: "echo hi", Backend: "remote"})
	require.NoError(t, err)
	require.Equal(t, 0, out.ExitCode)
	require.Equal(t, "remote\n", out.Stdout)
	require.Equal(t, 1, remote.runCalls)
	require.Equal(t, 0, local.runCalls)
	require.Equal(t, "echo hi", remote.lastSpec.Command)
}

func TestBashRemoteUnavailable(t *testing.T) {
	tool := buildBashTool(t, policy.Rules{Mode: policy.ModeDenyByDefault, AllowList: []string{"echo"}})
	_, err := tool.Run(context.Background(), agent.BashInput{Command: "echo hi", Backend: "remote"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "remote backend unavailable")
}

func buildEnvBackendTool(t *testing.T, local, remote executor.Executor) *agent.BashTool {
	t.Helper()
	return agent.NewBashTool(agent.BashToolDeps{
		Evaluator:    policy.NewEvaluator(policy.Rules{Mode: policy.ModeDenyByDefault, AllowList: []string{"echo"}}),
		Executor:     local,
		Remote:       remote,
		Audit:        mustAudit(t),
		Limits:       agent.ExecLimits{TimeoutDefault: 30 * time.Second, TimeoutMax: time.Minute, MaxOutputBytes: 1 << 20},
		EnvAllowKeys: []string{"FOO"},
		Session:      "test-session",
	})
}

func TestBashRemoteRejectsEnv(t *testing.T) {
	remote := &fakeExecutor{name: "remote"}
	local := &fakeExecutor{name: "local"}
	tool := buildEnvBackendTool(t, local, remote)
	_, err := tool.Run(context.Background(), agent.BashInput{
		Command: "echo hi",
		Backend: "remote",
		Env:     map[string]string{"FOO": "bar"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "env")
	require.Contains(t, err.Error(), "remote")
	require.Equal(t, 0, remote.runCalls)
	require.Equal(t, 0, local.runCalls)
}

func TestBashLocalAcceptsEnv(t *testing.T) {
	remote := &fakeExecutor{name: "remote"}
	localExec, err := executor.NewLocal(executor.LocalOptions{})
	require.NoError(t, err)
	tool := buildEnvBackendTool(t, localExec, remote)
	out, err := tool.Run(context.Background(), agent.BashInput{
		Command: "echo $FOO",
		Backend: "local",
		Env:     map[string]string{"FOO": "bar"},
	})
	require.NoError(t, err)
	require.Equal(t, "bar\n", out.Stdout)
	require.Equal(t, 0, remote.runCalls)
}

func TestBashUnknownBackend(t *testing.T) {
	tool := buildBashTool(t, policy.Rules{Mode: policy.ModeDenyByDefault, AllowList: []string{"echo"}})
	_, err := tool.Run(context.Background(), agent.BashInput{Command: "echo hi", Backend: "mars"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "mars")
}

func TestBashDefaultLocal(t *testing.T) {
	remote := &fakeExecutor{name: "remote", result: executor.JobResult{ExitCode: 0, Backend: "remote"}}
	local := &fakeExecutor{name: "local", result: executor.JobResult{ExitCode: 0, Stdout: "local\n"}}
	tool := buildBackendTool(t, local, remote, "")
	out, err := tool.Run(context.Background(), agent.BashInput{Command: "echo hi"})
	require.NoError(t, err)
	require.Equal(t, "local\n", out.Stdout)
	require.Equal(t, 1, local.runCalls)
	require.Equal(t, 0, remote.runCalls)
}

func TestBashDefaultRemoteWhenConfigured(t *testing.T) {
	remote := &fakeExecutor{name: "remote", result: executor.JobResult{ExitCode: 0, Stdout: "remote\n", Backend: "remote"}}
	local := &fakeExecutor{name: "local", result: executor.JobResult{ExitCode: 0}}
	tool := buildBackendTool(t, local, remote, "remote")
	out, err := tool.Run(context.Background(), agent.BashInput{Command: "echo hi"})
	require.NoError(t, err)
	require.Equal(t, "remote\n", out.Stdout)
	require.Equal(t, 1, remote.runCalls)
	require.Equal(t, 0, local.runCalls)
}

func buildWorkdirTool(t *testing.T, exec executor.Executor, paths policy.PathEvaluator, auditPath string) *agent.BashTool {
	t.Helper()
	audit, err := policy.NewAuditLogger(auditPath)
	require.NoError(t, err)
	return agent.NewBashTool(agent.BashToolDeps{
		Evaluator:    policy.NewEvaluator(policy.Rules{Mode: policy.ModeDenyByDefault, AllowList: []string{"echo"}}),
		Executor:     exec,
		Paths:        paths,
		Audit:        audit,
		Limits:       agent.ExecLimits{TimeoutDefault: 30 * time.Second, TimeoutMax: time.Minute, MaxOutputBytes: 1 << 20},
		EnvAllowKeys: []string{"FOO"},
		Session:      "test-session",
	})
}

func TestBashWorkdirDenied(t *testing.T) {
	root := t.TempDir()
	paths, err := policy.NewPathEvaluator(root, policy.PathRules{DenyList: []string{".env", "**/.env"}})
	require.NoError(t, err)
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	exec := &fakeExecutor{name: "local"}
	tool := buildWorkdirTool(t, exec, paths, auditPath)

	out, err := tool.Run(context.Background(), agent.BashInput{Command: "echo hi", WorkDir: "/etc"})
	require.NoError(t, err)
	require.Equal(t, -1, out.ExitCode)
	require.Contains(t, out.Stderr, "workdir denied")
	require.Equal(t, 0, exec.runCalls)

	s := readAudit(t, auditPath)
	require.Contains(t, s, `"decision":"deny"`)
	require.Contains(t, s, `"backend":"none"`)
	require.Contains(t, s, `"workdir":"/etc"`)
}

func TestBashWorkdirAllowed(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	paths, err := policy.NewPathEvaluator(root, policy.PathRules{DenyList: []string{".env", "**/.env"}})
	require.NoError(t, err)
	exec := &fakeExecutor{name: "local", result: executor.JobResult{ExitCode: 0, Stdout: "ok\n"}}
	tool := buildWorkdirTool(t, exec, paths, filepath.Join(t.TempDir(), "audit.jsonl"))

	out, err := tool.Run(context.Background(), agent.BashInput{Command: "echo hi", WorkDir: sub})
	require.NoError(t, err)
	require.Equal(t, "ok\n", out.Stdout)
	require.Equal(t, 1, exec.runCalls)
	require.Equal(t, sub, exec.lastSpec.WorkDir)
}

func TestBashWorkdirNilPaths(t *testing.T) {
	exec := &fakeExecutor{name: "local", result: executor.JobResult{ExitCode: 0}}
	tool := buildWorkdirTool(t, exec, nil, filepath.Join(t.TempDir(), "audit.jsonl"))

	_, err := tool.Run(context.Background(), agent.BashInput{Command: "echo hi", WorkDir: "/etc"})
	require.NoError(t, err)
	require.Equal(t, 1, exec.runCalls)
}

func TestBashWorkdirEmptyUngated(t *testing.T) {
	root := t.TempDir()
	paths, err := policy.NewPathEvaluator(root, policy.PathRules{})
	require.NoError(t, err)
	exec := &fakeExecutor{name: "local", result: executor.JobResult{ExitCode: 0}}
	tool := buildWorkdirTool(t, exec, paths, filepath.Join(t.TempDir(), "audit.jsonl"))

	_, err = tool.Run(context.Background(), agent.BashInput{Command: "echo hi"})
	require.NoError(t, err)
	require.Equal(t, 1, exec.runCalls)
	require.Empty(t, exec.lastSpec.WorkDir)
}

func TestBashRemoteUnavailableAudited(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	tool := buildWorkdirTool(t, &fakeExecutor{name: "local"}, nil, auditPath)

	_, err := tool.Run(context.Background(), agent.BashInput{Command: "echo hi", Backend: "remote"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unavailable")

	s := readAudit(t, auditPath)
	require.Contains(t, s, `"decision":"deny"`)
	require.Contains(t, s, `"backend":"none"`)
	require.Contains(t, s, "unavailable")
}

func TestBashUnknownBackendAudited(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	tool := buildWorkdirTool(t, &fakeExecutor{name: "local"}, nil, auditPath)

	_, err := tool.Run(context.Background(), agent.BashInput{Command: "echo hi", Backend: "mars"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "mars")

	s := readAudit(t, auditPath)
	require.Contains(t, s, `"decision":"deny"`)
	require.Contains(t, s, `"backend":"none"`)
	require.Contains(t, s, "mars")
}

func TestBashRemoteEnvRejectedAudited(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	remote := &fakeExecutor{name: "remote"}
	audit, err := policy.NewAuditLogger(auditPath)
	require.NoError(t, err)
	tool := agent.NewBashTool(agent.BashToolDeps{
		Evaluator:    policy.NewEvaluator(policy.Rules{Mode: policy.ModeDenyByDefault, AllowList: []string{"echo"}}),
		Executor:     &fakeExecutor{name: "local"},
		Remote:       remote,
		Audit:        audit,
		Limits:       agent.ExecLimits{TimeoutDefault: 30 * time.Second, TimeoutMax: time.Minute, MaxOutputBytes: 1 << 20},
		EnvAllowKeys: []string{"FOO"},
		Session:      "test-session",
	})

	_, err = tool.Run(context.Background(), agent.BashInput{
		Command: "echo hi",
		Backend: "remote",
		Env:     map[string]string{"FOO": "bar"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "env")
	require.Equal(t, 0, remote.runCalls)

	s := readAudit(t, auditPath)
	require.Contains(t, s, `"decision":"deny"`)
	require.Contains(t, s, `"backend":"none"`)
	require.Contains(t, s, "env")
}

type fakeApproval struct {
	approved bool
	err      error
	calls    int
	lastReq  policy.ApprovalRequest
}

func (f *fakeApproval) RequestApproval(_ context.Context, req policy.ApprovalRequest) (bool, error) {
	f.calls++
	f.lastReq = req
	return f.approved, f.err
}

func buildApprovalTool(t *testing.T, exec executor.Executor, approval policy.ApprovalService, auditPath string) *agent.BashTool {
	t.Helper()
	audit, err := policy.NewAuditLogger(auditPath)
	require.NoError(t, err)
	return agent.NewBashTool(agent.BashToolDeps{
		Evaluator: policy.NewEvaluator(policy.Rules{
			Mode:         policy.ModeDenyByDefault,
			ApprovalList: []string{"git push"},
		}),
		Executor: exec,
		Approval: approval,
		Audit:    audit,
		Limits:   agent.ExecLimits{TimeoutDefault: 30 * time.Second, TimeoutMax: time.Minute, MaxOutputBytes: 1 << 20},
		Session:  "test-session",
	})
}

func readAudit(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func TestBashApprovalApproved(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	exec := &fakeExecutor{name: "local", result: executor.JobResult{ExitCode: 0, Stdout: "pushed\n"}}
	approval := &fakeApproval{approved: true}
	tool := buildApprovalTool(t, exec, approval, auditPath)

	out, err := tool.Run(context.Background(), agent.BashInput{Command: "git push origin main"})
	require.NoError(t, err)
	require.Equal(t, 0, out.ExitCode)
	require.Equal(t, "pushed\n", out.Stdout)
	require.Equal(t, 1, exec.runCalls)
	require.Equal(t, 1, approval.calls)
	require.Equal(t, "git push origin main", approval.lastReq.Command)
	require.Equal(t, "test-session", approval.lastReq.Session)
	require.NotEmpty(t, approval.lastReq.JobID)

	s := readAudit(t, auditPath)
	require.Contains(t, s, `"decision":"approved"`)
	require.Contains(t, s, `"job_id":"`+approval.lastReq.JobID+`"`)
}

func TestBashApprovalRejected(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	exec := &fakeExecutor{name: "local"}
	approval := &fakeApproval{approved: false}
	tool := buildApprovalTool(t, exec, approval, auditPath)

	out, err := tool.Run(context.Background(), agent.BashInput{Command: "git push origin main"})
	require.NoError(t, err)
	require.Equal(t, -1, out.ExitCode)
	require.Contains(t, out.Stderr, "rejected by approval service")
	require.Equal(t, 0, exec.runCalls)
	require.Equal(t, 1, approval.calls)

	s := readAudit(t, auditPath)
	require.Contains(t, s, `"decision":"needs_approval"`)
	require.Contains(t, s, `"exit_code":-1`)
}

func TestBashApprovalError(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	exec := &fakeExecutor{name: "local"}
	approval := &fakeApproval{err: errors.New("webhook down")}
	tool := buildApprovalTool(t, exec, approval, auditPath)

	out, err := tool.Run(context.Background(), agent.BashInput{Command: "git push origin main"})
	require.NoError(t, err)
	require.Equal(t, -1, out.ExitCode)
	require.Equal(t, 0, exec.runCalls)

	s := readAudit(t, auditPath)
	require.Contains(t, s, `"decision":"needs_approval"`)
	require.Contains(t, s, "webhook down")
}

func TestBashApprovalNilService(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	exec := &fakeExecutor{name: "local"}
	tool := buildApprovalTool(t, exec, nil, auditPath)

	out, err := tool.Run(context.Background(), agent.BashInput{Command: "git push origin main"})
	require.NoError(t, err)
	require.Equal(t, -1, out.ExitCode)
	require.Contains(t, out.Stderr, "needs approval")
	require.Equal(t, 0, exec.runCalls)
}
