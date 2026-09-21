# Headless Agent Wheel（供第三方桌面端）设计文档

- **日期**: 2026-08-05
- **状态**: 已评审（brainstorming §1–§4 用户确认）
- **基准**: xhermes-agent v0.19.x 工作区
- **关联**:
  - `website/docs/developer-guide/desktop-gateway-protocol.md`（WS JSON-RPC 协议）
  - `website/docs/developer-guide/programmatic-integration.md`（集成面对比）
  - `docs/superpowers/specs/2026-08-03-cli-tui-prune-design.md`（部分冲突，见 §8）
  - `docs/superpowers/specs/2026-08-03-pkg-wrap-installsh-design.md`（正交：macOS pkg）

---

## 1. 背景与动机

第三方桌面端需要运行与 XHermes Desktop 同族的 agent 能力，但不需要本仓库的 TUI / Electron / Dashboard 前端。现有发行物 `xhermes-agent` 已是可安装 Python 包，且 headless 路径 `xhermes serve` + `/api/ws` JSON-RPC 已是 Desktop 的正式协议面。

本设计将发行形态收敛为：**单一全量后端 wheel + 薄 Subprocess-first SDK**，去掉前端资产，保留后端能力与协议兼容性。

---

## 2. 目标与非目标

### 2.1 目标

1. 发布可 `pip install` 的 **单一 Python wheel**，供第三方桌面端集成。
2. 桌面端通过 SDK **spawn headless `serve`**，经 **WebSocket JSON-RPC** 驱动 agent（与现 XHermes Desktop 同协议族）。
3. **无任何前端打包**：不包含 TUI（`ui-tui`）、Electron Desktop（`apps/`）、Dashboard SPA（`web/`）、文档站（`website/`）。
4. **后端能力尽量全**：`AIAgent`、核心工具（terminal/file/web 等）、session/记忆/skills、消息平台网关、ACP、cron（含守护）、MCP client、browser、vision、TTS 等。
5. 提供薄 Python API：`start` / `wait_ready` / `ws_url` / `stop`（只管生命周期，不跑 agent 循环）。

### 2.2 非目标

- 不在宿主进程内跑 `AIAgent` / 工具循环（避免 GIL 与崩溃耦合）。
- 不靠 pip extras 拆依赖减肥（已选单包全量）；体积优化 = **剔除前端与构建产物**。
- 不重新发明协议；不把 Desktop React / Ink 客户端打进 wheel。
- 不把「删除 `tui_gateway`」作为本方案的裁剪目标（JSON-RPC 网关后端必须保留）。
- 第一版 SDK **不**封装 `prompt.submit` / 自动重连（正式契约是线上方法与事件名）。

### 2.3 已确认决策摘要

| 决策项 | 选择 |
|--------|------|
| 调用方式 | 子进程网关 + WS JSON-RPC（方案 B） |
| 能力范围 | 后端全能力；不要前端页面（自定义 D） |
| 依赖策略 | 单包全量（C） |
| 入口 | serve + 可编程生命周期 API（C） |
| 架构 | Subprocess-first SDK（方案 1） |

---

## 3. 总体架构

```text
┌─────────────────────────────────────────┐
│  Third-party Desktop (任意 UI 栈)         │
│    HermesRuntime.start(...)               │
│         │ spawn                            │
│         ▼                                  │
│    xhermes serve --host 127.0.0.1 --port 0 │
│         │ XHERMES_BACKEND_READY port=<n>     │
│         ▼                                  │
│    JsonRpc client ──WS──► /api/ws          │
│         prompt.submit / tool.* events …    │
└─────────────────────────────────────────┘
              │
              ▼
     wheel: xhermes-agent (headless)
     agent + tools + gateway + tui_gateway
     + cron + acp + platforms + …
     ✗ ui-tui / apps / web / website
```

| 层 | 内容 |
|----|------|
| Runtime SDK | 新建 `hermes_runtime/`：spawn、ready 探测、连接信息、stop |
| Serve 入口 | 现有 `xhermes serve`（`headless_backend=True`） |
| 协议面 | `web_server.py`（HTTP/WS 路由层，`@app.websocket("/api/ws")`）+ `tui_gateway.ws`（JSON-RPC dispatch 层，`handle_ws`）分层协作；两者都必需，**tui_gateway 不能删除**（见 §8） |
| Agent 核心 | `run_agent` / `agent/` / `tools/` / session / memory / skills |
| 重能力 | 消息网关、ACP、cron、browser、vision、TTS — 打进同一 wheel |

**原则**：RPC = 控制面；event stream = 数据面；UI 只是事件缓存，不是 agent 真源。

---

## 4. 包边界与裁剪清单

### 4.1 Wheel 包含（后端）

| 区域 | 说明 |
|------|------|
| `agent/`、`run_agent.py`、`model_tools.py`、`toolsets.py` | Agent 循环与工具编排 |
| `tools/`（含 environments） | terminal / file / web / browser / vision / TTS 等 |
| `hermes_cli/` | CLI + `serve`；去掉仅服务前端的入口（见 4.3） |
| `tui_gateway/` | JSON-RPC dispatch 层；`/api/ws` 路由在 web_server.py，内部 `from tui_gateway.ws import handle_ws` 转发——**不能删除**（见 §8 与 prune 冲突） |
| `gateway/` + `plugins/platforms/` | 消息平台网关 |
| `acp_adapter/` | ACP |
| `cron/` | 定时任务与守护 |
| `plugins/`（memory / model-providers / …） | 记忆、模型后端等 |
| `skills/`、`optional-skills/`、`locales/` | 技能与文案 |
| 顶层 `hermes_*.py`、`batch_runner.py` 等运行时模块 | 必需 |

依赖：把 headless 运行所需、现 extras 中会被后端用到的依赖 **并入** `[project].dependencies`。实现阶段维护「extras → 主依赖」迁移表，避免漏 ACP / 平台 SDK / browser / TTS 等。

### 4.2 Wheel 排除（前端 / 宿主）

| 路径 | 原因 |
|------|------|
| `apps/` | Electron Desktop + shared |
| `ui-tui/` | Ink TUI |
| `web/` | Dashboard SPA |
| `website/` | 文档站 |
| `tests-js/` | 前端测试 |
| `web_dist/`、`node_modules`、前端构建产物 | 不得进入 bdist_wheel |

`setuptools.packages.find` / `package-data` 使用 **后端白名单**。sdist 可含更广源码树；**bdist_wheel 不装前端目录**。

### 4.3 `hermes_cli` 调整

| 动作 | 项 |
|------|-----|
| 保留 | `serve`、gateway 启停、config/setup、cron、tools、skills、profiles、ACP 子命令 |
| 移除或明确报错 | `desktop` / `gui` / `--tui`（本发行版不含 UI） |
| 保留模块、headless 使用 | `web_server.py` 供 `serve` 挂 `/api/ws`；`XHERMES_SERVE_HEADLESS=1` 不挂 SPA |
| 可删（确认无其它引用后） | 仅 SPA 构建辅助、仅 PTY 嵌 TUI 的逻辑 |

### 4.4 包名与入口脚本

- 发行包名：`xhermes-agent`（若 fork 另有定名，实现时单一常量，不双轨）。
- Console scripts：至少 `xhermes`（含 `serve`）、`xhermes-acp`。
- SDK 导入：`import hermes_runtime`（名称实现前可微调，spec 以该名为准）。

### 4.5 体积预期

| 手段 | 效果 |
|------|------|
| 去掉 apps / ui-tui / web / website | 去掉最大前端与 Node 体积 |
| 单包全量 Python 依赖 | 安装环境仍可能很大（平台 SDK + browser/TTS 等） |
| 不打包前端测试与文档站进 wheel | 避免回归打进无关资产 |

CI 打印 wheel 解压 top 体积；设门禁防止再次打进 `web_dist` / `node_modules`（不设绝对 MB 上限，因全量依赖会波动）。

---

## 5. SDK API（`hermes_runtime`）

### 5.1 约束

- 薄模块：标准库为主；可复用本仓库 ready 解析约定。
- **不**在 SDK 进程内 import `AIAgent` 或全量拉起 gateway 业务（避免宿主被重依赖拖死）。
- 只负责：spawn / 读 ready / 返回连接信息 / stop。

### 5.2 API 草图

```python
from hermes_runtime import HermesRuntime, RuntimeInfo

rt = HermesRuntime(
    hermes_home=None,          # default: get_hermes_home() / XHERMES_HOME
    profile=None,
    host="127.0.0.1",
    port=0,
    extra_env=None,
    xhermes_executable=None,   # default: console script on PATH / same env
)

info: RuntimeInfo = rt.start(timeout_s=60)
# info.ws_url, info.base_url, info.token, info.port, info.pid
# token = SDK 生成并经 XHERMES_DASHBOARD_SESSION_TOKEN 注入（非 serve 输出，见 §5.3）

rt.stop(grace_s=10)
# also: with HermesRuntime(...) as rt: ...
```

可选辅助：`wait_ready`、`is_running`、`poll_health`、`find_xhermes`。

**第一版不做**：`rt.chat(...)`、进程内 `run_conversation`、自动重连、在 API 里注入 API key。

可选后续：极薄 `hermes_runtime.rpc.JsonRpcGatewayClient`；正式契约仍是协议方法/事件名（见 Desktop Gateway Protocol 文档）。

### 5.3 子进程约定

```text
xhermes [--profile P] serve --host 127.0.0.1 --port 0
```

环境：`XHERMES_HOME` / profile；`XHERMES_SERVE_HEADLESS=1`（serve 自动设置，SDK 双保险）；**`XHERMES_DASHBOARD_SESSION_TOKEN=<sdk_token>`（SDK 生成，见下）**。

Ready + token 机制（代码核查确认，修正初稿"端口 + token"表述）：
- serve headless 输出 `XHERMES_BACKEND_READY port=<n>`，**只含 port，不含 token**（`web_server.py:17536-17541` 注释明确 headless "不广播连接 URL，只 announce bind"）
- WS `/api/ws` 在 loopback 下**仍需 `?token=<_SESSION_TOKEN>`**（`_ws_auth_reason` L14653-14658；HTTP 页面 loopback 免 auth，但 WS 不免——初稿"loopback 免 token"假设不成立）
- `_SESSION_TOKEN` 优先读 `XHERMES_DASHBOARD_SESSION_TOKEN` env，否则随机生成（L301）
- **SDK 方案**：spawn 前 SDK 生成 `token = secrets.token_urlsafe(32)`，设 `XHERMES_DASHBOARD_SESSION_TOKEN` env 传入 serve → 解析 ready 行拿 port → 拼 `ws://127.0.0.1:<port>/api/ws?token=<token>` 连接。token 由 SDK 主动注入，无需 serve 输出。

超时 → 杀子进程并抛 `RuntimeStartError`（附最后 N 行日志）。

进程管理：优先进程组 / `start_new_session`；文档要求宿主退出钩子调用 `stop`；可选 PID 文件便于回收。

### 5.4 配置与多实例

- 密钥与配置仍在 `XHERMES_HOME` 的 `config.yaml` + `.env`。
- 多实例：不同 `hermes_home` 或 profile，沿用现有 token lock 等隔离机制。

### 5.5 SDK 层错误

| 情况 | 行为 |
|------|------|
| 找不到 `xhermes` | `RuntimeBinaryNotFound` |
| ready 超时 / 子进程非零退出 | 终止子进程 + `RuntimeStartError` |
| `stop()` 时已死 | 幂等 no-op |
| 宿主崩溃未 stop | 进程组 + 文档 + 可选 PID 文件 |

---

## 6. 交互数据流（一轮对话）

```text
Host: rt.start()
  → serve 就绪，返回 ws_url
Host: WS connect → gateway.ready
Host: session.create / session.resume（按客户端需要）
Host: prompt.submit { session_id, text }
  → RPC result { status: "streaming" }
Agent: message.delta / tool.generating / tool.start / tool.progress / tool.complete / …
Host: 渲染进度
Agent: message.complete / session.info
Host exit: rt.stop()
```

工具进度是否可见：由网关事件 + `display.tool_progress` 配置决定；协议面与现 Desktop 一致。

---

## 7. 构建、测试、文档、分期

### 7.1 构建与发布

- `python -m build` → wheel + sdist。
- 打包白名单仅后端；依赖全量并入主 dependencies。
- 发布渠道（PyPI / 私有源）实现时选定；CI：`build-wheel` + 安装烟测。

### 7.2 测试

| 层级 | 内容 |
|------|------|
| 单元 | Runtime：fake binary、ready 解析、超时杀进程、stop 幂等 |
| 烟测 | `pip install dist/*.whl` → `start` → WS `gateway.ready` → `stop` |
| 协议 | 保留现有 `tui_gateway` / serve 相关测试 |
| 负面 | 无 `web_dist` 时 serve 可起；UI 子命令行为符合设计 |

### 7.3 文档

- 在 Programmatic Integration / Desktop Gateway Protocol 增加「Headless wheel + HermesRuntime」；若发行版不打包 website，则镜像到仓库 `docs/` 或 README。
- 最小集成示例：Python 启 runtime + 任意语言 WS 客户端发 `prompt.submit`。

### 7.4 分期落地

1. **P0** — 打包去 UI + `HermesRuntime` + 烟测  
2. **P1** — 依赖全量并入、UI 子命令清理、文档  
3. **P2**（可选）— `agent_gateway` 文档/别名；可选薄 Python RPC client  

---

## 8. 与既有设计的关系

| 文档 | 关系 |
|------|------|
| `2026-08-03-cli-tui-prune-design.md` | **tui_gateway 与 web_server.py 不能删**：`/api/ws` 路由在 web_server.py、dispatch 在 tui_gateway.ws（`from tui_gateway.ws import handle_ws`），二者是本方案协议面的必需组件。prune v3 的"删 tui_gateway + 删 web_server.py"与本方案**直接对立**——二者是**互斥发行形态**：prune = 纯 CLI 裁剪（无 WS 协议面），本方案 = headless wheel（保留 WS 协议面），**不可叠加到同一发行版**。prune 文档应加脚注：若需 headless wheel 发行形态，其"删 tui_gateway/web_server"任务撤销。 |
| `2026-08-03-pkg-wrap-installsh-design.md` | 正交（macOS pkg 薄壳 ≠ pip wheel）。 |
| 现有 `xhermes-agent` 包 | 本方案是发行形态收敛，不是重写 agent。 |

---

## 9. 风险与缓解

| 风险 | 缓解 |
|------|------|
| 单包体积大、安装慢 | 文档如实说明；extras 减肥列为未来工作，不在本范围 |
| 宿主未 stop 残留进程 | 进程组 + 退出钩子文档 + 可选 PID 文件 |
| ready 行格式变更 | 与 Desktop 共用约定 + 单测锁格式 |
| 与「删 tui_gateway」裁剪并行 | **tui_gateway 不能删**（`/api/ws` 依赖 `tui_gateway.ws` dispatch）；二者互斥发行形态，见 §8 |
| 全量依赖供应链面大 | 继续遵守现有 pin 策略；不在本设计放宽 pin |

---

## 10. 成功标准

1. `pip install <wheel>` 后，无 Node/前端资产即可 `HermesRuntime.start()` 并收到 `gateway.ready`。  
2. 第三方桌面端仅用 WS JSON-RPC 即可完成：建会话、提交 prompt、观察 tool/message 事件、interrupt、stop。  
3. Wheel 内不含 `apps/`、`ui-tui/`、`web/`、`website/` 内容。  
4. 消息网关 / ACP / cron / MCP / browser / vision / TTS 等后端能力在 headless 发行版中仍可配置启用（依赖已打进主包）。  

---

## 11. 审批记录

| 章节 | 结果 |
|------|------|
| §1 目标与架构 | 用户同意 |
| §2 包边界与裁剪 | 用户同意（OK） |
| §3 SDK API 与数据流 | 用户同意 |
| §4 构建测试迁移 | 用户同意（OK） |
| 推荐架构 | Subprocess-first SDK（方案 1） |
