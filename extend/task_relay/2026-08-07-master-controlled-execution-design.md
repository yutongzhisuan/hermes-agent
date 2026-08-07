# Master Agent 受控执行能力 — 蓝图 + 首轮设计

- 日期：2026-08-07
- 状态：待评审
- 范围：master agent（`master/go`）能力增强蓝图（P0+P1），首轮实施受控执行器

## 1. 背景与目标

master 现状（`master/go`）：eino 编排 + MCP loader + Tavily/Perplexity 搜索 + gRPC hub 任务调度 + local-planner 子代理。工具面只有 `dispatchTask / dispatchBatch / watchAndJoin / getTaskResult / cancelTask` + web 搜索 + MCP 工具，**没有本地执行能力和文件操作能力**。

目标：把 master 从"纯调度器"升级为"企业级通用 agent"。借鉴 crush（charmbracelet/crush）的能力内核——执行、文件、权限治理、成本架构——但不抄其单机交互模型。master 是 headless gRPC server，所有能力必须以受控、可审计、可治理的方式接入。

### 设计原则

1. **master 是唯一策略决策点**：所有执行/文件/外部访问统一经策略引擎裁决，双后端共用同一套策略与审计。
2. **执行进边界，不进进程**：bash 能力以受控执行单元形式存在（沙箱后端），不在 master 进程内裸跑。
3. **fail-closed**：审计写失败、策略引擎故障、后端不可用，一律拒绝执行，不允许绕过。
4. **YAGNI**：每轮只实现当轮范围；接口为后续演进预留但不超前实现。

## 2. 全蓝图（P0+P1）

| 优先级 | 能力 | 说明 | 状态 |
|---|---|---|---|
| P0 | 受控执行器（bash） | Executor 抽象 + LocalBackend + 策略引擎 + 审计；RemoteBackend 留接口 | **首轮** |
| P0 | 文件操作 | view / write / edit / multiedit + filetracker | 第二轮 |
| P0 | 权限治理 | 策略引擎与执行器共用；文件操作纳入同一策略框架 | 随执行器首轮建立骨架 |
| P1 | fetch / download | URL 抓取转 markdown、文件下载，char_limit 截断 + 落盘 | 第三轮 |
| P1 | todos | 多步任务跟踪，持久化 | 第三轮 |
| P1 | 双模型成本架构 | large/small 分工：摘要、标题、子任务走 small model | 第三轮 |
| P1 | PreToolUse hooks | 工具调用前触发外部脚本（审计/合规扩展点） | 第三轮 |
| P1 | MCP instructions 注入 | MCP server instructions 写入 system prompt | 第三轮 |

明确不借鉴（crush 有但 master 不要）：TUI/UI 全家桶、LSP 工具集（除非定位转为代码 agent）、question 交互工具、agent 本地子代理委托（已有 task_relay 分布式调度）、herdr 集成。

## 3. 首轮范围：受控执行器

本轮交付：Executor 抽象 + LocalBackend（本机沙箱）+ 策略引擎 + 审计 + bash_tool 暴露。RemoteBackend 只定义接口，不实现。

### 3.1 架构与数据流

新增包（`master/go/agent/` 下）：

```
executor/
├── executor.go    # Executor 接口 + Spec/JobResult 类型
└── local.go       # LocalBackend：本机沙箱执行
policy/
├── policy.go      # Evaluate(spec) → Allow|Deny|NeedsApproval
├── rules.go       # YAML 规则加载
└── audit.go       # AuditLogger：JSONL 落盘 + OTel span 属性
tools/bash_tool.go # bash 工具：policy.Evaluate → executor.Run → audit
```

一次 bash 调用的数据流：

```
eino agent → bash_tool
  → policy.Evaluate(spec)
      ├─ Deny          → 立即拒绝，返回拒绝原因
      ├─ NeedsApproval → 首轮映射为 Deny + 审计记录（审批接口预留）
      └─ Allow         → executor.Run(ctx, spec)
                           └─ LocalBackend：本机沙箱进程
  → audit.Log(job)（Allow/Deny 均记录）
  → 返回 JobResult(stdout/stderr/exit/timeout/canceled)
```

### 3.2 核心类型

```go
type Spec struct {
    Command string            // 原始命令行
    WorkDir string            // 默认 master 工作目录
    Timeout time.Duration     // 默认 60s，上限 10m
    Env     map[string]string // env 覆盖，键受白名单约束
    Backend string            // ""=auto | "local"（"remote" 二期）
}

type JobResult struct {
    ExitCode           int
    Stdout, Stderr     string // 截断至 max_output_bytes（默认 1MB）
    TimedOut, Canceled bool
    Backend            string
    StartedAt, FinishedAt time.Time
}

type Executor interface {
    Run(ctx context.Context, spec Spec) (JobResult, error)
    Name() string
}
```

### 3.3 策略引擎

决策三态：`Allow | Deny | NeedsApproval`。NeedsApproval 首轮映射为 Deny + 审计记录，审批服务接口预留（后续接内部审批服务 + 企业 IM 待办）。

裁决顺序：deny_list 命中 → Deny；allow_list 命中 → Allow；approval_list 命中 → NeedsApproval；兜底按 mode（默认 deny_by_default）。

配置（master.yaml 新增 `exec` 段）：

```yaml
exec:
  enabled: true
  default_backend: local
  policy:
    mode: deny_by_default        # deny_by_default | allow_with_deny_list
    allow_list: ["ls", "cat", "grep", "git status", "go test", "make"]
    deny_list: ["rm -rf", "sudo", "curl | sh"]
    approval_list: ["git push", "kubectl"]
    env_allow_keys: ["PATH", "HOME", "GOPATH"]
  limits:
    timeout_default: 60s
    timeout_max: 10m
    max_output_bytes: 1048576
  audit:
    path: ~/.task-relay/exec-audit.jsonl   # 默认路径，可配置；目录不存在时自动创建
```

匹配语义：allow_list 按命令头（首个 token）精确匹配；deny_list 按子串匹配（命令行任意位置命中即拒）。env_allow_keys 之外的 env 键一律剥离。

### 3.4 审计

- JSONL 落盘，字段：`ts, job_id, command, backend, decision, exit_code, duration_ms, stdout_hash(sha256), workdir, session`
- stdout/stderr 不落盘（防敏感泄漏），只记哈希与截断长度，哈希用于事后比对
- 每次调用写 OTel span 属性（复用现有 tracing 基建）：`exec.backend`、`exec.decision`、`exec.exit_code`、`exec.duration_ms`
- 审计写失败 → 拒绝执行（fail-closed）

### 3.5 LocalBackend 沙箱平台策略

- **Linux**：bubblewrap（`bwrap`）隔离——只读挂载根、独立 mount/pid namespace、drop capabilities
- **darwin**：`sandbox-exec` 在 macOS 11+ 已废弃且 `bubblewrap` 不可用，首轮采用**进程级隔离降级**（独立进程组 + 资源限制 + 超时 kill），策略引擎与审计照常生效；文档明确标注"开发环境降级，生产部署以 Linux 容器为准"
- 后端探测顺序：bwrap 可用 → bwrap；否则 → 进程级降级（日志 warn 一条）

### 3.6 RemoteBackend（接口预留，二期）

hub proto（`task_relay_v1.proto`）当前只有 TaskRelay 任务调度 RPC，无 exec RPC。remote 后端落地需要：proto 增加 exec RPC → hub 转发 → Python worker 新增 exec handler。首轮只定义 `Executor` 接口与 `default_backend` 配置项，不实现 remote。

### 3.7 配置接入与工具暴露

- `agent.Config` 新增 `Exec *ExecConfig`；`New()` 构建 policy evaluator + executor + audit logger，注入 bash_tool
- bash_tool 实现 eino `tool.BaseTool`，参数：`command`（必填）、`workdir`、`timeout_seconds`、`backend`
- 注册路径与现有 RelayTools/MCP/search 工具并列
- local-only 模式（无 hub）下 `backend: remote` 请求直接报错
- `exec.enabled: false` 或缺省时完全不注册 bash_tool（零足迹）

## 4. 测试与验证

- **policy 单测**：三态裁决、优先级顺序、env 键过滤、命令头精确匹配 vs 子串匹配
- **executor 单测**：mock 进程验证超时/截断/退出码传播；LocalBackend 用 `echo`/`false`/`sleep` 真实冒烟（Linux CI 跑 bubblewrap 路径，mac 跑降级路径）
- **audit 单测**：JSONL 字段完整性、fail-closed 行为
- **集成测试**：bash_tool 端到端（mock model → tool → executor → 断言审计文件）
- 运行方式：`cd master/go && go test ./...`

## 5. 非目标（本轮不做）

- 后台 job（job_output / job_kill）
- RemoteBackend 实现（依赖 hub proto 变更）
- 审批服务对接（NeedsApproval 仅留接口语义）
- 文件操作工具、fetch/download、todos、双模型、hooks、MCP instructions（蓝图第二轮及以后）
- TUI / 交互式审批

## 6. 后续演进路径

1. **第二轮**：文件操作（view/write/edit/multiedit）接入同一策略框架；策略引擎增加路径维度（workdir 白名单/黑名单）
2. **第三轮**：P1 各项；RemoteBackend 落地（proto + hub + worker exec handler）；审批服务对接
3. **更远**：Docker/K8s Job 后端作为 Executor 的第三种实现
