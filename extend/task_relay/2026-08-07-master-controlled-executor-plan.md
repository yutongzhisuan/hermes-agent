# Master 受控执行器 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 master/go 增加受控 bash 执行能力：Executor 抽象 + LocalBackend（本机沙箱）+ 策略引擎 + JSONL 审计 + bash_tool 暴露。

**Architecture:** master 是唯一策略决策点。bash_tool 收到调用后先经 `policy.Evaluate`（Allow/Deny/NeedsApproval，首轮 NeedsApproval 映射为 Deny+审计），Allow 后由 `executor.Run` 在本机沙箱执行（Linux=bubblewrap，darwin=进程级降级），全程 JSONL 审计 + OTel span 属性，审计写失败则拒绝执行（fail-closed）。

**Tech Stack:** Go 1.26，eino `tool.BaseTool`（`toolutils.InferTool`），yaml.v3，OTel trace，标准库 `os/exec`。

**Spec:** `extend/task_relay/2026-08-07-master-controlled-execution-design.md`

**运行测试：** 全部在 `extend/task_relay/master/go/` 目录下执行 `go test ./...`。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `agent/policy/policy.go` | Decision 三态 + Evaluator 接口 |
| `agent/policy/rules.go` | Rules 结构（YAML）+ 匹配逻辑 |
| `agent/policy/audit.go` | AuditLogger：JSONL 落盘（fail-closed） |
| `agent/executor/executor.go` | Executor 接口 + Spec/JobResult |
| `agent/executor/local.go` | LocalBackend：进程执行 + 超时/截断 + bwrap 探测 |
| `agent/bash_tool.go` | bash_tool：policy → executor → audit 组合 |
| `agent/exec_config.go` | ExecConfig / ExecFileConfig + 合并逻辑 |
| `agent/master.go` | Config 加 Exec 字段 + New() 注册 bash_tool |
| `agent/master_file_merge.go` | MergeFileIntoConfig 加 exec 段合并 |
| `cmd/master-demo/master.example.yaml` | exec 示例配置 |
| 各 `*_test.go` | 对应单测/集成测试 |

---

### Task 1: policy 包 — Decision 与 Evaluator

**Files:**
- Create: `agent/policy/policy.go`
- Test: `agent/policy/policy_test.go`

- [ ] **Step 1: 写失败测试**

```go
// agent/policy/policy_test.go
package policy_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/policy"
)

func TestDecisionString(t *testing.T) {
	require.Equal(t, "allow", policy.Allow.String())
	require.Equal(t, "deny", policy.Deny.String())
	require.Equal(t, "needs_approval", policy.NeedsApproval.String())
}
```

- [ ] **Step 2: 运行确认失败**

Run: `cd extend/task_relay/master/go && go test ./agent/policy/ -run TestDecisionString -v`
Expected: FAIL，提示 `policy` 包不存在或 `Allow` 未定义。

- [ ] **Step 3: 最小实现**

```go
// agent/policy/policy.go
package policy

// Decision is the policy verdict for one execution spec.
type Decision int

const (
	Allow Decision = iota
	Deny
	NeedsApproval
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Deny:
		return "deny"
	case NeedsApproval:
		return "needs_approval"
	default:
		return "unknown"
	}
}

// Evaluator decides whether an execution spec may run.
type Evaluator interface {
	Evaluate(command string) Decision
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./agent/policy/ -run TestDecisionString -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/policy/policy.go agent/policy/policy_test.go
git commit -m "feat(task_relay/master): policy Decision 三态与 Evaluator 接口"
```

---

### Task 2: policy 包 — Rules 与匹配逻辑

**Files:**
- Create: `agent/policy/rules.go`
- Test: `agent/policy/rules_test.go`

匹配语义：deny_list 子串匹配（命令行任意位置）；allow_list 命令头（首个 token）精确匹配；approval_list 命令头精确匹配；兜底按 Mode。裁决顺序：deny → allow → approval → mode 兜底。

- [ ] **Step 1: 写失败测试**

```go
// agent/policy/rules_test.go
package policy_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/policy"
)

func rules() policy.Rules {
	return policy.Rules{
		Mode:         policy.ModeDenyByDefault,
		AllowList:    []string{"ls", "git status"},
		DenyList:     []string{"rm -rf", "sudo"},
		ApprovalList: []string{"git push"},
	}
}

func TestDenyListWins(t *testing.T) {
	require.Equal(t, policy.Deny, policy.NewEvaluator(rules()).Evaluate("ls; rm -rf /"))
}

func TestAllowList(t *testing.T) {
	require.Equal(t, policy.Allow, policy.NewEvaluator(rules()).Evaluate("ls -la"))
	require.Equal(t, policy.Allow, policy.NewEvaluator(rules()).Evaluate("git status"))
}

func TestApprovalList(t *testing.T) {
	require.Equal(t, policy.NeedsApproval, policy.NewEvaluator(rules()).Evaluate("git push origin main"))
}

func TestDenyByDefaultFallback(t *testing.T) {
	require.Equal(t, policy.Deny, policy.NewEvaluator(rules()).Evaluate("curl example.com"))
}

func TestAllowWithDenyListMode(t *testing.T) {
	r := rules()
	r.Mode = policy.ModeAllowWithDenyList
	require.Equal(t, policy.Allow, policy.NewEvaluator(r).Evaluate("curl example.com"))
}

func TestEmptyCommandDenied(t *testing.T) {
	require.Equal(t, policy.Deny, policy.NewEvaluator(rules()).Evaluate("   "))
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./agent/policy/ -v`
Expected: FAIL，`Rules`/`NewEvaluator`/`ModeDenyByDefault` 未定义。

- [ ] **Step 3: 实现**

```go
// agent/policy/rules.go
package policy

import "strings"

// Mode is the fallback when no list matches.
type Mode string

const (
	ModeDenyByDefault    Mode = "deny_by_default"
	ModeAllowWithDenyList Mode = "allow_with_deny_list"
)

// Rules configures command gating.
type Rules struct {
	Mode         Mode     `json:"mode" yaml:"mode"`
	AllowList    []string `json:"allow_list" yaml:"allow_list"`
	DenyList     []string `json:"deny_list" yaml:"deny_list"`
	ApprovalList []string `json:"approval_list" yaml:"approval_list"`
}

type ruleEvaluator struct {
	rules Rules
}

// NewEvaluator builds an Evaluator from Rules.
func NewEvaluator(r Rules) Evaluator {
	if r.Mode == "" {
		r.Mode = ModeDenyByDefault
	}
	return &ruleEvaluator{rules: r}
}

func commandHead(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func (e *ruleEvaluator) Evaluate(command string) Decision {
	head := commandHead(command)
	if head == "" {
		return Deny
	}
	for _, d := range e.rules.DenyList {
		if strings.Contains(command, d) {
			return Deny
		}
	}
	for _, a := range e.rules.AllowList {
		if head == a || strings.HasPrefix(command, a+" ") {
			return Allow
		}
	}
	for _, a := range e.rules.ApprovalList {
		if head == a || strings.HasPrefix(command, a+" ") {
			return NeedsApproval
		}
	}
	if e.rules.Mode == ModeAllowWithDenyList {
		return Allow
	}
	return Deny
}
```

注意：`git status` 这类多词 allow 项用 `strings.HasPrefix(command, a+" ")` 处理，单词项（`ls`）用 `head == a` 或前缀都覆盖。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./agent/policy/ -v`
Expected: 全部 PASS。

- [ ] **Step 5: Commit**

```bash
git add agent/policy/rules.go agent/policy/rules_test.go
git commit -m "feat(task_relay/master): policy 规则匹配（deny/allow/approval + 兜底模式）"
```

---

### Task 3: executor 包 — 接口与类型

**Files:**
- Create: `agent/executor/executor.go`
- Test: `agent/executor/executor_test.go`

- [ ] **Step 1: 写失败测试**

```go
// agent/executor/executor_test.go
package executor_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/executor"
)

func TestSpecDefaults(t *testing.T) {
	s := executor.Spec{Command: "ls"}.WithDefaults(60*time.Second, 10*time.Minute, 1<<20)
	require.Equal(t, 60*time.Second, s.Timeout)
	require.Equal(t, int64(1<<20), s.MaxOutputBytes)
}

func TestSpecTimeoutClamped(t *testing.T) {
	s := executor.Spec{Command: "ls", Timeout: time.Hour}.WithDefaults(60*time.Second, 10*time.Minute, 1<<20)
	require.Equal(t, 10*time.Minute, s.Timeout)
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./agent/executor/ -v`
Expected: FAIL，包不存在。

- [ ] **Step 3: 实现**

```go
// agent/executor/executor.go
package executor

import (
	"context"
	"time"
)

// Spec describes one controlled command execution.
type Spec struct {
	Command        string
	WorkDir        string
	Timeout        time.Duration
	Env            map[string]string
	Backend        string // ""=auto | "local"（"remote" 二期）
	MaxOutputBytes int64
}

// WithDefaults fills empty fields and clamps Timeout to [1s, maxTimeout].
func (s Spec) WithDefaults(defaultTimeout, maxTimeout time.Duration, maxOutput int64) Spec {
	if s.Timeout <= 0 {
		s.Timeout = defaultTimeout
	}
	if s.Timeout > maxTimeout {
		s.Timeout = maxTimeout
	}
	if s.MaxOutputBytes <= 0 {
		s.MaxOutputBytes = maxOutput
	}
	if s.Backend == "" {
		s.Backend = "local"
	}
	return s
}

// JobResult summarizes one finished execution.
type JobResult struct {
	ExitCode           int
	Stdout, Stderr     string
	TimedOut, Canceled bool
	Backend            string
	StartedAt          time.Time
	FinishedAt         time.Time
}

// Executor runs a Spec in a controlled boundary.
type Executor interface {
	Run(ctx context.Context, spec Spec) (JobResult, error)
	Name() string
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./agent/executor/ -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add agent/executor/executor.go agent/executor/executor_test.go
git commit -m "feat(task_relay/master): Executor 接口与 Spec/JobResult 类型"
```

---

### Task 4: LocalBackend — 进程执行核心

**Files:**
- Create: `agent/executor/local.go`
- Test: `agent/executor/local_test.go`

首轮实现进程级执行：独立进程组 + 超时 kill 进程组 + 输出截断 + 退出码传播。bwrap 探测在 Task 5。

- [ ] **Step 1: 写失败测试**

```go
// agent/executor/local_test.go
package executor_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/executor"
)

func localExec(t *testing.T) executor.Executor {
	t.Helper()
	e, err := executor.NewLocal(executor.LocalOptions{})
	require.NoError(t, err)
	return e
}

func TestLocalEcho(t *testing.T) {
	res, err := localExec(t).Run(context.Background(), executor.Spec{
		Command: "echo hello",
	}.WithDefaults(10*time.Second, time.Minute, 1<<20))
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
	require.Equal(t, "hello\n", res.Stdout)
	require.False(t, res.TimedOut)
}

func TestLocalNonZeroExit(t *testing.T) {
	res, err := localExec(t).Run(context.Background(), executor.Spec{
		Command: "exit 3",
	}.WithDefaults(10*time.Second, time.Minute, 1<<20))
	require.NoError(t, err)
	require.Equal(t, 3, res.ExitCode)
}

func TestLocalTimeout(t *testing.T) {
	res, err := localExec(t).Run(context.Background(), executor.Spec{
		Command: "sleep 5",
		Timeout: 200 * time.Millisecond,
	}.WithDefaults(10*time.Second, time.Minute, 1<<20))
	require.NoError(t, err)
	require.True(t, res.TimedOut)
}

func TestLocalOutputTruncation(t *testing.T) {
	res, err := localExec(t).Run(context.Background(), executor.Spec{
		Command:        "seq 1 100000",
		MaxOutputBytes: 256,
	}.WithDefaults(10*time.Second, time.Minute, 1<<20))
	require.NoError(t, err)
	require.LessOrEqual(t, int64(len(res.Stdout)), int64(256))
}

func TestLocalEnvFilter(t *testing.T) {
	res, err := localExec(t).Run(context.Background(), executor.Spec{
		Command: "echo $EXEC_TEST_VAR",
		Env:     map[string]string{"EXEC_TEST_VAR": "filtered"},
	}.WithDefaults(10*time.Second, time.Minute, 1<<20))
	require.NoError(t, err)
	// Env 键过滤由 bash_tool 层做，local 只执行给定 env；此处断言透传
	require.Equal(t, "filtered\n", res.Stdout)
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./agent/executor/ -v`
Expected: FAIL，`NewLocal` 未定义。

- [ ] **Step 3: 实现**

```go
// agent/executor/local.go
package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// LocalOptions configures the local backend.
type LocalOptions struct {
	// Shell overrides the shell path; default /bin/sh.
	Shell string
}

type localBackend struct {
	shell string
}

// NewLocal builds a local-process executor.
func NewLocal(opts LocalOptions) (Executor, error) {
	shell := opts.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	if _, err := exec.LookPath(shell); err != nil {
		return nil, fmt.Errorf("shell %q: %w", shell, err)
	}
	return &localBackend{shell: shell}, nil
}

func (l *localBackend) Name() string { return "local" }

func (l *localBackend) Run(ctx context.Context, spec Spec) (JobResult, error) {
	res := JobResult{Backend: l.Name(), StartedAt: time.Now()}
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, l.shell, "-c", spec.Command)
	if spec.WorkDir != "" {
		cmd.Dir = spec.WorkDir
	}
	cmd.Env = mergeEnv(spec.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// kill 整个进程组，避免子进程泄漏
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, max: spec.MaxOutputBytes}
	cmd.Stderr = &limitedWriter{w: &stderr, max: spec.MaxOutputBytes}

	err := cmd.Run()
	res.FinishedAt = time.Now()
	res.Stdout = stdout.String()
	res.Stderr = stderr.String()

	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	}
	if ctx.Err() == context.Canceled {
		res.Canceled = true
		res.ExitCode = -1
		return res, nil
	}
	if err != nil {
		var exitErr *exec.ExitError
		if ok := errorAs(err, &exitErr); ok {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return res, fmt.Errorf("run: %w", err)
	}
	return res, nil
}

func errorAs(err error, target **exec.ExitError) bool {
	for err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// limitedWriter drops bytes beyond max but keeps consuming so the child doesn't block.
type limitedWriter struct {
	w   *bytes.Buffer
	max int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	remaining := l.max - int64(l.w.Len())
	if remaining > 0 {
		if int64(len(p)) > remaining {
			l.w.Write(p[:remaining])
		} else {
			l.w.Write(p)
		}
	}
	return len(p), nil
}
```

`mergeEnv` 见 Step 4 补到同文件末尾：

```go
// mergeEnv starts from the process env and overlays spec.Env.
// 白名单过滤在 bash_tool 层完成，这里只做覆盖合并。
func mergeEnv(overrides map[string]string) []string {
	base := map[string]string{}
	for _, kv := range syscall.Environ() {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				base[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	for k, v := range overrides {
		base[k] = v
	}
	out := make([]string, 0, len(base))
	for k, v := range base {
		out = append(out, k+"="+v)
	}
	return out
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./agent/executor/ -v`
Expected: 全部 PASS（echo/exit 3/timeout/截断/env）。

- [ ] **Step 5: Commit**

```bash
git add agent/executor/local.go agent/executor/local_test.go
git commit -m "feat(task_relay/master): LocalBackend 进程执行（进程组 kill/截断/超时）"
```

---

### Task 5: LocalBackend — bwrap 沙箱探测（Linux）

**Files:**
- Modify: `agent/executor/local.go`
- Test: `agent/executor/local_test.go`

Linux 且 `bwrap` 在 PATH 时，用 bubblewrap 包裹命令：只读绑定根、独立 mount/pid namespace、drop capabilities、绑定 workdir 可写。darwin 或 bwrap 缺失时走 Task 4 的进程级路径（warn 由调用方记）。

- [ ] **Step 1: 写失败测试**

```go
func TestLocalSandboxProbe(t *testing.T) {
	e, err := executor.NewLocal(executor.LocalOptions{})
	require.NoError(t, err)
	res, err := e.Run(context.Background(), executor.Spec{
		Command: "echo sandbox-ok",
	}.WithDefaults(10*time.Second, time.Minute, 1<<20))
	require.NoError(t, err)
	require.Equal(t, "sandbox-ok\n", res.Stdout)
	// bwrap 存在与否不影响功能，只影响隔离强度；这里只断言结果正确
}

func TestLocalSandboxedFlag(t *testing.T) {
	l, err := executor.NewLocal(executor.LocalOptions{})
	require.NoError(t, err)
	// 暴露是否启用了内核沙箱，便于审计/日志标注降级
	_ = l.(interface{ Sandboxed() bool }).Sandboxed()
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./agent/executor/ -run TestLocalSandbox -v`
Expected: FAIL，`Sandboxed()` 未定义。

- [ ] **Step 3: 实现**

在 `local.go` 中：

```go
// localBackend 增加字段
type localBackend struct {
	shell     string
	bwrapPath string // 非空表示启用 bubblewrap 沙箱
}

func NewLocal(opts LocalOptions) (Executor, error) {
	shell := opts.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	if _, err := exec.LookPath(shell); err != nil {
		return nil, fmt.Errorf("shell %q: %w", shell, err)
	}
	bwrapPath, _ := exec.LookPath("bwrap") // 缺失则降级进程级隔离
	return &localBackend{shell: shell, bwrapPath: bwrapPath}, nil
}

// Sandboxed reports whether kernel-level sandboxing (bubblewrap) is active.
func (l *localBackend) Sandboxed() bool { return l.bwrapPath != "" }
```

`Run` 里构造 cmd 改为：

```go
	args := []string{"-c", spec.Command}
	bin := l.shell
	if l.bwrapPath != "" {
		// 只读绑定根，dev/proc 独立，workdir 可写绑定，drop all caps
		bwrapArgs := []string{
			"--ro-bind", "/", "/",
			"--dev", "/dev",
			"--proc", "/proc",
			"--unshare-pid",
			"--unshare-ipc",
			"--cap-drop", "ALL",
			"--die-with-parent",
		}
		if spec.WorkDir != "" {
			bwrapArgs = append(bwrapArgs, "--bind", spec.WorkDir, spec.WorkDir, "--chdir", spec.WorkDir)
		}
		bwrapArgs = append(bwrapArgs, "--", l.shell, "-c", spec.Command)
		bin = l.bwrapPath
		args = bwrapArgs
	}
	cmd := exec.CommandContext(ctx, bin, args...)
```

（同时把原来 `exec.CommandContext(ctx, l.shell, "-c", spec.Command)` 替换为上面的 bin/args 构造，workdir 在 bwrap 路径下用 `--chdir`、非 bwrap 路径用 `cmd.Dir`。）

- [ ] **Step 4: 运行确认通过**

Run: `go test ./agent/executor/ -v`
Expected: 全部 PASS（mac 走降级路径，Linux CI 走 bwrap 路径）。

- [ ] **Step 5: Commit**

```bash
git add agent/executor/local.go agent/executor/local_test.go
git commit -m "feat(task_relay/master): LocalBackend bubblewrap 沙箱探测与降级"
```

---

### Task 6: policy 包 — AuditLogger（JSONL，fail-closed）

**Files:**
- Create: `agent/policy/audit.go`
- Test: `agent/policy/audit_test.go`

- [ ] **Step 1: 写失败测试**

```go
// agent/policy/audit_test.go
package policy_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/policy"
)

func TestAuditWritesJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	log, err := policy.NewAuditLogger(path)
	require.NoError(t, err)
	defer log.Close()

	err = log.Log(policy.AuditEntry{
		Command:  "ls",
		Backend:  "local",
		Decision: "allow",
		ExitCode: 0,
	})
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(bytes(data), &entry))
	require.Equal(t, "ls", entry["command"])
	require.Equal(t, "allow", entry["decision"])
}

func TestAuditFailClosed(t *testing.T) {
	// 路径指向不存在的深层目录且无法创建（文件占位）→ NewAuditLogger 报错
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	_, err := policy.NewAuditLogger(filepath.Join(blocker, "sub", "audit.jsonl"))
	require.Error(t, err)
}

func bytes(b []byte) []byte { return b[:len(b)-1] } // 去掉末尾换行
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./agent/policy/ -run TestAudit -v`
Expected: FAIL，`AuditLogger`/`AuditEntry` 未定义。

- [ ] **Step 3: 实现**

```go
// agent/policy/audit.go
package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditEntry is one execution audit record.
type AuditEntry struct {
	JobID      string
	Command    string
	Backend    string
	Decision   string
	ExitCode   int
	DurationMs int64
	Stdout     string // 不落盘，只记哈希与长度
	WorkDir    string
	Session    string
}

type auditRecord struct {
	TS         string `json:"ts"`
	JobID      string `json:"job_id"`
	Command    string `json:"command"`
	Backend    string `json:"backend"`
	Decision   string `json:"decision"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
	StdoutHash string `json:"stdout_hash"`
	StdoutLen  int    `json:"stdout_len"`
	WorkDir    string `json:"workdir"`
	Session    string `json:"session"`
}

// AuditLogger appends JSONL audit records. Writes are fail-closed:
// a write error is returned to the caller so the tool can refuse execution.
type AuditLogger struct {
	mu   sync.Mutex
	file *os.File
	enc  *json.Encoder
}

// NewAuditLogger opens (creating dirs as needed) the JSONL audit file.
func NewAuditLogger(path string) (*AuditLogger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("audit dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit open: %w", err)
	}
	return &AuditLogger{file: f, enc: json.NewEncoder(f)}, nil
}

// Log appends one record. Returns error on write failure (fail-closed).
func (l *AuditLogger) Log(e AuditEntry) error {
	sum := sha256.Sum256([]byte(e.Stdout))
	rec := auditRecord{
		TS:         time.Now().UTC().Format(time.RFC3339Nano),
		JobID:      e.JobID,
		Command:    e.Command,
		Backend:    e.Backend,
		Decision:   e.Decision,
		ExitCode:   e.ExitCode,
		DurationMs: e.DurationMs,
		StdoutHash: "sha256:" + hex.EncodeToString(sum[:]),
		StdoutLen:  len(e.Stdout),
		WorkDir:    e.WorkDir,
		Session:    e.Session,
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.enc.Encode(rec); err != nil {
		return fmt.Errorf("audit write: %w", err)
	}
	return l.file.Sync()
}

// Close closes the audit file.
func (l *AuditLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./agent/policy/ -v`
Expected: 全部 PASS。

- [ ] **Step 5: Commit**

```bash
git add agent/policy/audit.go agent/policy/audit_test.go
git commit -m "feat(task_relay/master): JSONL 审计 logger（fail-closed）"
```

---

### Task 7: bash_tool — 组合 policy + executor + audit

**Files:**
- Create: `agent/bash_tool.go`
- Test: `agent/bash_tool_test.go`

- [ ] **Step 1: 写失败测试**

```go
// agent/bash_tool_test.go
package agent_test

import (
	"context"
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
		Evaluator: policy.NewEvaluator(rules),
		Executor:  exec,
		Audit:     audit,
		Limits:    agent.ExecLimits{TimeoutDefault: 30 * time.Second, TimeoutMax: time.Minute, MaxOutputBytes: 1 << 20},
		EnvAllowKeys: []string{"PATH", "HOME"},
		Session:   "test-session",
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
	// SECRET_TOKEN 不在 env_allow_keys，被剥离
	require.Equal(t, "\n", out.Stdout)
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./agent/ -run TestBashTool -v`
Expected: FAIL，`agent.BashTool` 等未定义。

- [ ] **Step 3: 实现**

```go
// agent/bash_tool.go
package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/infa/task_relay/master/agent/executor"
	"github.com/infa/task_relay/master/agent/policy"
)

// ExecLimits bounds one execution.
type ExecLimits struct {
	TimeoutDefault time.Duration
	TimeoutMax     time.Duration
	MaxOutputBytes int64
}

// BashToolDeps wires the bash tool's collaborators.
type BashToolDeps struct {
	Evaluator    policy.Evaluator
	Executor     executor.Executor
	Audit        *policy.AuditLogger
	Limits       ExecLimits
	EnvAllowKeys []string
	Session      string
}

// BashTool executes shell commands under policy + audit control.
type BashTool struct {
	deps BashToolDeps
}

// NewBashTool builds a BashTool.
func NewBashTool(deps BashToolDeps) *BashTool { return &BashTool{deps: deps} }

// BashInput is the argument schema for bash.
type BashInput struct {
	Command        string            `json:"command" jsonschema:"required,description=Shell command to execute"`
	WorkDir        string            `json:"workdir,omitempty" jsonschema:"description=Working directory; defaults to master working dir"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty" jsonschema:"description=Timeout in seconds; default and max from exec.limits"`
	Env            map[string]string `json:"env,omitempty" jsonschema:"description=Extra env vars; keys outside exec.policy.env_allow_keys are stripped"`
}

// BashOutput is the structured result.
type BashOutput struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timed_out"`
}

// Run executes the command if policy allows, always auditing.
func (b *BashTool) Run(ctx context.Context, in BashInput) (BashOutput, error) {
	d := b.deps
	spec := executor.Spec{
		Command: in.Command,
		WorkDir: in.WorkDir,
		Env:     filterEnv(in.Env, d.EnvAllowKeys),
	}
	if in.TimeoutSeconds > 0 {
		spec.Timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}
	spec = spec.WithDefaults(d.Limits.TimeoutDefault, d.Limits.TimeoutMax, d.Limits.MaxOutputBytes)

	decision := d.Evaluator.Evaluate(spec.Command)
	entry := policy.AuditEntry{
		JobID:    uuid.NewString(),
		Command:  spec.Command,
		Decision: decision.String(),
		WorkDir:  spec.WorkDir,
		Session:  d.Session,
	}

	switch decision {
	case policy.Deny:
		return b.deny(entry, "denied by policy")
	case policy.NeedsApproval:
		return b.deny(entry, "needs approval (approval workflow not yet enabled)")
	}

	entry.Backend = d.Executor.Name()
	res, err := d.Executor.Run(ctx, spec)
	if err != nil {
		entry.ExitCode = -1
		entry.Stdout = err.Error()
		if logErr := d.Audit.Log(entry); logErr != nil {
			return BashOutput{}, fmt.Errorf("execution failed (%v) and audit failed: %w", err, logErr)
		}
		return BashOutput{}, err
	}

	entry.ExitCode = res.ExitCode
	entry.DurationMs = res.FinishedAt.Sub(res.StartedAt).Milliseconds()
	entry.Stdout = res.Stdout
	if logErr := d.Audit.Log(entry); logErr != nil {
		return BashOutput{}, fmt.Errorf("audit failed after execution: %w", logErr)
	}
	return BashOutput{
		ExitCode: res.ExitCode,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		TimedOut: res.TimedOut,
	}, nil
}

func (b *BashTool) deny(entry policy.AuditEntry, reason string) (BashOutput, error) {
	entry.ExitCode = -1
	if err := b.deps.Audit.Log(entry); err != nil {
		return BashOutput{}, fmt.Errorf("denied and audit failed: %w", err)
	}
	return BashOutput{ExitCode: -1, Stderr: reason}, nil
}

func filterEnv(env map[string]string, allowKeys []string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	allow := make(map[string]struct{}, len(allowKeys))
	for _, k := range allowKeys {
		allow[k] = struct{}{}
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		if _, ok := allow[k]; ok {
			out[k] = v
		}
	}
	return out
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./agent/ -run TestBashTool -v`
Expected: 4 个测试全部 PASS。

- [ ] **Step 5: Commit**

```bash
git add agent/bash_tool.go agent/bash_tool_test.go
git commit -m "feat(task_relay/master): bash_tool 组合策略/执行/审计"
```

---

### Task 8: 配置接入 — ExecConfig + 文件合并 + New() 注册

**Files:**
- Create: `agent/exec_config.go`
- Modify: `agent/master_file_merge.go`（MergeFileIntoConfig 加 exec 段）
- Modify: `agent/master.go`（Config 加 Exec 字段 + New() 注册 bash_tool）
- Test: `agent/exec_config_test.go`

- [ ] **Step 1: 写失败测试**

```go
// agent/exec_config_test.go
package agent_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent"
)

func TestExecConfigMerge(t *testing.T) {
	file := &agent.MasterFileConfig{
		Exec: &agent.ExecFileConfig{
			Enabled:        true,
			DefaultBackend: "local",
			Policy: &agent.ExecPolicyFileConfig{
				Mode:      "deny_by_default",
				AllowList: []string{"ls"},
			},
		},
	}
	cfg, _, err := agent.MergeFileIntoConfig(agent.Config{}, file)
	require.NoError(t, err)
	require.NotNil(t, cfg.Exec)
	require.True(t, cfg.Exec.Enabled)
	require.Equal(t, []string{"ls"}, cfg.Exec.Policy.AllowList)
}

func TestExecConfigDefaults(t *testing.T) {
	cfg := agent.ExecConfig{}.WithDefaults()
	require.Equal(t, 60*time.Second, cfg.Limits.TimeoutDefault)
	require.Equal(t, 10*time.Minute, cfg.Limits.TimeoutMax)
	require.Equal(t, int64(1<<20), cfg.Limits.MaxOutputBytes)
	require.Contains(t, cfg.AuditPath, "exec-audit.jsonl")
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./agent/ -run TestExecConfig -v`
Expected: FAIL，`ExecConfig`/`ExecFileConfig` 未定义。

- [ ] **Step 3: 实现**

```go
// agent/exec_config.go
package agent

import (
	"os"
	"path/filepath"
	"time"

	"github.com/infa/task_relay/master/agent/policy"
)

// ExecFileConfig is the exec section of master.yaml.
type ExecFileConfig struct {
	Enabled        bool                   `json:"enabled" yaml:"enabled"`
	DefaultBackend string                 `json:"default_backend" yaml:"default_backend"`
	Policy         *ExecPolicyFileConfig  `json:"policy" yaml:"policy"`
	Limits         *ExecLimitsFileConfig  `json:"limits" yaml:"limits"`
	Audit          *ExecAuditFileConfig   `json:"audit" yaml:"audit"`
}

// ExecPolicyFileConfig is the exec.policy section.
type ExecPolicyFileConfig struct {
	Mode         string   `json:"mode" yaml:"mode"`
	AllowList    []string `json:"allow_list" yaml:"allow_list"`
	DenyList     []string `json:"deny_list" yaml:"deny_list"`
	ApprovalList []string `json:"approval_list" yaml:"approval_list"`
	EnvAllowKeys []string `json:"env_allow_keys" yaml:"env_allow_keys"`
}

// ExecLimitsFileConfig is the exec.limits section (durations as strings).
type ExecLimitsFileConfig struct {
	TimeoutDefault string `json:"timeout_default" yaml:"timeout_default"`
	TimeoutMax     string `json:"timeout_max" yaml:"timeout_max"`
	MaxOutputBytes int64  `json:"max_output_bytes" yaml:"max_output_bytes"`
}

// ExecAuditFileConfig is the exec.audit section.
type ExecAuditFileConfig struct {
	Path string `json:"path" yaml:"path"`
}

// ExecConfig is the resolved runtime exec configuration.
type ExecConfig struct {
	Enabled        bool
	DefaultBackend string
	Policy         policy.Rules
	EnvAllowKeys   []string
	Limits         ExecLimits
	AuditPath      string
}

// WithDefaults fills unset limits and audit path.
func (c ExecConfig) WithDefaults() ExecConfig {
	if c.Limits.TimeoutDefault <= 0 {
		c.Limits.TimeoutDefault = 60 * time.Second
	}
	if c.Limits.TimeoutMax <= 0 {
		c.Limits.TimeoutMax = 10 * time.Minute
	}
	if c.Limits.MaxOutputBytes <= 0 {
		c.Limits.MaxOutputBytes = 1 << 20
	}
	if c.AuditPath == "" {
		home, _ := os.UserHomeDir()
		c.AuditPath = filepath.Join(home, ".task-relay", "exec-audit.jsonl")
	}
	if c.DefaultBackend == "" {
		c.DefaultBackend = "local"
	}
	return c
}

// execConfigFromFile converts file config to runtime ExecConfig.
func execConfigFromFile(f *ExecFileConfig) *ExecConfig {
	if f == nil {
		return nil
	}
	cfg := &ExecConfig{Enabled: f.Enabled, DefaultBackend: f.DefaultBackend}
	if f.Policy != nil {
		cfg.Policy = policy.Rules{
			Mode:         policy.Mode(f.Policy.Mode),
			AllowList:    f.Policy.AllowList,
			DenyList:     f.Policy.DenyList,
			ApprovalList: f.Policy.ApprovalList,
		}
		cfg.EnvAllowKeys = f.Policy.EnvAllowKeys
	}
	if f.Limits != nil {
		if d, err := time.ParseDuration(f.Limits.TimeoutDefault); err == nil {
			cfg.Limits.TimeoutDefault = d
		}
		if d, err := time.ParseDuration(f.Limits.TimeoutMax); err == nil {
			cfg.Limits.TimeoutMax = d
		}
		cfg.Limits.MaxOutputBytes = f.Limits.MaxOutputBytes
	}
	if f.Audit != nil {
		cfg.AuditPath = f.Audit.Path
	}
	return cfg
}
```

在 `agent/master_file_merge.go` 的 `MasterFileConfig`（定义在 `agent/mcp_config.go`）中增加字段：

```go
// mcp_config.go MasterFileConfig 增加：
	Exec *ExecFileConfig `json:"exec" yaml:"exec"`
```

在 `MergeFileIntoConfig` 末尾（`if cfg.Search == nil` 之后）增加：

```go
	if cfg.Exec == nil {
		cfg.Exec = execConfigFromFile(file.Exec)
	}
	if cfg.Exec != nil {
		*cfg.Exec = cfg.Exec.WithDefaults()
	}
```

在 `agent/master.go` 的 `Config` 结构体增加字段：

```go
	// Exec enables the controlled bash tool when non-nil and Enabled.
	Exec *ExecConfig
```

在 `New()` 的工具组装段（`tools = append(tools, searchTools...)` 之后、`ensureUniqueToolNames` 之前）增加：

```go
		if cfg.Exec != nil && cfg.Exec.Enabled {
			bashTool, bashErr := buildBashTool(cfg)
			if bashErr != nil {
				if mcpClose != nil {
					_ = mcpClose()
				}
				_ = closeHub(hub)
				_ = shutdownTracing(context.Background())
				return nil, bashErr
			}
			if bashTool != nil {
				tools = append(tools, bashTool)
			}
		}
```

并在 `master.go` 增加 builder（放文件末尾）：

```go
// buildBashTool wires policy + local executor + audit into an eino tool.
// Returns nil (no tool) when the configured backend is "remote"（二期）.
func buildBashTool(cfg Config) (tool.BaseTool, error) {
	execCfg := *cfg.Exec
	if execCfg.DefaultBackend == "remote" {
		// RemoteBackend 二期：本轮只提供 local
		return nil, nil
	}
	exec, err := executor.NewLocal(executor.LocalOptions{})
	if err != nil {
		return nil, fmt.Errorf("exec local backend: %w", err)
	}
	audit, err := policy.NewAuditLogger(execCfg.AuditPath)
	if err != nil {
		return nil, fmt.Errorf("exec audit: %w", err)
	}
	bash := NewBashTool(BashToolDeps{
		Evaluator:    policy.NewEvaluator(execCfg.Policy),
		Executor:     exec,
		Audit:        audit,
		Limits:       execCfg.Limits,
		EnvAllowKeys: execCfg.EnvAllowKeys,
		Session:      cfg.MasterSession,
	})
	t, err := toolutils.InferTool(
		"bash",
		"Execute a shell command under policy control (allow-list, audit). Use for local system commands.",
		bash.Run,
	)
	if err != nil {
		return nil, fmt.Errorf("bash tool: %w", err)
	}
	return t, nil
}
```

`master.go` 头部 import 增加：

```go
	"github.com/infa/task_relay/master/agent/executor"
	"github.com/infa/task_relay/master/agent/policy"
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./agent/ -run TestExecConfig -v && go build ./...`
Expected: 测试 PASS，构建无错。

- [ ] **Step 5: Commit**

```bash
git add agent/exec_config.go agent/exec_config_test.go agent/master.go agent/master_file_merge.go agent/mcp_config.go
git commit -m "feat(task_relay/master): exec 配置接入与 bash_tool 注册"
```

---

### Task 9: 示例配置 + 端到端集成测试

**Files:**
- Modify: `cmd/master-demo/master.example.yaml`
- Test: `agent/bash_tool_integration_test.go`

- [ ] **Step 1: 更新示例配置**

在 `cmd/master-demo/master.example.yaml` 的 `search:` 段之后追加：

```yaml
# Controlled shell execution (disabled by default).
exec:
  enabled: false   # 设为 true 启用 bash 工具
  default_backend: local
  policy:
    mode: deny_by_default
    allow_list: ["ls", "cat", "grep", "git status", "go test", "make"]
    deny_list: ["rm -rf", "sudo", "curl | sh"]
    approval_list: ["git push", "kubectl"]
    env_allow_keys: ["PATH", "HOME", "GOPATH"]
  limits:
    timeout_default: 60s
    timeout_max: 10m
    max_output_bytes: 1048576
  audit:
    path: ~/.task-relay/exec-audit.jsonl
```

- [ ] **Step 2: 写集成测试**

```go
// agent/bash_tool_integration_test.go
package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent"
)

// 验证 New() 从文件配置构建 bash_tool 并真实执行一条白名单命令。
func TestMasterExecEndToEnd(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	yaml := `
exec:
  enabled: true
  policy:
    mode: deny_by_default
    allow_list: ["echo"]
  audit:
    path: ` + auditPath + `
`
	cfgPath := filepath.Join(t.TempDir(), "master.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o644))

	fileCfg, err := agent.LoadMasterConfigFile(cfgPath)
	require.NoError(t, err)
	cfg, _, err := agent.MergeFileIntoConfig(agent.Config{MasterSession: "it"}, fileCfg)
	require.NoError(t, err)
	require.NotNil(t, cfg.Exec)
	require.True(t, cfg.Exec.Enabled)
	require.Equal(t, auditPath, cfg.Exec.AuditPath)

	// 直接构建 bash_tool 跑一条 echo，断言审计文件有记录
	tool, err := agent.BuildBashToolForTest(cfg)
	require.NoError(t, err)
	require.NotNil(t, tool)

	out, err := tool.(interface {
		Run(context.Context, agent.BashInput) (agent.BashOutput, error)
	}).Run(context.Background(), agent.BashInput{Command: "echo e2e"})
	require.NoError(t, err)
	require.Equal(t, "e2e\n", out.Stdout)

	data, err := os.ReadFile(auditPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"command":"echo e2e"`)
	require.Contains(t, string(data), `"decision":"allow"`)
}
```

为此在 `agent/bash_tool.go` 增加测试辅助导出（生产代码里正常，测试用）：

```go
// BuildBashToolForTest exposes buildBashTool for package-external tests.
func BuildBashToolForTest(cfg Config) (any, error) { return buildBashTool(cfg) }
```

注意：若倾向不导出测试辅助，可把集成测试改为 `package agent`（内部测试）。选择内部测试更符合"不在生产 API 留测试口"。**决策：把 `bash_tool_integration_test.go` 声明为 `package agent`（内部测试），直接调 `buildBashTool`，不加 `BuildBashToolForTest`。**

- [ ] **Step 3: 运行全部测试**

Run: `cd extend/task_relay/master/go && go test ./... -count=1`
Expected: 全部 PASS。

- [ ] **Step 4: Commit**

```bash
git add cmd/master-demo/master.example.yaml agent/bash_tool_integration_test.go
git commit -m "feat(task_relay/master): exec 示例配置与端到端集成测试"
```

---

## Self-Review 记录

- **Spec 覆盖**：策略三态（T1/T2）、Executor/LocalBackend（T3/T4）、bwrap/降级（T5）、审计 fail-closed（T6）、bash_tool 组合（T7）、配置接入+注册（T8）、示例+集成测试（T9）——spec 首轮范围全覆盖。RemoteBackend 接口预留由 `executor.Executor` 接口 + `default_backend: remote → nil` 承载。
- **占位符扫描**：无 TBD/TODO；所有代码步骤含完整实现。
- **类型一致性**：`policy.Rules`（T2）与 `ExecConfig.Policy`（T8）一致；`executor.Spec.WithDefaults`（T3）在 T4/T7 使用签名一致；`BashToolDeps`（T7）与 T8 builder 一致。
