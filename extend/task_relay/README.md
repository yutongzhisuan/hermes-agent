# extend/task_relay — ACP 执行端（Worker sidecar）

XHermes 侧的 Task Relay 执行端：`acp_rpc_server.py` 作为节点本地 sidecar
（默认 127.0.0.1:9105），把 relay 任务跑在进程内 ACP session 上
（`acp_backend.py` + `acp_adapter.session`）。

## 安全模型（M2-W4：executor profile）

平台开放后，外部租户（planner）下发的 goal 会在算力节点上被执行。执行端若带
shell/browser 工具等于远程代码执行，因此 **Worker executor profile 默认无
shell/browser**（与 planner 最小权限同原则），白名单双层强制：

- **第 1 层 — executor profile**（`executor_profile.py`）：任务请求的
  toolsets 在 ACP session 创建前与节点运营者白名单求交
  （`AcpTaskBackend.run` → `ExecutorProfile.resolve`）。这是工具
  **注册/过滤层**的强制（最终落到 `AIAgent(enabled_toolsets=...)`），
  不是提示词层面的约束。默认白名单：

  | toolset | 内容 |
  |---------|------|
  | `file`  | read_file / write_file / patch / search_files |
  | `web`   | web_search / web_extract |
  | `todo`  | 任务规划 |

  默认显式排除 shell/系统控制类：`terminal`、`code_execution`、
  `browser`、`computer_use`、`delegation`、`homeassistant`、`kanban`
  （`SHELL_CLASS_TOOLSETS`）。请求全部被拒时 session 得到**空的**
  toolsets 列表（无工具），不会回退到更宽的默认值。

- **第 2 层 — stateless 隔离**（`stateless.py`）：`--stateless` 下
  `BLOCKED_STATELESS_TOOLSETS`（memory / skills / session_search /
  cronjob / clarify / messaging / project）**永远**被丢弃——即使运营者
  在第 1 层白名单里加了它们。这层保护节点本地用户数据。

每个 session 生效的白名单会打进日志
（`task t1 executor toolsets: [...] (requested=..., whitelist=...)`）。

### 放开白名单（运营者自主选择信任级别）

sidecar 启动参数（或等价 env）：

```bash
python -m extend.task_relay.acp_rpc_server --stateless \
    --executor-allow-extra terminal,code_execution   # 追加到默认白名单
    # 或整体替换： --executor-toolsets file,web,todo,terminal
# env: ACP_EXECUTOR_TOOLSETS / ACP_EXECUTOR_ALLOW_EXTRA
```

**风险**：放开 `terminal` / `code_execution` / `browser` / `delegation`
即把对应能力交给远程租户，等同于远程代码执行。无 Docker 的节点请只用于
可信内部任务；对外节点必须配合 `--sandbox docker`（每任务一次性容器、
默认无网络），沙箱才是安全边界。构建 profile 时若白名单含 shell 类
toolset，启动日志会打 warning。

### toolsets 上报对齐（宣称的 == 能用的）

Worker 向上（`task-relay-worker --toolsets` → daemon announce）声明的
toolsets 必须与 sidecar 实际白名单一致。sidecar 是事实来源：

```bash
python -m extend.task_relay.executor_profile   # 打印喂给 --toolsets 的 CSV
```

或运行时查 RPC `acp.toolsets`（返回 `{"toolsets": [...]}`）。
`ExecutorProfile.validate_announce(announced)` 可校验已声明清单是否是
白名单子集（返回越权项列表，空 = 一致）。

## 运行模式速查

| 场景 | 启动方式 |
|------|----------|
| 不可信远程任务（默认推荐） | `--stateless`（+ 默认 executor profile） |
| 不可信任务 + 需要 shell | `--stateless --sandbox docker --executor-allow-extra terminal` |
| 可信内部任务（无 Docker） | `--local-confined`（审批 deny 护栏，非安全边界） |

stateful（无 `--stateless`）路径面向本地可信使用，不应用 executor
profile。

## 按任务模型绑定（S4：本地 Runtime 优先）

TaskSpec 的模型绑定（`model` 字段 / `params["model"]`）经 Hub
`{"run":...}` payload → worker `acp-remote` → `acp.run` 的 `model` 参数
到达 sidecar。绑定模型的任务**必须**跑在节点本地 OpenAI 兼容 Runtime
（`local_runtime.py`）：

- 先过运营者白名单 `ACP_ALLOWED_MODELS`（逗号分隔；未设置 = 不做静态
  白名单，由探测兜底），再探测本地 Runtime 的 `GET /models`；模型不在
  服务清单内、或本地 Runtime 不可达 → **fail-fast**，任务以
  `failed` + `error_code="model_unavailable"` 上行，Hub 据此换候选
  （Hub 侧识别逻辑属 S2）。不等待、不静默换模型。
- 探测命中后用显式 `provider="custom"` / `api_mode="chat_completions"` /
  `base_url` / `api_key` 构建 session（`model_sessions.py`），绕开
  云端凭证解析——绑定任务永远不会落到云平台。
- 平台全链路回退本期不做（节点上放租户凭证的安全模型未定，spec §13.4
  S4）。

配置（env）：

| env | 默认 | 含义 |
|-----|------|------|
| `ACP_LOCAL_RUNTIME_BASE_URL` | `http://127.0.0.1:8080/v1` | 本地 Runtime OpenAI 兼容地址 |
| `ACP_LOCAL_RUNTIME_API_KEY` | `no-key-required` | 本地 Runtime bearer（多数本地服务忽略） |
| `ACP_ALLOWED_MODELS` | 未设置 | 运营者模型白名单（逗号分隔） |

未携带 `model` 的任务行为与 S4 之前完全一致（不探测、不覆盖，用 sidecar
默认 model/provider）。
