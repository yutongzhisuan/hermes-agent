package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/infa/task_relay/master/agent/policy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
	return path
}

func TestHookAllow(t *testing.T) {
	dir := t.TempDir()
	allow := writeScript(t, dir, "allow.sh", "#!/bin/sh\nexit 0\n")
	r := &Runner{Hooks: []Hook{{Command: allow}}}
	assert.NoError(t, r.Check(context.Background(), "bash", `{"cmd":"ls"}`))
}

func TestHookBlock(t *testing.T) {
	dir := t.TempDir()
	block := writeScript(t, dir, "block.sh", "#!/bin/sh\necho \"policy violation: not today\"\nexit 1\n")
	r := &Runner{Hooks: []Hook{{Command: block}}}
	err := r.Check(context.Background(), "bash", `{"cmd":"rm -rf /"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked by hook")
	assert.Contains(t, err.Error(), "not today")
}

func TestHookTimeout(t *testing.T) {
	dir := t.TempDir()
	slow := writeScript(t, dir, "slow.sh", "#!/bin/sh\nsleep 10\n")
	r := &Runner{Hooks: []Hook{{Command: slow, TimeoutSeconds: 1}}}
	err := r.Check(context.Background(), "bash", `{"cmd":"ls"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestHookNotFound(t *testing.T) {
	r := &Runner{Hooks: []Hook{{Command: filepath.Join(t.TempDir(), "no-such-hook.sh")}}}
	err := r.Check(context.Background(), "bash", `{"cmd":"ls"}`)
	require.Error(t, err)
}

func TestHookReceivesPayload(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "payload.json")
	script := fmt.Sprintf("#!/bin/sh\ncat > %s\nexit 0\n", out)
	capture := writeScript(t, dir, "capture.sh", script)
	r := &Runner{Hooks: []Hook{{Command: capture}}}
	require.NoError(t, r.Check(context.Background(), "bash", `{"cmd":"ls -la"}`))
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"tool":"bash"`)
	assert.Contains(t, string(data), `"cmd":"ls -la"`)
}

type fakeTool struct {
	name  string
	calls int
	args  string
}

func (f *fakeTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: f.name}, nil
}

func (f *fakeTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	f.calls++
	f.args = argumentsInJSON
	return "ok", nil
}

type infoOnlyTool struct{}

func (infoOnlyTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "info-only"}, nil
}

func TestHookedToolPassesThrough(t *testing.T) {
	dir := t.TempDir()
	allow := writeScript(t, dir, "allow.sh", "#!/bin/sh\nexit 0\n")
	r := &Runner{Hooks: []Hook{{Command: allow}}}
	fake := &fakeTool{name: "fake"}
	wrapped := r.Wrap(fake)
	out, err := wrapped.InvokableRun(context.Background(), `{"x":1}`)
	require.NoError(t, err)
	assert.Equal(t, "ok", out)
	assert.Equal(t, 1, fake.calls)
	assert.Equal(t, `{"x":1}`, fake.args)
}

func TestHookedToolBlocks(t *testing.T) {
	dir := t.TempDir()
	block := writeScript(t, dir, "block.sh", "#!/bin/sh\necho nope\nexit 1\n")
	r := &Runner{Hooks: []Hook{{Command: block}}}
	fake := &fakeTool{name: "fake"}
	wrapped := r.Wrap(fake)
	_, err := wrapped.InvokableRun(context.Background(), `{"x":1}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked by hook")
	assert.Equal(t, 0, fake.calls)
}

func TestHookedToolUsesInnerName(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "payload.json")
	script := fmt.Sprintf("#!/bin/sh\ncat > %s\nexit 0\n", out)
	capture := writeScript(t, dir, "capture.sh", script)
	r := &Runner{Hooks: []Hook{{Command: capture}}}
	fake := &fakeTool{name: "fake"}
	wrapped := r.Wrap(fake)
	_, err := wrapped.InvokableRun(context.Background(), `{}`)
	require.NoError(t, err)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"tool":"fake"`)
}

func TestWrapAll(t *testing.T) {
	dir := t.TempDir()
	allow := writeScript(t, dir, "allow.sh", "#!/bin/sh\nexit 0\n")
	r := &Runner{Hooks: []Hook{{Command: allow}}}
	fake := &fakeTool{name: "fake"}
	nonInvocable := &infoOnlyTool{}
	wrapped := r.WrapAll([]tool.BaseTool{fake, nonInvocable})
	require.Len(t, wrapped, 2)
	_, err := wrapped[0].(tool.InvokableTool).InvokableRun(context.Background(), `{}`)
	require.NoError(t, err)
	assert.Equal(t, 1, fake.calls)
	assert.Same(t, nonInvocable, wrapped[1])
}

type safeTool struct{ name string }

func (s *safeTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: s.name}, nil
}

func (s *safeTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	return "ok", nil
}

type flakyInfoTool struct {
	name  string
	calls atomic.Int64
}

func (f *flakyInfoTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	if f.calls.Add(1) == 1 {
		return nil, fmt.Errorf("info not ready")
	}
	return &schema.ToolInfo{Name: f.name}, nil
}

func (f *flakyInfoTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	return "ok", nil
}

func TestHookedToolConcurrentNameResolution(t *testing.T) {
	dir := t.TempDir()
	allow := writeScript(t, dir, "allow.sh", "#!/bin/sh\nexit 0\n")
	r := &Runner{Hooks: []Hook{{Command: allow}}}

	runConcurrent := func(t *testing.T, wrapped tool.InvokableTool) {
		t.Helper()
		var wg sync.WaitGroup
		errs := make([]error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				out, err := wrapped.InvokableRun(context.Background(), `{"x":1}`)
				if err == nil && out != "ok" {
					err = fmt.Errorf("unexpected output %q", out)
				}
				errs[i] = err
			}(i)
		}
		wg.Wait()
		for _, err := range errs {
			assert.NoError(t, err)
		}
	}

	t.Run("eager", func(t *testing.T) {
		wrapped := r.Wrap(&safeTool{name: "fake"})
		runConcurrent(t, wrapped)
	})

	t.Run("lazy_once_fallback", func(t *testing.T) {
		wrapped := r.Wrap(&flakyInfoTool{name: "flaky"})
		runConcurrent(t, wrapped)
	})
}

func TestHookBlockAudited(t *testing.T) {
	dir := t.TempDir()
	block := writeScript(t, dir, "block.sh", "#!/bin/sh\necho \"denied by policy\"\nexit 1\n")
	auditPath := filepath.Join(dir, "audit.jsonl")
	audit, err := policy.NewAuditLogger(auditPath)
	require.NoError(t, err)
	defer audit.Close()
	r := &Runner{Hooks: []Hook{{Command: block}}, Audit: audit, Session: "sess-1"}
	err = r.Check(context.Background(), "bash", `{"cmd":"ls"}`)
	require.Error(t, err)
	require.NoError(t, audit.Close())

	data, err := os.ReadFile(auditPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 1)
	var rec struct {
		Operation string `json:"op"`
		Command   string `json:"command"`
		Decision  string `json:"decision"`
		ExitCode  int    `json:"exit_code"`
		Error     string `json:"error"`
		Session   string `json:"session"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &rec))
	assert.Equal(t, "hook_block", rec.Operation)
	assert.Equal(t, "bash", rec.Command)
	assert.Equal(t, "deny", rec.Decision)
	assert.Equal(t, -1, rec.ExitCode)
	assert.Contains(t, rec.Error, "blocked by hook")
	assert.Equal(t, "sess-1", rec.Session)
}
