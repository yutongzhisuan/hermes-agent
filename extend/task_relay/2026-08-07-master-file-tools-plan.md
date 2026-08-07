# Master 文件操作工具（第二轮）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 master/go 增加受控文件操作能力：view / write / edit / multiedit 四个工具，接入 policy 路径维度（root 限制 + glob 白/黑名单）与既有 JSONL 审计。

**Architecture:** 复用首轮建立的三段组合：`filetools` → `policy.PathEvaluator`（路径裁决）→ 文件 IO → `policy.AuditLogger`。所有文件操作默认限制在 root（master 工作目录）内，越界路径仅当命中 allow_paths 绝对 glob 才放行；deny_paths 永远优先。写操作原子化（multiedit 全成功才落盘）。

**Tech Stack:** Go 1.26，eino `toolutils.InferTool`，`github.com/bmatcuk/doublestar/v4`（glob，已在 go.mod indirect，提升为 direct），既有 `agent/policy` + `agent/executor` 包。

**Spec:** `extend/task_relay/2026-08-07-master-controlled-execution-design.md`（蓝图 §6 第二轮）

**运行测试：** `cd extend/task_relay/master/go && go test ./...`

**代码注释与 commit message 一律英文（用户 2026-08-07 明确要求）。**

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `agent/policy/paths.go` | PathRules + PathEvaluator（root 限制 + glob 匹配 + symlink 解析） |
| `agent/policy/audit.go` | AuditEntry/auditRecord 增加 Operation 字段（修改既有文件） |
| `agent/filetools/filetools.go` | Deps + 路径解析 helper + 审计 helper |
| `agent/filetools/view.go` | view 工具（读，行范围 + 字节截断） |
| `agent/filetools/write.go` | write 工具（创建/覆盖，大小限制） |
| `agent/filetools/edit.go` | edit（唯一匹配替换）+ multiedit（原子多替换） |
| `agent/file_config.go` | FileToolsFileConfig / FileToolsConfig + 合并 |
| `agent/master.go` | Config 加 File 字段 + New() 注册 file tools |
| `agent/mcp_config.go` | MasterFileConfig 加 File 字段 |
| `cmd/master-demo/master.example.yaml` | file 示例段 |
| 各 `*_test.go` | 对应测试 |

---

### Task 1: policy 路径维度 + 审计 Operation 字段

**Files:**
- Create: `agent/policy/paths.go`
- Modify: `agent/policy/audit.go`（加 Operation 字段）
- Test: `agent/policy/paths_test.go`
- 依赖：`go get github.com/bmatcuk/doublestar/v4`（已在 go.sum indirect）

- [ ] **Step 1: 写失败测试**

```go
// agent/policy/paths_test.go
package policy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/policy"
)

func newRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=1"), 0o644))
	return root
}

func eval(t *testing.T, root string, rules policy.PathRules) policy.PathEvaluator {
	t.Helper()
	e, err := policy.NewPathEvaluator(root, rules)
	require.NoError(t, err)
	return e
}

func TestPathInsideRootAllowed(t *testing.T) {
	e := eval(t, newRoot(t), policy.PathRules{})
	require.Equal(t, policy.Allow, e.EvaluatePath("src/main.go"))
}

func TestPathAbsoluteInsideRootAllowed(t *testing.T) {
	root := newRoot(t)
	e := eval(t, root, policy.PathRules{})
	require.Equal(t, policy.Allow, e.EvaluatePath(filepath.Join(root, "src/main.go")))
}

func TestPathEscapeDenied(t *testing.T) {
	e := eval(t, newRoot(t), policy.PathRules{})
	require.Equal(t, policy.Deny, e.EvaluatePath("../outside.txt"))
	require.Equal(t, policy.Deny, e.EvaluatePath("/etc/passwd"))
}

func TestPathDenyList(t *testing.T) {
	e := eval(t, newRoot(t), policy.PathRules{DenyList: []string{".env", "**/*.pem"}})
	require.Equal(t, policy.Deny, e.EvaluatePath(".env"))
	require.Equal(t, policy.Deny, e.EvaluatePath("certs/server.pem"))
}

func TestPathAllowListWhitelistMode(t *testing.T) {
	e := eval(t, newRoot(t), policy.PathRules{AllowList: []string{"src/**"}})
	require.Equal(t, policy.Allow, e.EvaluatePath("src/main.go"))
	require.Equal(t, policy.Deny, e.EvaluatePath("README.md"))
}

func TestPathAllowListAbsoluteOverride(t *testing.T) {
	root := newRoot(t)
	shared := t.TempDir()
	e := eval(t, root, policy.PathRules{AllowList: []string{"src/**", shared + "/**"}})
	require.Equal(t, policy.Allow, e.EvaluatePath(filepath.Join(shared, "data.json")))
	require.Equal(t, policy.Deny, e.EvaluatePath("/etc/passwd"))
}

func TestPathSymlinkEscapeDenied(t *testing.T) {
	root := newRoot(t)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))
	e := eval(t, root, policy.PathRules{})
	require.Equal(t, policy.Deny, e.EvaluatePath("link/secret.txt"))
}

func TestPathNonexistentFileAllowed(t *testing.T) {
	// write targets may not exist yet; resolution must not fail on missing file
	e := eval(t, newRoot(t), policy.PathRules{})
	require.Equal(t, policy.Allow, e.EvaluatePath("new/dir/file.txt"))
}
```

- [ ] **Step 2: 运行确认失败**

Run: `cd extend/task_relay/master/go && go get github.com/bmatcuk/doublestar/v4 && go test ./agent/policy/ -run TestPath -v`
Expected: FAIL（NewPathEvaluator 未定义）

- [ ] **Step 3: 实现**

```go
// agent/policy/paths.go
package policy

import (
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// PathRules gates filesystem access by glob patterns.
// Patterns are matched against both the root-relative path and the absolute path.
type PathRules struct {
	AllowList []string `json:"allow_list" yaml:"allow_list"`
	DenyList  []string `json:"deny_list" yaml:"deny_list"`
}

// PathEvaluator decides whether a filesystem path may be accessed.
type PathEvaluator interface {
	EvaluatePath(path string) Decision
	Root() string
}

type pathEvaluator struct {
	root  string
	rules PathRules
}

// NewPathEvaluator resolves root to an absolute symlink-free path.
func NewPathEvaluator(root string, rules PathRules) (PathEvaluator, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return &pathEvaluator{root: abs, rules: rules}, nil
}

func (e *pathEvaluator) Root() string { return e.root }

func (e *pathEvaluator) EvaluatePath(path string) Decision {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(e.root, abs)
	}
	abs = filepath.Clean(abs)
	abs = resolveSymlinks(abs)

	rel, relErr := filepath.Rel(e.root, abs)
	insideRoot := relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))

	for _, d := range e.rules.DenyList {
		if matchAny(d, rel, abs) {
			return Deny
		}
	}

	if !insideRoot {
		// Outside root: only an absolute allow glob may grant access.
		for _, a := range e.rules.AllowList {
			if filepath.IsAbs(a) && matchAny(a, rel, abs) {
				return Allow
			}
		}
		return Deny
	}

	if len(e.rules.AllowList) == 0 {
		return Allow
	}
	for _, a := range e.rules.AllowList {
		if !filepath.IsAbs(a) && matchAny(a, rel, abs) {
			return Allow
		}
	}
	return Deny
}

// resolveSymlinks resolves as much of the path as exists; missing tails are kept as-is.
func resolveSymlinks(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	dir := filepath.Dir(p)
	base := filepath.Base(p)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(resolved, base)
	}
	return p
}

func matchAny(pattern, rel, abs string) bool {
	if ok, _ := doublestar.Match(pattern, rel); ok {
		return true
	}
	ok, _ := doublestar.Match(pattern, abs)
	return ok
}
```

`agent/policy/audit.go` 修改——AuditEntry 加字段：

```go
type AuditEntry struct {
	Operation  string // "bash" | "file_view" | "file_write" | "file_edit"
	JobID      string
	Command    string
	Backend    string
	Decision   string
	ExitCode   int
	DurationMs int64
	Stdout     string
	Stderr     string
	Error      string
	WorkDir    string
	Session    string
}
```

auditRecord 加：

```go
	Operation  string `json:"op"`
```

Log() 里 `rec.Operation = e.Operation`（放 TS 之后）。bash_tool.go 的 entry 构造加 `Operation: "bash"`（两处：entry 初始化）。同步更新 audit_test.go 若有断言受影响（op 字段存在即可，不必断言具体值——在 paths 集成时由 filetools 测试覆盖）。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./agent/... -count=1`
Expected: 全部 PASS（新路径测试 + 既有测试）

- [ ] **Step 5: Commit**

```bash
git add extend/task_relay/master/go/agent/policy/ extend/task_relay/master/go/agent/bash_tool.go extend/task_relay/master/go/go.mod extend/task_relay/master/go/go.sum
git commit -m "feat(task_relay/master): path-dimension policy evaluator + audit operation field"
```

---

### Task 2: filetools 骨架 + view 工具

**Files:**
- Create: `agent/filetools/filetools.go`（Deps + helpers）
- Create: `agent/filetools/view.go`
- Test: `agent/filetools/view_test.go`

- [ ] **Step 1: 写失败测试**

```go
// agent/filetools/view_test.go
package filetools_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/filetools"
	"github.com/infa/task_relay/master/agent/policy"
)

func setup(t *testing.T) (*filetools.Deps, string) {
	t.Helper()
	root := t.TempDir()
	content := "line1\nline2\nline3\nline4\nline5\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte(content), 0o644))
	paths, err := policy.NewPathEvaluator(root, policy.PathRules{DenyList: []string{".env"}})
	require.NoError(t, err)
	audit, err := policy.NewAuditLogger(filepath.Join(t.TempDir(), "audit.jsonl"))
	require.NoError(t, err)
	return &filetools.Deps{
		Paths: paths, Audit: audit,
		MaxReadBytes: 1 << 20, MaxWriteBytes: 1 << 20,
		Session: "test",
	}, root
}

func TestViewWholeFile(t *testing.T) {
	deps, _ := setup(t)
	out, err := filetools.NewViewTool(deps).Run(context.Background(), filetools.ViewInput{Path: "a.txt"})
	require.NoError(t, err)
	require.Contains(t, out.Content, "line1")
	require.Equal(t, 5, out.TotalLines)
	require.False(t, out.Truncated)
}

func TestViewLineRange(t *testing.T) {
	deps, _ := setup(t)
	out, err := filetools.NewViewTool(deps).Run(context.Background(), filetools.ViewInput{Path: "a.txt", Offset: 2, Limit: 2})
	require.NoError(t, err)
	require.NotContains(t, out.Content, "line1")
	require.Contains(t, out.Content, "line2")
	require.Contains(t, out.Content, "line3")
	require.NotContains(t, out.Content, "line4")
}

func TestViewDenied(t *testing.T) {
	deps, _ := setup(t)
	_, err := filetools.NewViewTool(deps).Run(context.Background(), filetools.ViewInput{Path: ".env"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "denied")
}

func TestViewEscapeDenied(t *testing.T) {
	deps, _ := setup(t)
	_, err := filetools.NewViewTool(deps).Run(context.Background(), filetools.ViewInput{Path: "../outside.txt"})
	require.Error(t, err)
}

func TestViewByteTruncation(t *testing.T) {
	deps, root := setup(t)
	big := make([]byte, 4096)
	for i := range big {
		big[i] = 'x'
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.txt"), big, 0o644))
	deps.MaxReadBytes = 1024
	out, err := filetools.NewViewTool(deps).Run(context.Background(), filetools.ViewInput{Path: "big.txt"})
	require.NoError(t, err)
	require.True(t, out.Truncated)
	require.LessOrEqual(t, len(out.Content), 1024+100) // content + truncation marker
}
```

（文件顶部加 `import "context"`）

- [ ] **Step 2: 运行确认失败**

Run: `go test ./agent/filetools/ -v`
Expected: FAIL（包不存在）

- [ ] **Step 3: 实现**

```go
// agent/filetools/filetools.go
package filetools

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/infa/task_relay/master/agent/policy"
)

// Deps wires the file tools' collaborators.
type Deps struct {
	Paths          policy.PathEvaluator
	Audit          *policy.AuditLogger
	MaxReadBytes   int64
	MaxWriteBytes  int64
	Session        string
}

// checkPath evaluates the path and audits denials. Returns nil error when allowed.
func (d *Deps) checkPath(op, path string) error {
	decision := d.Paths.EvaluatePath(path)
	if decision == policy.Allow {
		return nil
	}
	entry := policy.AuditEntry{
		Operation: op,
		JobID:     uuid.NewString(),
		Command:   path,
		Decision:  decision.String(),
		ExitCode:  -1,
		Session:   d.Session,
	}
	if err := d.Audit.Log(entry); err != nil {
		return fmt.Errorf("%s denied and audit failed: %w", op, err)
	}
	return fmt.Errorf("%s denied by policy: %s", op, path)
}

// auditOp records a completed operation. Stdout carries the content summary for hashing.
func (d *Deps) auditOp(op, path string, content string, exitCode int, err error) error {
	entry := policy.AuditEntry{
		Operation: op,
		JobID:     uuid.NewString(),
		Command:   path,
		Backend:   "local",
		Decision:  policy.Allow.String(),
		ExitCode:  exitCode,
		Stdout:    content,
		Session:   d.Session,
	}
	if err != nil {
		entry.Error = err.Error()
	}
	if logErr := d.Audit.Log(entry); logErr != nil {
		return fmt.Errorf("audit failed: %w", logErr)
	}
	return nil
}
```

```go
// agent/filetools/view.go
package filetools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
)

type ViewTool struct {
	deps *Deps
}

func NewViewTool(deps *Deps) *ViewTool { return &ViewTool{deps: deps} }

type ViewInput struct {
	Path   string `json:"path" jsonschema:"required,description=File path, relative to the configured root or absolute"`
	Offset int    `json:"offset,omitempty" jsonschema:"description=1-based starting line; default 1"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=Max lines to return; default all (subject to byte cap)"`
}

type ViewOutput struct {
	Content    string `json:"content"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated"`
}

func (v *ViewTool) Run(ctx context.Context, in ViewInput) (ViewOutput, error) {
	if err := v.deps.checkPath("file_view", in.Path); err != nil {
		return ViewOutput{}, err
	}
	data, err := os.ReadFile(resolveIn(v.deps.Paths.Root(), in.Path))
	if err != nil {
		_ = v.deps.auditOp("file_view", in.Path, "", -1, err)
		return ViewOutput{}, fmt.Errorf("read: %w", err)
	}
	truncated := false
	if int64(len(data)) > v.deps.MaxReadBytes {
		data = data[:v.deps.MaxReadBytes]
		truncated = true
	}
	lines := strings.Split(string(data), "\n")
	total := len(lines)
	if total > 0 && lines[total-1] == "" {
		total--
	}
	start := 0
	if in.Offset > 1 {
		start = in.Offset - 1
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := len(lines)
	if in.Limit > 0 && start+in.Limit < end {
		end = start + in.Limit
	}
	selected := lines[start:end]
	var b strings.Builder
	for i, line := range selected {
		fmt.Fprintf(&b, "%d: %s\n", start+i+1, line)
	}
	if truncated {
		b.WriteString("[truncated: byte cap reached]\n")
	}
	out := ViewOutput{Content: b.String(), TotalLines: total, Truncated: truncated}
	if err := v.deps.auditOp("file_view", in.Path, out.Content, 0, nil); err != nil {
		return ViewOutput{}, err
	}
	return out, nil
}

// resolveIn joins relative paths against root; absolute paths pass through
// (policy already validated them).
func resolveIn(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}
```

（view.go 顶部 import 加 `"path/filepath"`；`bufio` 不需要则去掉）

- [ ] **Step 4: 运行确认通过**

Run: `go test ./agent/filetools/ -v -count=1`
Expected: 5 个测试 PASS

- [ ] **Step 5: Commit**

```bash
git add extend/task_relay/master/go/agent/filetools/
git commit -m "feat(task_relay/master): filetools skeleton + view tool with path policy and audit"
```

---

### Task 3: write 工具

**Files:**
- Create: `agent/filetools/write.go`
- Test: `agent/filetools/write_test.go`

- [ ] **Step 1: 写失败测试**

```go
// agent/filetools/write_test.go
package filetools_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/filetools"
)

func TestWriteNewFile(t *testing.T) {
	deps, root := setup(t)
	out, err := filetools.NewWriteTool(deps).Run(context.Background(), filetools.WriteInput{
		Path: "new/dir/out.txt", Content: "hello",
	})
	require.NoError(t, err)
	require.True(t, out.Created)
	require.Equal(t, 5, out.BytesWritten)
	data, err := os.ReadFile(filepath.Join(root, "new/dir/out.txt"))
	require.NoError(t, err)
	require.Equal(t, "hello", string(data))
}

func TestWriteOverwrite(t *testing.T) {
	deps, root := setup(t)
	out, err := filetools.NewWriteTool(deps).Run(context.Background(), filetools.WriteInput{
		Path: "a.txt", Content: "replaced",
	})
	require.NoError(t, err)
	require.False(t, out.Created)
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	require.Equal(t, "replaced", string(data))
}

func TestWriteDenied(t *testing.T) {
	deps, _ := setup(t)
	_, err := filetools.NewWriteTool(deps).Run(context.Background(), filetools.WriteInput{
		Path: ".env", Content: "x",
	})
	require.Error(t, err)
}

func TestWriteSizeLimit(t *testing.T) {
	deps, _ := setup(t)
	deps.MaxWriteBytes = 4
	_, err := filetools.NewWriteTool(deps).Run(context.Background(), filetools.WriteInput{
		Path: "big.txt", Content: "too-long-content",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "max_write_bytes")
}
```

（复用 Task 2 的 setup helper——同包测试共享。）

- [ ] **Step 2: 运行确认失败**

Run: `go test ./agent/filetools/ -run TestWrite -v`
Expected: FAIL（NewWriteTool 未定义）

- [ ] **Step 3: 实现**

```go
// agent/filetools/write.go
package filetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type WriteTool struct {
	deps *Deps
}

func NewWriteTool(deps *Deps) *WriteTool { return &WriteTool{deps: deps} }

type WriteInput struct {
	Path    string `json:"path" jsonschema:"required,description=Target file path"`
	Content string `json:"content" jsonschema:"required,description=Full file content to write"`
}

type WriteOutput struct {
	BytesWritten int  `json:"bytes_written"`
	Created      bool `json:"created"`
}

func (w *WriteTool) Run(ctx context.Context, in WriteInput) (WriteOutput, error) {
	if err := w.deps.checkPath("file_write", in.Path); err != nil {
		return WriteOutput{}, err
	}
	if int64(len(in.Content)) > w.deps.MaxWriteBytes {
		return WriteOutput{}, fmt.Errorf("content exceeds max_write_bytes (%d)", w.deps.MaxWriteBytes)
	}
	abs := resolveIn(w.deps.Paths.Root(), in.Path)
	_, statErr := os.Stat(abs)
	created := os.IsNotExist(statErr)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		_ = w.deps.auditOp("file_write", in.Path, "", -1, err)
		return WriteOutput{}, fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(abs, []byte(in.Content), 0o644); err != nil {
		_ = w.deps.auditOp("file_write", in.Path, "", -1, err)
		return WriteOutput{}, fmt.Errorf("write: %w", err)
	}
	out := WriteOutput{BytesWritten: len(in.Content), Created: created}
	if err := w.deps.auditOp("file_write", in.Path, in.Content, 0, nil); err != nil {
		return WriteOutput{}, err
	}
	return out, nil
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./agent/filetools/ -v -count=1`
Expected: 全部 PASS

- [ ] **Step 5: Commit**

```bash
git add extend/task_relay/master/go/agent/filetools/
git commit -m "feat(task_relay/master): write tool with size limit and audit"
```

---

### Task 4: edit + multiedit 工具

**Files:**
- Create: `agent/filetools/edit.go`
- Test: `agent/filetools/edit_test.go`

- [ ] **Step 1: 写失败测试**

```go
// agent/filetools/edit_test.go
package filetools_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/filetools"
)

func TestEditUniqueReplace(t *testing.T) {
	deps, root := setup(t)
	out, err := filetools.NewEditTool(deps).Run(context.Background(), filetools.EditInput{
		Path: "a.txt", OldString: "line3", NewString: "LINE3",
	})
	require.NoError(t, err)
	require.Equal(t, 1, out.Replacements)
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	require.Contains(t, string(data), "LINE3")
}

func TestEditNotFound(t *testing.T) {
	deps, _ := setup(t)
	_, err := filetools.NewEditTool(deps).Run(context.Background(), filetools.EditInput{
		Path: "a.txt", OldString: "nonexistent", NewString: "x",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestEditAmbiguous(t *testing.T) {
	deps, root := setup(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "dup.txt"), []byte("foo\nfoo\n"), 0o644))
	_, err := filetools.NewEditTool(deps).Run(context.Background(), filetools.EditInput{
		Path: "dup.txt", OldString: "foo", NewString: "bar",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "multiple")
}

func TestEditReplaceAll(t *testing.T) {
	deps, root := setup(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "dup.txt"), []byte("foo\nfoo\n"), 0o644))
	out, err := filetools.NewEditTool(deps).Run(context.Background(), filetools.EditInput{
		Path: "dup.txt", OldString: "foo", NewString: "bar", ReplaceAll: true,
	})
	require.NoError(t, err)
	require.Equal(t, 2, out.Replacements)
}

func TestMultiEditAtomic(t *testing.T) {
	deps, root := setup(t)
	_, err := filetools.NewMultiEditTool(deps).Run(context.Background(), filetools.MultiEditInput{
		Path: "a.txt",
		Edits: []filetools.EditOp{
			{OldString: "line1", NewString: "L1"},
			{OldString: "nonexistent", NewString: "X"},
		},
	})
	require.Error(t, err)
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	require.Contains(t, string(data), "line1") // rolled back: first edit NOT persisted
}

func TestMultiEditSuccess(t *testing.T) {
	deps, root := setup(t)
	out, err := filetools.NewMultiEditTool(deps).Run(context.Background(), filetools.MultiEditInput{
		Path: "a.txt",
		Edits: []filetools.EditOp{
			{OldString: "line1", NewString: "L1"},
			{OldString: "line5", NewString: "L5"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 2, out.Replacements)
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	require.Contains(t, string(data), "L1")
	require.Contains(t, string(data), "L5")
}

func TestEditDenied(t *testing.T) {
	deps, _ := setup(t)
	_, err := filetools.NewEditTool(deps).Run(context.Background(), filetools.EditInput{
		Path: ".env", OldString: "a", NewString: "b",
	})
	require.Error(t, err)
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./agent/filetools/ -run 'TestEdit|TestMultiEdit' -v`
Expected: FAIL

- [ ] **Step 3: 实现**

```go
// agent/filetools/edit.go
package filetools

import (
	"context"
	"fmt"
	"os"
	"strings"
)

type EditTool struct {
	deps *Deps
}

func NewEditTool(deps *Deps) *EditTool { return &EditTool{deps: deps} }

type EditInput struct {
	Path       string `json:"path" jsonschema:"required,description=File to edit"`
	OldString  string `json:"old_string" jsonschema:"required,description=Exact text to replace; must match uniquely unless replace_all"`
	NewString  string `json:"new_string" jsonschema:"required,description=Replacement text"`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"description=Replace every occurrence"`
}

type EditOutput struct {
	Replacements int `json:"replacements"`
}

type MultiEditTool struct {
	deps *Deps
}

func NewMultiEditTool(deps *Deps) *MultiEditTool { return &MultiEditTool{deps: deps} }

type EditOp struct {
	OldString string `json:"old_string" jsonschema:"required"`
	NewString string `json:"new_string" jsonschema:"required"`
}

type MultiEditInput struct {
	Path  string   `json:"path" jsonschema:"required,description=File to edit"`
	Edits []EditOp `json:"edits" jsonschema:"required,description=Ordered replacements; each old_string must match uniquely. All-or-nothing."`
}

func (e *EditTool) Run(ctx context.Context, in EditInput) (EditOutput, error) {
	if err := e.deps.checkPath("file_edit", in.Path); err != nil {
		return EditOutput{}, err
	}
	abs := resolveIn(e.deps.Paths.Root(), in.Path)
	data, err := os.ReadFile(abs)
	if err != nil {
		_ = e.deps.auditOp("file_edit", in.Path, "", -1, err)
		return EditOutput{}, fmt.Errorf("read: %w", err)
	}
	content, n, err := applyEdit(string(data), in.OldString, in.NewString, in.ReplaceAll)
	if err != nil {
		return EditOutput{}, err
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		_ = e.deps.auditOp("file_edit", in.Path, "", -1, err)
		return EditOutput{}, fmt.Errorf("write: %w", err)
	}
	out := EditOutput{Replacements: n}
	summary := fmt.Sprintf("old=%q new=%q n=%d", in.OldString, in.NewString, n)
	if err := e.deps.auditOp("file_edit", in.Path, summary, 0, nil); err != nil {
		return EditOutput{}, err
	}
	return out, nil
}

func (m *MultiEditTool) Run(ctx context.Context, in MultiEditInput) (EditOutput, error) {
	if err := m.deps.checkPath("file_edit", in.Path); err != nil {
		return EditOutput{}, err
	}
	abs := resolveIn(m.deps.Paths.Root(), in.Path)
	data, err := os.ReadFile(abs)
	if err != nil {
		_ = m.deps.auditOp("file_edit", in.Path, "", -1, err)
		return EditOutput{}, fmt.Errorf("read: %w", err)
	}
	content := string(data)
	total := 0
	for i, op := range in.Edits {
		next, n, err := applyEdit(content, op.OldString, op.NewString, false)
		if err != nil {
			return EditOutput{}, fmt.Errorf("edit %d: %w (no changes written)", i, err)
		}
		content = next
		total += n
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		_ = m.deps.auditOp("file_edit", in.Path, "", -1, err)
		return EditOutput{}, fmt.Errorf("write: %w", err)
	}
	out := EditOutput{Replacements: total}
	summary := fmt.Sprintf("multiedit n=%d ops=%d", total, len(in.Edits))
	if err := m.deps.auditOp("file_edit", in.Path, summary, 0, nil); err != nil {
		return EditOutput{}, err
	}
	return out, nil
}

func applyEdit(content, oldStr, newStr string, replaceAll bool) (string, int, error) {
	if oldStr == "" {
		return "", 0, fmt.Errorf("old_string must not be empty")
	}
	n := strings.Count(content, oldStr)
	if n == 0 {
		return "", 0, fmt.Errorf("old_string not found")
	}
	if n > 1 && !replaceAll {
		return "", 0, fmt.Errorf("old_string matches multiple locations (%d); pass replace_all or a longer unique string", n)
	}
	return strings.ReplaceAll(content, oldStr, newStr), n, nil
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./agent/filetools/ -v -count=1`
Expected: 全部 PASS

- [ ] **Step 5: Commit**

```bash
git add extend/task_relay/master/go/agent/filetools/
git commit -m "feat(task_relay/master): edit and multiedit tools with atomic apply"
```

---

### Task 5: 配置接入 + 注册 + 示例 + 集成测试

**Files:**
- Create: `agent/file_config.go`
- Modify: `agent/mcp_config.go`（MasterFileConfig 加 File 字段）
- Modify: `agent/master_file_merge.go`（合并 file 段）
- Modify: `agent/master.go`（Config 加 File 字段 + New() 注册 + buildFileTools）
- Modify: `cmd/master-demo/master.example.yaml`
- Test: `agent/file_config_test.go` + `agent/filetools_integration_test.go`

- [ ] **Step 1: 写失败测试**

```go
// agent/file_config_test.go
package agent_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent"
)

func TestFileConfigMerge(t *testing.T) {
	file := &agent.MasterFileConfig{
		File: &agent.FileToolsFileConfig{
			Enabled: true,
			Root:    "/srv/work",
			Policy: &agent.FilePolicyFileConfig{
				AllowPaths: []string{"src/**"},
				DenyPaths:  []string{".env"},
			},
		},
	}
	cfg, _, err := agent.MergeFileIntoConfig(agent.Config{}, file)
	require.NoError(t, err)
	require.NotNil(t, cfg.File)
	require.True(t, cfg.File.Enabled)
	require.Equal(t, "/srv/work", cfg.File.Root)
	require.Equal(t, []string{".env"}, cfg.File.Policy.DenyList)
}

func TestFileConfigDefaults(t *testing.T) {
	cfg := agent.FileToolsConfig{}.WithDefaults("/fallback/root")
	require.Equal(t, "/fallback/root", cfg.Root)
	require.Equal(t, int64(1<<20), cfg.MaxReadBytes)
	require.Equal(t, int64(1<<20), cfg.MaxWriteBytes)
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./agent/ -run TestFileConfig -v`
Expected: FAIL

- [ ] **Step 3: 实现**

```go
// agent/file_config.go
package agent

import (
	"github.com/infa/task_relay/master/agent/policy"
)

type FileToolsFileConfig struct {
	Enabled bool                  `json:"enabled" yaml:"enabled"`
	Root    string                `json:"root" yaml:"root"`
	Policy  *FilePolicyFileConfig `json:"policy" yaml:"policy"`
	Limits  *FileLimitsFileConfig `json:"limits" yaml:"limits"`
}

type FilePolicyFileConfig struct {
	AllowPaths []string `json:"allow_paths" yaml:"allow_paths"`
	DenyPaths  []string `json:"deny_paths" yaml:"deny_paths"`
}

type FileLimitsFileConfig struct {
	MaxReadBytes  int64 `json:"max_read_bytes" yaml:"max_read_bytes"`
	MaxWriteBytes int64 `json:"max_write_bytes" yaml:"max_write_bytes"`
}

type FileToolsConfig struct {
	Enabled       bool
	Root          string
	Policy        policy.PathRules
	MaxReadBytes  int64
	MaxWriteBytes int64
}

func (c FileToolsConfig) WithDefaults(fallbackRoot string) FileToolsConfig {
	if c.Root == "" {
		c.Root = fallbackRoot
	}
	if c.MaxReadBytes <= 0 {
		c.MaxReadBytes = 1 << 20
	}
	if c.MaxWriteBytes <= 0 {
		c.MaxWriteBytes = 1 << 20
	}
	return c
}

func fileConfigFromFile(f *FileToolsFileConfig) *FileToolsConfig {
	if f == nil {
		return nil
	}
	cfg := &FileToolsConfig{Enabled: f.Enabled, Root: f.Root}
	if f.Policy != nil {
		cfg.Policy = policy.PathRules{AllowList: f.Policy.AllowPaths, DenyList: f.Policy.DenyPaths}
	}
	if f.Limits != nil {
		cfg.MaxReadBytes = f.Limits.MaxReadBytes
		cfg.MaxWriteBytes = f.Limits.MaxWriteBytes
	}
	return cfg
}
```

`agent/mcp_config.go` MasterFileConfig 加：

```go
	File *FileToolsFileConfig `json:"file" yaml:"file"`
```

`hasMasterContent()` 加 `cfg.File != nil`（与 exec 同例）。

`agent/master_file_merge.go` 在 exec 合并之后加：

```go
	if cfg.File == nil {
		cfg.File = fileConfigFromFile(file.File)
	}
```

`agent/master.go`：
1. Config 加 `File *FileToolsConfig`
2. New() 在 bash_tool 注册之后加：

```go
		if cfg.File != nil && cfg.File.Enabled {
			fileTools, fileErr := buildFileTools(cfg)
			if fileErr != nil {
				if mcpClose != nil {
					_ = mcpClose()
				}
				_ = closeHub(hub)
				_ = shutdownTracing(context.Background())
				return nil, fileErr
			}
			tools = append(tools, fileTools...)
		}
```

3. master.go 末尾加：

```go
func buildFileTools(cfg Config) ([]tool.BaseTool, error) {
	fileCfg := cfg.File.WithDefaults(cfg.WorkingDir)
	paths, err := policy.NewPathEvaluator(fileCfg.Root, fileCfg.Policy)
	if err != nil {
		return nil, fmt.Errorf("file root: %w", err)
	}
	auditPath := filepath.Join(filepath.Dir(fileCfg.Root), ".task-relay", "file-audit.jsonl")
	if cfg.Exec != nil && cfg.Exec.AuditPath != "" {
		auditPath = cfg.Exec.AuditPath // share the exec audit file when configured
	}
	audit, err := policy.NewAuditLogger(auditPath)
	if err != nil {
		return nil, fmt.Errorf("file audit: %w", err)
	}
	deps := &filetools.Deps{
		Paths: paths, Audit: audit,
		MaxReadBytes: fileCfg.MaxReadBytes, MaxWriteBytes: fileCfg.MaxWriteBytes,
		Session: cfg.MasterSession,
	}
	view := filetools.NewViewTool(deps)
	write := filetools.NewWriteTool(deps)
	edit := filetools.NewEditTool(deps)
	multiedit := filetools.NewMultiEditTool(deps)

	viewT, err := toolutils.InferTool("view", "Read a file with line numbers (policy-gated, audited)", view.Run)
	if err != nil {
		return nil, fmt.Errorf("view tool: %w", err)
	}
	writeT, err := toolutils.InferTool("write", "Write a file, creating parent dirs (policy-gated, audited)", write.Run)
	if err != nil {
		return nil, fmt.Errorf("write tool: %w", err)
	}
	editT, err := toolutils.InferTool("edit", "Replace exact text in a file; old_string must match uniquely unless replace_all (policy-gated, audited)", edit.Run)
	if err != nil {
		return nil, fmt.Errorf("edit tool: %w", err)
	}
	multieditT, err := toolutils.InferTool("multiedit", "Apply multiple exact replacements atomically; all-or-nothing (policy-gated, audited)", multiedit.Run)
	if err != nil {
		return nil, fmt.Errorf("multiedit tool: %w", err)
	}
	return []tool.BaseTool{viewT, writeT, editT, multieditT}, nil
}
```

注意：buildFileTools 引用 `cfg.WorkingDir`——**master.go 的 Config 当前没有 WorkingDir 字段**。检查后若没有，用 `os.Getwd()` 作为 fallbackRoot：

```go
	wd, _ := os.Getwd()
	fileCfg := cfg.File.WithDefaults(wd)
```

imports 增加 `"os"`, `"path/filepath"`, `"github.com/infa/task_relay/master/agent/filetools"`（policy/toolutils 已有）。

`cmd/master-demo/master.example.yaml` 在 exec 段后追加：

```yaml
# Controlled file operations (disabled by default).
file:
  enabled: false   # set true to enable view/write/edit/multiedit
  root: ""         # default: master working directory; all ops confined here unless allow_paths grants absolute globs
  policy:
    allow_paths: []                 # relative globs; empty = everything under root allowed (deny_paths still applies)
    deny_paths: [".ssh/**", "**/*.pem", "**/.env", "**/id_rsa*"]
  limits:
    max_read_bytes: 1048576
    max_write_bytes: 1048576
```

集成测试 `agent/filetools_integration_test.go`（package agent 内部测试）：

```go
package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/stretchr/testify/require"
)

func TestMasterFileToolsEndToEnd(t *testing.T) {
	root := t.TempDir()
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	require.NoError(t, os.WriteFile(filepath.Join(root, "hello.txt"), []byte("alpha\nbeta\n"), 0o644))

	yaml := `
exec:
  enabled: false
  audit:
    path: ` + auditPath + `
file:
  enabled: true
  root: ` + root + `
  policy:
    deny_paths: ["**/.env"]
`
	cfgPath := filepath.Join(t.TempDir(), "master.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o644))

	fileCfg, err := LoadMasterConfigFile(cfgPath)
	require.NoError(t, err)
	cfg, _, err := MergeFileIntoConfig(Config{MasterSession: "it"}, fileCfg)
	require.NoError(t, err)
	require.NotNil(t, cfg.File)

	tools, err := buildFileTools(cfg)
	require.NoError(t, err)
	require.Len(t, tools, 4)

	byName := map[string]tool.BaseTool{}
	for _, tl := range tools {
		info, err := tl.Info(context.Background())
		require.NoError(t, err)
		byName[info.Name] = tl
	}
	require.Contains(t, byName, "view")
	require.Contains(t, byName, "write")
	require.Contains(t, byName, "edit")
	require.Contains(t, byName, "multiedit")

	invoke := func(name, args string) string {
		t.Helper()
		res, err := byName[name].(tool.InvokableTool).InvokableRun(context.Background(), args)
		require.NoError(t, err)
		return res
	}

	out := invoke("view", `{"path":"hello.txt"}`)
	require.Contains(t, out, "alpha")

	out = invoke("write", `{"path":"new.txt","content":"gamma"}`)
	require.Contains(t, out, "gamma")

	out = invoke("edit", `{"path":"new.txt","old_string":"gamma","new_string":"delta"}`)
	require.Contains(t, out, "1")

	data, err := os.ReadFile(filepath.Join(root, "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "delta", string(data))

	// denied path rejected at tool level
	_, err = byName["view"].(tool.InvokableTool).InvokableRun(context.Background(), `{"path":".env"}`)
	// tool returns error string in result, not Go error; assert via content
	if err == nil {
		// some tool wrappers encode errors into the result payload
		res, _ := byName["view"].(tool.InvokableTool).InvokableRun(context.Background(), `{"path":".env"}`)
		require.Contains(t, res, "denied")
	}

	// audit captured the ops
	auditData, err := os.ReadFile(auditPath)
	require.NoError(t, err)
	s := string(auditData)
	require.Contains(t, s, `"op":"file_view"`)
	require.Contains(t, s, `"op":"file_write"`)
	require.Contains(t, s, `"op":"file_edit"`)
}
```

注意：上面审计断言假设 exec 段配了 audit.path 时 file tools 共享该文件（buildFileTools 里 `cfg.Exec.AuditPath != ""` 分支）。若 exec 未配，默认路径在 root 旁的 .task-relay/。测试里 exec.enabled=false 但 audit.path 已解析进 cfg.Exec——确认 merge 逻辑：exec.enabled=false 时 cfg.Exec 仍非 nil（execConfigFromFile 不因 enabled=false 返回 nil），审计共享生效。

（测试顶部 `import "encoding/json"` 若未用则去掉）

- [ ] **Step 4: 运行全部测试**

Run: `go test ./... -count=1 && go vet ./...`
Expected: 全部 PASS

- [ ] **Step 5: Commit**

```bash
git add extend/task_relay/master/go/
git commit -m "feat(task_relay/master): file tools config wiring, registration, example, e2e test"
```

---

## Self-Review 记录

- **Spec 覆盖**：第二轮两项——文件四工具（T2/T3/T4）+ 路径维度策略（T1）+ 配置接入（T5）全覆盖。
- **占位符扫描**：无 TBD/TODO；代码完整。
- **类型一致性**：`policy.PathEvaluator`（T1）→ `filetools.Deps.Paths`（T2）→ `buildFileTools`（T5）一致；`policy.PathRules{AllowList, DenyList}` 与 `FilePolicyFileConfig{AllowPaths, DenyPaths}` 映射明确；AuditEntry.Operation（T1）在 filetools（T2-T4）与集成测试（T5）一致。
- **已知设计决策**：①file tools 与 exec 共享审计文件（exec.audit.path 配置时）；②multiedit 原子性=内存中全部应用成功才写回；③view 返回带行号内容（crush view 同款）；④路径 symlink 解析对已存在路径全解析、不存在路径解析父目录。
