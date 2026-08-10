# Master 第三轮（P1 能力 + RemoteBackend + 审批接口）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** 落地蓝图第三轮：fetch/download 工具（SSRF 防护 + 域名策略）、todos、双模型成本架构、PreToolUse hooks、MCP instructions 注入、RemoteBackend（worker shell 执行后端 + master 远程执行器）、审批服务接口。

**Architecture:** 全部复用前两轮的三段组合（工具 → policy/审计 → 既有基建）。RemoteBackend 零 proto/hub 改动——走既有 `DispatchTask`（`params["cmd"]` + `toolsets: ["shell"]` 路由）+ `WatchTask` 等终态；Python worker 新增 `ShellExecBackend`（实现 `TaskBackend` 协议）。

**Tech Stack:** Go 1.26（master）、Python 3.11+ asyncio（worker）、eino toolutils、既有 `agent/policy` 审计、`master/go/client` gRPC SDK。

**Spec:** `extend/task_relay/2026-08-07-master-controlled-execution-design.md`（蓝图 §6 第三轮）

**侦察结论（2026-08-07 explorer）：**
- Hub↔Worker = 单条 WebSocket JSON-RPC；DispatchTask 入库，worker pull/claim 执行；TaskSpec.params 是自由 map，toolsets 做路由筛选——**无需改 proto/hub**
- Worker backend 是进程级 CLI 选择（`--backend {stub,acp,remote-acp}`，`worker/__main__.py` `_create_backend`）；新增 shell-exec backend 实现 `TaskBackend.run(run, on_progress, on_checkpoint, cancel_event) -> TaskCompletePayload`
- master client 每个 RPC 包装是同一模板（observeRPC + 可选 AttachTraceToSpec）
- 双模型接入点：`agent/subagents.go:NewLocalPlannerSubAgent(ctx, chatModel)`——构建第二个 small chatModel 传入即可

**运行测试：** `cd extend/task_relay/master/go && go test ./...`；worker: `cd extend/task_relay/worker && python -m pytest -q`（若有既有测试结构则跟随）

**代码注释与 commit message 一律英文。**

---

## 配置总览（master.yaml 新增段）

```yaml
fetch:
  enabled: false
  policy:
    domain_allow_list: []            # suffix match; empty = all allowed (deny still applies)
    domain_deny_list: []
    allow_private_networks: false    # SSRF guard: reject RFC1918/loopback/link-local/ULA/CGNAT
  limits:
    max_bytes: 1048576
    timeout_seconds: 30

todos:
  enabled: false
  path: ""                           # default ~/.task-relay/todos-<session>.json

hooks:
  pre_tool_use:
    - command: /usr/local/bin/audit-hook
      timeout_seconds: 5

exec:
  approval:
    webhook_url: ""                  # empty = approval requests denied (fail-closed)
    timeout_seconds: 120

openai:
  small_model: ""                    # e.g. qwen-turbo; empty = planner reuses main model
```

Note: this doc was updated post-implementation — approval ships nested under
`exec.approval` (see `agent/exec_config.go`), not as a top-level section.

---

### Task 1: fetch 工具（SSRF 防护 + 域名策略 + 审计）

**Files:**
- Create: `agent/webtools/fetch.go`（含 URL 安全校验 + 文本提取）
- Create: `agent/webtools/webtools.go`（Deps + 共享 helpers）
- Test: `agent/webtools/fetch_test.go`

**设计要点：**
- URL 校验：`url.Parse` → scheme 仅 http/https → 域名策略（deny suffix → allow suffix 空则全放）→ 若 `!allow_private_networks`：自定义 `http.Transport.DialContext`，dial 时解析 IP 并拒绝私网（防 DNS rebinding——校验发生在连接建立时而非请求前）
- 私网判定：`netip.Addr` — `IsLoopback/IsPrivate/IsLinkLocalUnicast/IsLinkLocalMulticast/IsUnspecified` + CGNAT 100.64.0.0/10 + ULA fc00::/7（IsPrivate 已含 ULA？Go 的 IsPrivate 只含 RFC1918 + ULA——显式补 CGNAT 与 link-local）
- 响应处理：`Content-Type` 含 `text/html` → 去标签转文本（自实现简易 HTML→text：用 `golang.org/x/net/html` 解析器遍历文本节点——先检查 go.sum 是否已有 x/net；有则直接用，没有则用 `strings`+正则的保守 strip，不引入新依赖）；其他 `text/*` 与 `application/json` 直接返回；二进制类型拒绝
- 截断：`max_bytes`，响应体用 `http.MaxBytesReader` 或 io.LimitReader
- 重定向：`http.Client.CheckRedirect` 每跳重新校验 URL 策略（防 302 跳内网）
- 审计：Operation `web_fetch`，Command=URL，content 哈希落审计
- 工具描述不提其他工具（跨工具引用禁令同 hermes）

**Input/Output：**

```go
type FetchInput struct {
	URL string `json:"url" jsonschema:"required,description=HTTP(S) URL to fetch"`
}

type FetchOutput struct {
	URL        string `json:"url"`         // final URL after redirects
	StatusCode int    `json:"status_code"`
	Content    string `json:"content"`
	Truncated  bool   `json:"truncated"`
}
```

**测试（httptest.Server 是本机回环——测试用 `allow_private_networks: true` 的 Deps 构造，另加纯函数测试私网判定）：**
- TestFetchTextPage（httptest 返回 text/plain）
- TestFetchHTMLStripped（HTML → 文本节点拼接）
- TestFetchTruncation
- TestFetchDomainDenied / TestFetchDomainAllowList
- TestFetchPrivateIPRejected（纯函数：`isPrivateIP("10.0.0.1")`=true, `"8.8.8.8"`=false, `"100.64.1.1"`=true, `"127.0.0.1"`=true, `"::1"`=true, `"fe80::1"`=true）
- TestFetchRedirectToPrivateBlocked（httptest 302 → http://169.254.169.254/，应被拒绝——用 allow_private_networks=false + 单测 URL 校验函数，避免真实 dial）

**Steps:** TDD → `go test ./agent/webtools/ -v` → commit
`feat(task_relay/master): fetch tool with SSRF guard and domain policy`

---

### Task 2: download 工具

**Files:**
- Create: `agent/webtools/download.go`
- Test: `agent/webtools/download_test.go`

**设计要点：**
- Input：`url`（必填）+ `path`（必填，目标文件——复用 `policy.PathEvaluator` 做路径裁决，写 root 内）
- URL 校验完全复用 Task 1 的 helpers（`validateURL` / safe transport）
- 流式写盘：`io.Copy` 到 tmp 文件 + `io.LimitReader(max_bytes+1)` 超限报错删 tmp → 原子 rename
- 与 filetools.write 的区别：内容来自远程；写前同样 `checkPath("file_write")` 语义——但审计 Operation 用 `web_download`
- Output：`{path, bytes_written, status_code, truncated}`

**测试：** httptest 二进制 body → 下载到 root 内文件，校验内容；deny path（`.env`）拒绝；超限报错且无残留文件。

**Steps:** TDD → commit `feat(task_relay/master): download tool with path policy and atomic write`

---

### Task 3: todos 工具

**Files:**
- Create: `agent/todostool/todos.go`
- Test: `agent/todostool/todos_test.go`

**设计要点：**
- 单工具 `todos`，set 语义（全量替换，crush 同款），返回当前全量列表
- 持久化：JSON 文件（默认 `~/.task-relay/todos-<session>.json`，可配）；每次 set 后原子写（tmp+rename）
- 无 policy 裁决（内部状态文件，路径由配置控制不暴露给模型）

```go
type TodoItem struct {
	ID      string `json:"id" jsonschema:"description=Stable item id"`
	Content string `json:"content" jsonschema:"description=Task description"`
	Status  string `json:"status" jsonschema:"description=pending|in_progress|completed"`
}

type TodosInput struct {
	Items []TodoItem `json:"items" jsonschema:"required,description=Full replacement todo list"`
}

type TodosOutput struct {
	Items []TodoItem `json:"items"`
}
```

- Status 非法值 → error；ID 空 → 自动生成（uuid 短码）
- 加载：文件不存在 → 空列表（非错误）

**测试：** set → 读文件验证持久化；非法 status 报错；重开 tool（同 path）读到上次状态。

**Steps:** TDD → commit `feat(task_relay/master): todos tool with JSON persistence`

---

### Task 4: 双模型成本架构（small model）

**Files:**
- Modify: `agent/mcp_config.go`（OpenAIFileConfig + SmallModel）
- Modify: `agent/master.go`（构建 smallChatModel，NewSubAgents 用它）
- Test: `agent/master_config_test.go` 增补（或新文件）

**设计要点：**
- `OpenAIFileConfig` 加 `SmallModel string \`json:"small_model" yaml:"small_model"\``
- `OpenAIConfig`（runtime）加 `SmallModel string`；merge 映射
- master.go New()：`cfg.OpenAI.SmallModel != ""` 时用同一 base_url/api_key 构建 `smallChatModel`（第二个 `openai.NewChatModel`），传入 `NewSubAgents`；为空则传主 chatModel（现状保持）
- NewSubAgents 签名改为 `NewSubAgents(ctx, cfg, plannerModel model.BaseModel[*schema.Message])`——调用点更新

**测试：** 配置合并映射 small_model；SmallModel 为空时 planner 用主模型（通过构造注入验证——master.go 有 ChatModel 注入测试先例，跟随）。

**Steps:** TDD → commit `feat(task_relay/master): dual-model cost architecture (small model for planner)`

---

### Task 5: PreToolUse hooks

**Files:**
- Create: `agent/hooks/hooks.go`（Hook 配置 + Runner + HookedTool wrapper）
- Test: `agent/hooks/hooks_test.go`

**设计要点：**
- 配置：`hooks.pre_tool_use: [{command, timeout_seconds}]`
- Runner：`exec.CommandContext` 执行 hook 命令，stdin 喂 JSON `{"tool": name, "args": rawArgs}`；退出码 0=放行，非 0=阻止（stdout 文本为原因）；超时=阻止（fail-closed）
- HookedTool 实现 `tool.BaseTool`：包装 InvokableTool 的 `InvokableRun`——先跑 hooks，全过才调内层
- 包装点：master.go New() 在 `ensureUniqueToolNames` 之后统一包装所有工具（若 hooks 配置非空）
- hook 自身失败（命令不存在等）= 阻止（fail-closed）+ 审计（Operation `hook_block`）

**测试：** 假 hook 脚本（`#!/bin/sh\nexit 0` / `exit 1`）写 t.TempDir；放行/阻止/超时三态；包装后工具 Info() 不变。

**Steps:** TDD → commit `feat(task_relay/master): pre-tool-use hooks with fail-closed wrapper`

---

### Task 6: MCP instructions 注入 system prompt

**Files:**
- Modify: `agent/mcp_loader.go`（收集各 server 的 instructions）
- Modify: `agent/master.go`（Instruction 拼接）
- Test: `agent/mcp_loader_test.go` 增补

**设计要点：**
- MCP initialize 响应含 `instructions` 字段（officialmcp SDK）——loadMCPTools 返回时一并带出（改返回签名或加 out 参数）
- master.go New()：非空 instructions 汇总追加到 Instruction 尾部，格式 `\n\n## MCP Server Instructions\n### <server-name>\n<instructions>`
- 无 instructions → 零变化（byte-stable system prompt 原则）

**Steps:** TDD → commit `feat(task_relay/master): inject MCP server instructions into system prompt`

---

### Task 7: worker ShellExecBackend（Python）

**Files:**
- Create: `extend/task_relay/worker/backends/shell_exec_backend.py`
- Modify: `extend/task_relay/worker/__main__.py`（--backend choices + _create_backend）
- Test: `extend/task_relay/worker/tests/test_shell_exec_backend.py`（跟随既有测试结构；若无 tests 目录则新建）

**设计要点：**
```python
class ShellExecBackend:
    """Executes a shell command from task params. Policy/audit live on the master side;
    the worker enforces hard limits only (timeout ceiling, output caps)."""

    MAX_TIMEOUT = 600
    MAX_OUTPUT = 1 << 20

    async def run(self, run, on_progress, on_checkpoint, cancel_event):
        cmd = run.params.get("cmd", "")
        if not cmd:
            return TaskCompletePayload(status="failed", error="params.cmd required", ...)
        workdir = run.params.get("workdir") or None
        timeout = min(int(run.params.get("timeout_seconds", 60)), self.MAX_TIMEOUT)
        proc = await asyncio.create_subprocess_shell(
            cmd, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE,
            cwd=workdir, start_new_session=True,
        )
        # cancel_event watcher task: proc.kill() (process group)
        try:
            stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout)
        except asyncio.TimeoutError:
            kill process group; timed_out = True
        # build extensions payload
        payload = json.dumps({"exit_code": proc.returncode or -1,
                              "stdout": stdout[-MAX_OUTPUT:], "stderr": stderr[-MAX_OUTPUT:],
                              "timed_out": timed_out, "canceled": canceled})
        return TaskCompletePayload(status="succeeded" if not timed_out and not canceled else "failed",
                                   summary=..., result_text=stdout tail,
                                   fields=TaskFields(extensions={"exec": payload.encode()}), ...)
```
- 以 `__main__.py` 既有 backend 注册方式加 `shell-exec` choice
- worker 声明 toolsets 时加 `"shell"`（看 announce 的 capabilities 机制——若 toolsets 由 CLI flag 传入则文档说明启动方式）

**测试：** asyncio 测试——echo 成功、非零退出、超时 kill、cancel。

**Steps:** TDD → commit `feat(task_relay/worker): shell-exec backend for remote command execution`

---

### Task 8: master RemoteBackend

**Files:**
- Create: `agent/executor/remote.go`
- Test: `agent/executor/remote_test.go`（fake client 接口注入）

**设计要点：**
- 为避免 import 环：`client.Client` 的具体类型在 executor 包里用窄接口抽象：

```go
// TaskDispatcher is the slice of the master hub client that RemoteBackend needs.
type TaskDispatcher interface {
	DispatchTask(ctx context.Context, spec *pb.TaskSpec, masterSessionID string, allowRedispatch bool) (*pb.DispatchTaskResponse, error)
	GetTaskResult(ctx context.Context, taskID string) (*pb.TaskResult, error)
}
```

（签名以 `client/client.go` 实际为准；pb 包路径以 master/go 现有 import 为准——查 client.go 的 pb import 路径）

- Run() 流程：构造 TaskSpec{TaskId: uuid, Goal: "remote shell exec", Params: {cmd, workdir, timeout_seconds}, Toolsets: ["shell"], TimeoutSeconds: spec.Timeout} → DispatchTask → poll GetTaskResult（500ms 间隔 + ctx deadline）→ terminal 状态解析 `fields.extensions["exec"]` JSON → JobResult{Backend: "remote"}
- 轮询比 WatchTask 简单且够用（首轮）；ctx cancel → best-effort CancelTask？（首轮不做，注释说明任务会在 worker 侧超时自然终止）
- `Name()` = "remote"，`Sandboxed()` = false（远程 worker 沙箱由 worker 环境决定，master 不可见——注释说明）

**测试：** fake TaskDispatcher（内存状态机：dispatch → pending → complete with extensions JSON）验证 Run 解析；dispatch 失败传播；ctx 超时返回 TimedOut。

**Steps:** TDD → commit `feat(task_relay/master): remote executor backend over hub dispatch`

---

### Task 9: bash_tool backend 接线（local|remote）

**Files:**
- Modify: `agent/master.go`（buildBashTool 支持 remote）
- Modify: `agent/bash_tool.go`（BashInput 加 backend 可选字段）
- Test: 增补 bash_tool_test.go

**设计要点：**
- buildBashTool：`DefaultBackend == "remote"` → 构建 RemoteBackend（需要 hub client——New() 里 hub 变量在 localOnly 时为空，remote 仅在 hub 模式可用；local-only + remote 配置 → 明确报错）
- BashInput 加 `Backend string \`json:"backend,omitempty"\``——`"remote"` 时走 remote executor（若已配置），否则默认 default_backend；请求 remote 但不可用 → 报错"remote backend unavailable"
- BashToolDeps 加 `Remote executor.Executor`（可空）；Run 里按 input/backend 选择

**测试：** 默认 local；显式 backend=local；backend=remote 无配置 → error。

**Steps:** TDD → commit `feat(task_relay/master): bash tool backend selection (local|remote)`

---

### Task 10: 审批服务接口 + webhook 实现 + NeedsApproval 接线

**Files:**
- Create: `agent/policy/approval.go`（ApprovalService 接口 + webhook 实现）
- Modify: `agent/exec_config.go`（approval 配置段）
- Modify: `agent/bash_tool.go`（NeedsApproval 分支调 ApprovalService）
- Modify: `agent/master.go`（buildBashTool 组装）
- Modify: `cmd/master-demo/master.example.yaml`（approval 段）
- Test: `agent/policy/approval_test.go` + bash_tool 增补

**设计要点：**

```go
// ApprovalRequest describes one gated action awaiting a human decision.
type ApprovalRequest struct {
	JobID   string
	Command string
	Session string
}

// ApprovalService asks an external authority whether to proceed.
type ApprovalService interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (bool, error)
}

// WebhookApproval POSTs the request and expects {"approved": bool}.
// Any transport error, non-2xx, or timeout is a denial (fail-closed).
type WebhookApproval struct {
	URL     string
	Timeout time.Duration
	Client  *http.Client // optional
}
```

- bash_tool NeedsApproval 分支：Deps 加 `Approval policy.ApprovalService`（可空）——nil → 维持现状 deny；非 nil → 调用，批准则继续执行（审计 decision 记 `approved`），拒绝/错误 → deny（审计记 `needs_approval` + error）
- 配置：`approval: {webhook_url, timeout_seconds}`；exec_config 加映射；buildBashTool 组装
- webhook 响应格式：`{"approved": true}`；其他一律 deny

**测试：** httptest webhook——批准路径执行、拒绝路径 deny、超时 deny、无配置 deny（现状）；审计记录区分 approved/denied。

**Steps:** TDD → commit `feat(task_relay/master): approval service interface + webhook + needs-approval wiring`

---

### Task 11: 示例配置收尾 + 全量集成验证

**Files:**
- Modify: `cmd/master-demo/master.example.yaml`（fetch/todos/hooks/approval/small_model 示例段）
- Test: `agent/master_integration_test.go` 增补（fetch+todos 注册冒烟）

**内容：**
- 示例配置追加全部新段（全部 enabled: false / 空默认）
- 集成测试：配置全开（fetch/todos 启用）→ New() 注入 ChatModel 路径 → 工具表包含 fetch/download/todos/view/write/edit/multiedit/bash
- `go test ./... -count=1` 全绿

**Steps:** → commit `feat(task_relay/master): round-3 example config + registration smoke test`

---

## Self-Review 记录

- **蓝图第三轮覆盖**：fetch ✓(T1) download ✓(T2) todos ✓(T3) 双模型 ✓(T4) hooks ✓(T5) MCP instructions ✓(T6) RemoteBackend ✓(T7+T8+T9) 审批接口 ✓(T10)
- **proto/hub 零改动**：利用 TaskSpec.params/toolsets 既有扩展点（explorer 侦察确认）
- **依赖新增**：仅 x/net（fetch HTML 提取，若已在 go.sum 则零新增）——其他全部既有依赖
- **fail-closed 贯穿**：SSRF 私网默认拒绝、hook 失败阻止、approval 错误拒绝、remote 不可用在 local-only 明确报错
- **已知取舍**：①RemoteBackend 用轮询而非 WatchTask（首轮简单可靠）；②cancel 不透传到 remote（worker 侧超时自然终止）；③worker shell 后端的安全策略在 master 侧（worker 只做硬限制）；④todos 用 JSON 而非 SQLite（零新依赖）
