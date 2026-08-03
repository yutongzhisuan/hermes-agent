# xhermes-agent CLI+TUI 目录裁剪设计文档

- **日期**: 2026-08-03
- **状态**: 待审核
- **基准**: hermes-agent v0.19.x（xhermes fork 工作区）
- **目标**: 裁剪 hermes-agent 仓库，只保留 CLI（`hermes` 交互命令）与 TUI（`hermes --tui`），移除桌面端、Web Dashboard、文档站及其配套代码

## 1. 背景与目标

### 1.1 目标

1. 从 hermes-agent 裁剪出仅含 CLI + TUI 的最小代码库
2. 删除 Electron 桌面端（`apps/`）、Web Dashboard（`web/` + `hermes_cli` 内 web 专用文件）、文档站（`website/`）、前端测试（`tests-js/`）
3. 保留核心 agent 能力：CLI 交互、TUI、消息平台网关、ACP IDE 集成、cron 定时任务、记忆、技能、MCP
4. 与已有 xhermes fork 设计（`docs/superpowers/specs/2026-08-02-xhermes-fork-design.md`）衔接——该文档中的桌面端改名任务因此废弃

### 1.2 非目标

- **不删** 消息平台插件（`plugins/platforms/`，21 个：Telegram/飞书/钉钉等）——用户确认保留
- **不删** ACP 适配器（`acp_adapter/`，VS Code/JetBrains 集成）——用户确认保留
- **不删** cron 调度（`cron/`，`/cron` 命令）——用户确认保留
- 不改内部 Python 模块名（与 fork 设计一致，保留上游同步能力）
- 不删 `gateway/` 公共模块（`session_context`/`status`/`config`/`run` 被 CLI/TUI 硬依赖）
- 不删远程环境路径（`tools/environments/ssh.py` 等中的 `/root/.hermes` 是远端容器路径）

## 2. 范围决策

| 决策项 | 选择 | 理由 |
|---|---|---|
| 裁剪策略 | **方案 A：物理删除 + git 历史兜底** | `git rm`，历史可回滚；符合 fork overlay 同步模型 |
| 文档衔接 | **独立 spec + 标注旧文档** | 新写本文档；在 fork 设计中标注 desktop/web 任务废弃 |
| 消息平台 | **保留** | 用户确认 |
| ACP | **保留** | 用户确认 |
| Cron | **保留** | 用户确认 |
| 本地目录改名 `.hermes`→`.xhermes` | **不在此文档范围** | 属 fork 设计的单点常量层（Task 1），且 fork 设计禁止静默 rename |

## 3. 当前目录结构（裁剪前）

```
hermes-agent/
├── apps/                    # 🟥 Electron 桌面端（desktop + shared + bootstrap-installer）
├── web/                     # 🟥 Dashboard SPA 前端（Vite + React）
├── website/                 # 🟥 Docusaurus 文档站
├── tests-js/                # 🟥 前端 vitest 测试
├── ui-tui/                  # ✅ TUI 前端（Ink/React），hermes --tui 用
├── tui_gateway/             # ✅ TUI 后端 JSON-RPC server（stdio/WS）
├── gateway/                 # ✅ 消息网关 + 公共运行时模块（CLI/TUI 硬依赖其中部分）
│   └── platforms/           # ✅ 消息平台适配器（保留）
├── plugins/
│   ├── platforms/           # ✅ 消息平台插件（21 个，保留）
│   ├── model-providers/     # ✅ 模型 provider 注册（CLI 必需）
│   ├── memory/              # ✅ 记忆 provider（CLI 可用）
│   └── (其余)               # ✅ 保留（kanban/dashboard_auth 等按需）
├── acp_adapter/             # ✅ ACP IDE 集成（保留）
├── cron/                    # ✅ 定时任务（保留）
├── agent/                   # ✅ AIAgent 核心（必需）
├── tools/                   # ✅ 工具实现（必需）
├── hermes_cli/              # ⚠️ 部分删除（见 §4.2）
├── cli.py  run_agent.py  model_tools.py  toolsets.py   # ✅ 核心
├── hermes_*.py  utils.py  batch_runner.py 等            # ✅ 核心
├── skills/  optional-skills/  locales/                  # ✅ 内容资源
├── mcp_serve.py             # ✅ MCP server 入口
├── tests/                   # ✅ Python 测试（保留，验证用）
├── scripts/                 # ✅ 运维脚本（保留）
└── (杂项) datagen-config-examples/ mcp-research-data/  # 🟥 无运行时依赖
```

## 4. 删除清单

### 4.1 整目录删除（4 个）

| 目录 | 说明 | 依赖确认 |
|---|---|---|
| `apps/desktop/` | Electron 桌面端 | 仅桌面用，CLI/TUI 零引用 |
| `apps/bootstrap-installer/` | 桌面端安装器（Tauri） | 仅被 `scripts/install.sh` 桌面打包分支、`update_lock.py` 注释引用 |
| `web/` | Dashboard SPA 前端 | 仅 `hermes serve`/dashboard 用；Dockerfile 构建层需同步移除 |
| `website/` | Docusaurus 文档站 | 纯文档 |
| `tests-js/` | 前端 vitest 测试 | 前端配套 |

**⚠️ 保留 `apps/shared/`（TUI 硬依赖）：**

`ui-tui/package.json` 声明 `"@xhermes/shared": "file:../apps/shared"`，TUI 实际 import 了其中的 `skin.ts`（主题契约）、`billing-types.ts`、`charge-settlement.ts`：

```typescript
// ui-tui/src/gatewayTypes.ts
import type { HermesSkin } from '@xhermes/shared/skin'
// ui-tui/src/lib/billingDialog.ts
import type { BillingBlock } from '@xhermes/shared/billing'
// ui-tui/src/app/slash/commands/topup.ts
import { driveChargeSettlement } from '@xhermes/shared/charge-settlement'
```

因此 `apps/` 目录**部分删除**：删 `desktop/` + `bootstrap-installer/`，**保留 `shared/`**。`apps/shared/src/json-rpc-gateway.ts` / `websocket-url.ts` 虽为桌面端编写，但同目录保留，不单独摘除（TUI 未引用它们，无副作用）。

### 4.2 hermes_cli/ 内删除文件（web/serve/dashboard/desktop 专用）

```
web_server.py          # 仅被 main.py cmd_dashboard/cmd_serve 懒加载引用
web_deps.py
web_models.py
web_routers/
web_dist/
web_git.py
dashboard_auth/
dashboard_register.py
pty_bridge.py          # 仅被 web_server 用；hermes_logging.py 中仅为 logger 名（非 import）
pty_session.py
win_pty_bridge.py
windows_ssh_runtime.py # ⚠️ 见 §4.2-1，依赖 desktop-ssh 命令去留
gui_uninstall.py       # 补入：GUI 卸载专用，被 uninstall.py:548/981、main.py:4979/4989 懒加载引用
subcommands/dashboard.py  # 补入：dashboard/serve 子命令构建器，main.py:464 顶层 import
subcommands/gui.py        # 补入：gui 子命令构建器，main.py:465 顶层 import
# 注：dashboard_procs.py 已移出删除清单，见 §5.3（update 流程硬依赖）
```

**说明：** `hermes_cli/webhook.py`（`hermes webhook` 子命令，管理动态 webhook 订阅）**不在此删除清单**——它配合消息网关 webhook adapter（`gateway/platforms/webhook.py`）工作，用户保留消息平台，故保留。`gateway_windows.py`（CLI 在 Windows 上 `gateway start/stop/restart` 的实现，`hermes_cli/gateway.py` 多处引用，见 fork 设计 §3.3）同样保留，不在删除范围。

**验证过的引用事实：**
- `web_server` 只在 `hermes_cli/main.py` 内被 `cmd_dashboard`/`cmd_serve` 懒加载引用（`from hermes_cli.web_server import start_server` 在函数体内）
- `pty_bridge` 被 `hermes_logging.py` 引用处为 **logger 命名字符串**（`"hermes_cli.pty_bridge"` L248），非 import；其余引用在待删的 `web_server.py` 内，删 web_server 时一并消失
- `hermes_state.py` 匹配到的 `pty_session` 是 `delete_empty_sessions` 子串误匹配，无关
- ~~`dashboard_procs.py` 仅被 cmd_dashboard/cmd_serve 懒加载引用~~ **【已修正】**：该判断错误，见 §5.3——实际是 main.py:7227 顶层 import 且 update 流程硬依赖

#### 4.2-1 `windows_ssh_runtime.py` 与 `hermes desktop-ssh` 命令的联合去留

`windows_ssh_runtime.py` 被 `main.py:10074` 懒加载 import `read_token`，服务于 `hermes desktop-ssh` 命令；`tests/hermes_cli/test_ssh_session_token_parser.py:144-149` 断言 `_root() == .../desktop-ssh`。两者绑定，必须一致处理：

| 方案 | 行为 | 何时用 |
|---|---|---|
| **A：都保留** | 保留 `windows_ssh_runtime.py` + `desktop-ssh` 子命令；从 §4.2 删除清单移除该文件 | 用户仍需 `hermes desktop-ssh`（远程桌面 SSH 会话） |
| **B：都删** | 删 `windows_ssh_runtime.py` + 摘除 `desktop-ssh` 子命令注册 + 处理 `main.py:10074` 引用 + 删 `test_ssh_session_token_parser.py` | 用户确认不需要 desktop-ssh |

**默认建议 A（保留）**——desktop-ssh 是 CLI 远程能力，非桌面端 GUI，与"删桌面端"目标不冲突。除非用户明确放弃该命令，否则 `windows_ssh_runtime.py` 不删。

### 4.3 同步修改点

| 文件 | 修改 |
|---|---|
| `hermes_cli/main.py` | ① 摘除 `dashboard`/`serve`/`desktop`/`gui` 子命令注册（9175-9190 行附近 `subparsers` 名字列表），否则 `hermes serve` 报 ImportError；② **移除顶层 import** `from hermes_cli.subcommands.dashboard import build_dashboard_parser`（L464）、`from hermes_cli.subcommands.gui import build_gui_parser`（L465）——这是模块级 import，删 subcommands 文件后不处理会致 CLI 启动崩；③ 移除 §4.2 删文件的懒加载引用（`windows_ssh_runtime` L10074、`gui_uninstall` L4979/4989 等按 §4.2-1 方案处理） |
| `hermes_cli/uninstall.py` | 处理 `gui_uninstall` 引用：L548 `from hermes_cli.gui_uninstall import (...)`、L981 `from hermes_cli.gui_uninstall import uninstall_gui`——删除 gui_uninstall.py 后这两处懒加载会 ImportError，需移除 `run_gui_uninstall` 函数及 L1149 调用，或随 `hermes uninstall --gui` 子命令一并摘除 |
| `package.json`（根） | `workspaces` 移除 `"apps/*"`（改为 `"apps/shared"`）、`"web"`、`"tests-js"`；scripts 移除 `install:web`/`install:desktop`/`audit:web` 等 web/desktop 专属 |
| `Makefile` | 移除 `build-web`/`build-website`/`build-desktop`/`run-dashboard`/`run-serve`/`run-desktop-dev`/`run-website-dev`/`dist:*`/`pack` 目标；`build` 改为仅 `build-tui`；`clean`（138 行）移除 web/apps/website 路径；`test` 目标移除 `apps/desktop test:e2e`（55 行） |
| `Dockerfile` | 移除 `COPY web/` + `cd web && npm run build` 构建层（180/272-275 行）；移除 `ENV HERMES_WEB_DIST`（360 行）；保留 `COPY ui-tui/` 与 `apps/shared/`（ui-tui 构建依赖） |
| `pyproject.toml` | `[tool.setuptools.packages.find]` include 白名单已按名匹配，删目录后自然不打包；核对 `package-data` 无 web 引用 |
| `nix/` | 移除 `desktop.nix`、`web.nix`（若 nix 配置不维护则整体可评估） |
| `scripts/install.sh` | `--include-desktop` 选项（179 行）与桌面打包分支（2826-3023 行）移除或标注失效 |
| `scripts/check-windows-footguns.py` | `"website/build"` 路径检查条目（80 行）移除 |
| `docker-compose*.yml` | 核对无前端构建卷挂载引用（当前仅有 docs 注释，无需改） |

### 4.4 测试文件（tests/ 内 web/dashboard 专用，~50 个）

`tests/hermes_cli/` 下 `test_web_server*.py`（~30 个）、`test_dashboard*.py`（~20 个）、`conftest_dashboard_auth.py`、`tests/test_web_server.py`、`tests/test_install_ps1_web_server_syntax_probe.py`、`tests/docker/test_tui_prebuilt_bundle.py`（检查 web 构建层，Dockerfile 修改后同步调整）——这些测试断言已被删除的 `web_server.py`/dashboard 代码，**删除或跳过**。

## 5. 保留清单及理由

### 5.1 核心依赖链（CLI + TUI 硬依赖）

```
cli.py → agent/ tools/ hermes_cli/ gateway/(session_context,status,config,run)
       → run_agent.py → model_tools.py → toolsets.py → tools/registry.py
tui_gateway/ → agent/ tools/ hermes_cli/ gateway/(session_context,config,run)
ui-tui/     → tui_gateway（stdio JSON-RPC）
```

关键 import 证据：
- `cli.py:944` `from gateway.session_context import set_current_session_id`
- `cli.py:2039` `from gateway.status import _pid_exists`
- `cli.py:9732` `from gateway.config import load_gateway_config, Platform`
- `cli.py:17943` `from gateway.run import start_gateway`（`/gateway` 命令）
- `tui_gateway/server.py:1671` `from gateway.run import _redact_approval_command`
- `tui_gateway/server.py:2900` `from gateway.session_context import set_session_vars`
- `run_agent.py:95` `from gateway.session_context import get_session_env`
- `main.py:10698` `from gateway.platform_registry import platform_registry`

### 5.2 用户确认保留

- `plugins/platforms/`（21 个消息平台插件）
- `acp_adapter/`
- `cron/`
- `gateway/` 全部（含 `platforms/` 适配器）

### 5.3 `dashboard_procs.py` 必须保留（从删除清单移出）

首轮稿误判该文件"仅被 cmd_dashboard/cmd_serve 懒加载引用"。代码核查推翻此判断，它服务于**进程卫生 / update 残留清理**，CLI 必需：

| 引用点 | 性质 | 说明 |
|---|---|---|
| `main.py:7227` | **顶层 import**（`# noqa: F401` re-export） | 删文件→main.py import 失败→CLI 无法启动 |
| `update_cmd.py:578` | `_kill_stale_dashboard_processes(restart_managed=True)` | `xhermes update` 流程调用 |
| `update_cmd.py:3599` | `_detect_concurrent_hermes_instances(scripts_dir)` | `xhermes update` 流程调用 |
| `main.py:9881-9882` | `_scan_dashboard_processes()` + `_parse_dashboard_runtime()` | CLI 命令路径 |
| `main.py:10190/10195/10198` | `--stop` 路径扫描并杀残留进程 | CLI 命令路径 |

**职责**：用户从带 dashboard 的旧版升级到裁剪版时，机器上可能仍有残留的 dashboard 进程。`xhermes update` 必须能扫描并清理这些进程，否则旧进程占用端口/锁文件会阻塞升级。

**结论**：即使删掉 `dashboard`/`serve` 命令本身，`dashboard_procs.py` 仍必须保留。若执意删除，须先将上述函数迁移至 `update_cmd.py` 并处理 `main.py` 顶层 import 与 `_find_stale_dashboard_pids`/`_parse_dashboard_runtime`/`_warn_stale_dashboard_processes` 等调用点（L7233/7241/7596/9881/10190）——成本远高于保留。**默认保留**。

## 6. 裁剪后目录结构（目标）

```
hermes-agent/
├── cli.py  run_agent.py  model_tools.py  toolsets.py
├── hermes_*.py  utils.py  batch_runner.py  trajectory_compressor.py
├── agent/  tools/  hermes_cli/(其余)  tui_gateway/  ui-tui/
├── gateway/(全部)  plugins/(全部)  acp_adapter/  cron/
├── apps/shared/              # ⚠️ 保留（ui-tui 的 TS 类型依赖）
├── skills/  optional-skills/  locales/
├── mcp_serve.py  setup.py  pyproject.toml  uv.lock
├── package.json（workspaces 精简后）  Makefile（精简后）
├── tests/（移除 web_server/dashboard 测试）  scripts/  docker/
└── (无 apps/desktop apps/bootstrap-installer web/ website/ tests-js/)
```

## 7. 与 xhermes fork 文档的衔接

- **废弃标注**：`docs/superpowers/specs/2026-08-02-xhermes-fork-design.md` 中所有 `apps/desktop/**`、`apps/shared/**`、`apps/bootstrap-installer/**` 改名任务不再执行（目录已删除）
- **保留**：fork 设计的单点常量层（`hermes_constants.py` 身份常量）、overlay 同步模型、共存策略不受影响
- **冲突预期**：fork 设计 §4 提到 `apps/desktop/**` 是上游 merge 高频冲突区——裁剪后该区域消失，反而**降低**未来 merge 冲突面

## 8. 风险与回滚

| 风险 | 缓解 |
|---|---|
| 上游 merge 时已删目录改动冲突 | git 历史保留，冲突时按 overlay 模型处理；`git revert` 可临时恢复 |
| `hermes serve`/`dashboard` 命令残留 | §4.3 摘除子命令注册，否则报 ImportError |
| 遗漏某个被 CLI/TUI 引用的 web 文件 | ~~§4.2 已按 import 链验证~~ 首轮核查有误（dashboard_procs 误判），二次核查已修正；§9 验证方案兜底 |
| **误删 `apps/shared/`（TUI 硬依赖）** | **已确认保留**：ui-tui 依赖 `@xhermes/shared`（skin/billing/charge-settlement），且 Dockerfile ui-tui 构建层依赖它 |
| **Dockerfile 引用已删 `web/` 构建层** | §4.3 同步移除 `COPY web/` + `HERMES_WEB_DIST`，否则镜像构建失败 |
| **Makefile/package.json 引用已删目录** | §4.3 同步精简 workspaces 与目标 |
| **~50 个测试断言已删代码** | §4.4 删除/跳过 web_server/dashboard 测试（实测 test_web_server 18、test_dashboard 25，约 44 个） |
| **误删 `dashboard_procs.py`（update 流程依赖）** | §5.3：已移出删除清单；该文件服务 `xhermes update` 残留进程清理，删则 CLI 启动崩且 update 断 |
| **`subcommands/dashboard.py`+`gui.py` 顶层 import 遗漏处理** | §4.3：main.py:464-465 是模块级 import，删文件不处理会致 CLI 启动崩 |
| **`gui_uninstall.py` 删除后 uninstall.py 引用未处理** | §4.3：L548/981 懒加载 import 会 ImportError，需移除 `run_gui_uninstall` 及调用 |
| **`windows_ssh_runtime.py` 误删破坏 `hermes desktop-ssh`** | §4.2-1：该文件与 desktop-ssh 命令绑定，默认方案 A 保留 |

## 9. 验证方案

```bash
# 1. 核心导入（删除后无 ImportError）
python -c "import cli; print('CLI OK')"
python -c "import run_agent, model_tools, toolsets; print('core OK')"
python -c "import tui_gateway.server; print('TUI gateway OK')"

# 2. 残留引用检查（应只命中已删文件或无害 logger 名/注释）
rg -l "web_server|pty_bridge" --type py | grep -v "已删除目录"
rg -l "apps/desktop|website/build|tests-js" scripts/ Makefile Dockerfile package.json

# 3. TUI 前端构建（apps/shared 保留后应可构建）
cd ui-tui && npm run typecheck

# 4. 核心测试（排除已删 web/dashboard 测试后）
scripts/run_tests.sh tests/cli/ -q
scripts/run_tests.sh tests/tui_gateway/ -q
scripts/run_tests.sh tests/agent/test_*.py -q   # 抽样

# 5. 启动冒烟
python -m tui_gateway.entry --help
echo "hi" | timeout 5 python -m tui_gateway.entry   # 观察 gateway.ready
```

## 10. 决策记录

| 决策 | 选择 | 理由 |
|---|---|---|
| 2026-08-03 | 物理删除 + git 历史兜底 | 与 fork overlay 同步模型兼容，可回滚 |
| 2026-08-03 | 独立 spec + 标注旧文档 | 旧 fork 文档含 desktop 改名任务，本次范围更激进 |
| 2026-08-03 | 保留消息平台/ACP/cron | 用户确认 |
| 2026-08-03 | 不迁移 `.hermes`→`.xhermes` | 属 fork Task 1 单点常量层，且 fork 设计禁止静默 rename |
| 2026-08-03 | **保留 `apps/shared/`** | ui-tui 硬依赖其 TS 类型（skin/billing/charge-settlement），删除会破坏 TUI 构建 |
| 2026-08-03 | 保留 `hermes_cli/webhook.py` | 是 `hermes webhook` CLI 子命令，配合消息网关 webhook adapter，非 serve 面 |

**审核补充记录（2026-08-03）：** 首轮审核发现并修正 5 处遗漏——① `apps/shared/` 是 ui-tui 的 TS 依赖，不能整删 `apps/`；② 根 `package.json` workspaces 引用 `apps/*`/`web`/`tests-js` 需精简；③ Dockerfile 构建 web SPA 进镜像（`COPY web/` + `HERMES_WEB_DIST`）需同步移除；④ Makefile 有 ~12 个 web/apps/website 目标需移除；⑤ `tests/` 下 ~50 个 web_server/dashboard 测试文件需删除或跳过。

**二次核查修订（2026-08-03，代码级引用验证）：** 首轮的"验证过的引用事实"有误，二次对照代码库修正 4 处——

1. **`dashboard_procs.py` 移出删除清单**（§5.3）：首轮误称"仅被 cmd_dashboard/cmd_serve 懒加载引用"，实际 `main.py:7227` 顶层 import 且 `update_cmd.py:578/3599` 在 `xhermes update` 流程硬调用（残留进程清理）。删则 CLI 启动崩 + update 断。
2. **补入 `gui_uninstall.py`**（§4.2）：GUI 卸载专用，被 `uninstall.py:548/981`、`main.py:4979/4989` 懒加载引用，首轮 14 项清单遗漏。
3. **补入 `subcommands/dashboard.py` + `subcommands/gui.py`**（§4.2）：子命令构建器，被 `main.py:464-465` 顶层 import，首轮未覆盖；§4.3 补 main.py 顶层 import 处理与 uninstall.py 引用处理。
4. **`windows_ssh_runtime.py` 与 `hermes desktop-ssh` 联合去留**（§4.2-1）：`main.py:10074` 依赖该文件，首轮未交代 desktop-ssh 命令命运；新增方案 A/B，默认保留。

另修正：§4.4 测试数量（test_web_server 实测 18、test_dashboard 25）；§8 风险表"已按 import 链验证"措辞过度自信，已改为承认首轮有误。
