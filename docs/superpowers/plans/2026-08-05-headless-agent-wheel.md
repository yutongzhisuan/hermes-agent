# Headless Agent Wheel 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 发布供第三方桌面端集成的 **headless Python wheel**：无 TUI/Desktop/Dashboard SPA，保留 agent 全后端能力；桌面端通过 `HermesRuntime` spawn `xhermes serve`，经 WebSocket JSON-RPC 驱动 agent。

**Architecture:** Subprocess-first SDK（方案 1）。`hermes_runtime` 只管子进程生命周期 + WS URL；协议面沿用 `web_server.py`（HTTP/WS 路由）+ `tui_gateway.ws`（JSON-RPC dispatch）。Wheel 打包后端 Python 包 + 运行时数据资产（skills/locales/…），不打包前端目录。

**Tech Stack:** Python 3.11–3.13、setuptools/PEP 517、`fastapi`/`uvicorn`（serve）、现有 `tui_gateway`、pytest

**设计文档:** `docs/superpowers/specs/2026-08-05-headless-agent-wheel-design.md`

**与旧 prune 文档关系:** 本计划 **保留** `tui_gateway/` 与 headless `web_server` 路径；`2026-08-03-cli-tui-prune-design.md` 中「删 tui_gateway/web_server」任务 **不得** 与本计划并行执行。

---

## 前置：现状与阻塞点

实施前必须知晓的仓库事实：

| 事实 | 影响 |
|------|------|
| `setup.py` 默认 **禁止** 非 Nix 环境构建 wheel/sdist（`XHERMES_NIX_BUILD=1` 才放行） | P0 须新增 headless wheel 构建门闩 |
| `serve` 依赖 `[web]` extra（`fastapi` + `uvicorn`） | 烟测前至少要把 web 依赖并入 core 或文档化 `pip install xhermes-agent[web]` |
| `skills/`、`locales/`、`optional-skills/`、`optional-mcps/` 不在 `packages.find` 内 | Wheel 须显式打包数据资产 + 运行时解析 |
| `plugins/` 已在 `packages.find` 内 | 随 wheel 安装；`get_bundled_plugins_dir()` 开发态 fallback 仍有效 |
| Desktop 已有 TS `backend-ready.ts` 解析 `XHERMES_BACKEND_READY port=<n>` | Python SDK 应对齐同一 regex；token 由 SDK 注入 `XHERMES_DASHBOARD_SESSION_TOKEN` |

验证基线（开发态）：

```bash
source .venv/bin/activate   # 或 venv
scripts/run_tests.sh tests/test_packaging_build_guard.py -q
```

---

## 阶段总览

| 阶段 | 目标 | 交付物 |
|------|------|--------|
| **P0** | 可构建 headless wheel + `HermesRuntime` + 烟测 | 构建门闩、数据资产打包、SDK、CI 烟测 |
| **P1** | 单包全量依赖 + UI 入口清理 + 集成文档 | `pyproject.toml` 依赖合并、CLI 裁剪、docs |
| **P2** | 可选增强 | 文档别名、薄 RPC client、发布流水线硬化 |

**依赖顺序:** P0 → P1 → P2。P1 的「全量依赖」不阻塞 P0 烟测（P0 仅保证 serve + gateway 可起）。

---

# P0 — 可构建 Wheel + HermesRuntime + 烟测

## Task P0-1: Headless wheel 构建门闩

**Files:**
- Modify: `setup.py`
- Modify: `tests/test_packaging_build_guard.py`
- Create: `scripts/build_headless_wheel.sh`

**目标:** 允许 CI/发布在显式标记下构建 wheel，且 **不** 放开普通开发环境的误构建。

- [ ] **Step 1: 扩展 `setup.py` 放行条件**

在 `_IN_NIX_BUILD` 之外增加 headless 发行构建标记：

```python
_IN_HEADLESS_WHEEL_BUILD = os.environ.get("XHERMES_HEADLESS_WHEEL_BUILD") == "1"
_ALLOWED = _IN_NIX_BUILD or _IN_HEADLESS_WHEEL_BUILD
```

`_GuardedSdist` / `_GuardedBdistWheel` 的 `if not _IN_NIX_BUILD` 改为 `if not _ALLOWED`；错误信息区分 Nix vs headless wheel 指引。

- [ ] **Step 2: 更新 packaging guard 测试**

`tests/test_packaging_build_guard.py` 增加用例：

- `XHERMES_HEADLESS_WHEEL_BUILD=1` → wheel/sdist 构建 **成功**
- 无标记 → 仍 **拒绝**（保持现有行为）

- [ ] **Step 3: 添加构建脚本**

`scripts/build_headless_wheel.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail
export XHERMES_HEADLESS_WHEEL_BUILD=1
python -m pip install -q build
python -m build -w -o dist/
# 打印 wheel 体积与 top-level 目录清单（供回归对比）
```

- [ ] **Step 4: 运行测试**

```bash
scripts/run_tests.sh tests/test_packaging_build_guard.py -q
XHERMES_HEADLESS_WHEEL_BUILD=1 scripts/build_headless_wheel.sh
```

Expected: guard 测试 PASS；dist/*.whl 生成

- [ ] **Step 5: Commit**

```bash
git add setup.py tests/test_packaging_build_guard.py scripts/build_headless_wheel.sh
git commit -m "feat(packaging): allow headless wheel builds via XHERMES_HEADLESS_WHEEL_BUILD"
```

---

## Task P0-2: Wheel 数据资产打包（skills / locales / …）

**Files:**
- Modify: `pyproject.toml`（`package-data` / `data-files` / `MANIFEST.in` 若需要）
- Modify: `hermes_constants.py`（site-packages 解析）
- Create: `tests/test_headless_wheel_assets.py`

**目标:** `pip install` 后无需 git checkout 即可找到 bundled skills/locales/optional-skills/optional-mcps。

**推荐布局（对齐 Nix `share/xhermes-agent/`）：**

```text
site-packages/
  agent/ tools/ …          # 现有 Python 包
  xhermes_agent_data/
    skills/
    optional-skills/
    locales/
    optional-mcps/
```

- [ ] **Step 1: 定义数据根解析函数**

在 `hermes_constants.py` 增加（示意）：

```python
def get_package_data_root() -> Path | None:
    """Return bundled data root when installed from headless wheel."""
    # importlib.metadata.files("xhermes-agent") → .../xhermes_agent_data
    ...

def get_bundled_skills_dir(default=None) -> Path:
    # 在 env override 之后、caller default 之前插入 get_package_data_root()/skills
    ...
```

对 `get_optional_skills_dir`、`get_optional_mcps_dir`、`get_bundled_locales_dir`（若尚无则新增，与 locales 加载点对齐）应用同一模式。

- [ ] **Step 2: 配置 setuptools 打包数据目录**

在 `pyproject.toml` 增加 data 包或 `tool.setuptools.data-files`，确保 `bdist_wheel` 含上述目录。  
**禁止** 打进 `apps/`、`ui-tui/`、`web/`、`website/`、`web_dist/`、`node_modules/`。

- [ ] **Step 3: 写 wheel 内容契约测试**

`tests/test_headless_wheel_assets.py`（在 `XHERMES_HEADLESS_WHEEL_BUILD=1` 构建后，或模拟安装到 temp venv）：

- wheel 内存在 `skills/**/SKILL.md`（数量 ≥ 1）
- wheel 内存在 `locales/en.yaml`
- wheel 内 **不存在** `apps/desktop`、`ui-tui`、`website` 路径片段

- [ ] **Step 4: 运行测试**

```bash
XHERMES_HEADLESS_WHEEL_BUILD=1 scripts/build_headless_wheel.sh
python -m venv /tmp/xhermes-wheel-test
/tmp/xhermes-wheel-test/bin/pip install dist/xhermes_agent-*.whl
/tmp/xhermes-wheel-test/bin/python -c "from hermes_constants import get_bundled_skills_dir; print(get_bundled_skills_dir())"
scripts/run_tests.sh tests/test_headless_wheel_assets.py -q
```

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(packaging): bundle runtime data assets in headless wheel"
```

---

## Task P0-3: Serve 最小依赖（烟测前置）

**Files:**
- Modify: `pyproject.toml`
- Modify: `tests/test_project_metadata.py`

**目标:** 干净 venv `pip install wheel` 后 `xhermes serve` 不因缺 `fastapi` 失败。

- [ ] **Step 1: 将 `[web]` extra 依赖并入 core `dependencies`**

至少：

```toml
"fastapi==0.133.1",
"uvicorn[standard]==0.41.0",
"starlette==1.3.1",
"python-multipart==0.0.32",
```

（版本与现有 `[web]` extra 对齐；`[web]` 可保留为空别名或 no-op 以兼容旧安装命令。）

- [ ] **Step 2: 更新 metadata 测试**

若存在对 `[web]` 独有性的断言，改为「web 能力在 core 中」。

- [ ] **Step 3: 烟测手动验证**

```bash
/tmp/xhermes-wheel-test/bin/xhermes serve --host 127.0.0.1 --port 0
# 期望 stdout 出现 XHERMES_BACKEND_READY port=<n>
```

- [ ] **Step 4: Commit**

```bash
git commit -m "feat(packaging): promote web/serve deps to core for headless wheel"
```

---

## Task P0-4: 实现 `hermes_runtime` SDK

**Files:**
- Create: `hermes_runtime/__init__.py`
- Create: `hermes_runtime/_ready.py`
- Create: `hermes_runtime/_spawn.py`
- Create: `hermes_runtime/runtime.py`
- Create: `hermes_runtime/exceptions.py`
- Modify: `pyproject.toml`（`packages.find` 加入 `hermes_runtime`）
- Create: `tests/hermes_runtime/test_runtime.py`

**API（与设计 spec §5 对齐）:**

```python
@dataclass(frozen=True)
class RuntimeInfo:
    ws_url: str
    base_url: str
    token: str
    port: int
    pid: int

class HermesRuntime:
    def start(self, *, timeout_s: float = 90.0) -> RuntimeInfo: ...
    def stop(self, *, grace_s: float = 10.0) -> None: ...
    def is_running(self) -> bool: ...
    def __enter__(self): ...
    def __exit__(self, *exc): ...
```

**实现要点:**

| 项 | 规则 |
|----|------|
| Spawn argv | `["xhermes"] + (["--profile", p] if p else []) + ["serve", "--host", host, "--port", str(port)]` |
| Token | spawn 前 `token = secrets.token_urlsafe(32)`，env `XHERMES_DASHBOARD_SESSION_TOKEN=token` |
| Headless | env `XHERMES_SERVE_HEADLESS=1`（双保险） |
| Ready | 解析 stdout 行 `XHERMES_BACKEND_READY port=(\d+)`（与 `backend-ready.ts` 同 regex） |
| WS URL | `ws://{host}:{port}/api/ws?token={quote(token)}` |
| 进程组 | POSIX: `start_new_session=True`；Windows: 文档化 `CREATE_NEW_PROCESS_GROUP` 若需要 |
| 可执行文件 | `shutil.which("xhermes")` 或 `importlib.metadata` entry point 回退 |

- [ ] **Step 1: 实现 `_ready.py`（纯函数，可单测）**

- [ ] **Step 2: 实现 `HermesRuntime`**

- [ ] **Step 3: 单元测试（不启真实 serve）**

Fake child 向 stdout 写 ready 行；测超时、early exit、stop 幂等、token/URL 拼接。

- [ ] **Step 4: 运行测试**

```bash
scripts/run_tests.sh tests/hermes_runtime/ -q
```

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(runtime): add HermesRuntime subprocess SDK for headless serve"
```

---

## Task P0-5: 端到端烟测（wheel 安装 → gateway.ready）

**Files:**
- Create: `tests/hermes_runtime/test_wheel_smoke.py`
- Modify: `.github/workflows/tests.yml`（或新建 `wheel-smoke.yml`，按 CI 分类器接入）

**目标:** 证明第三方集成最小路径可行。

- [ ] **Step 1: 烟测用例**

流程：

1. `XHERMES_HEADLESS_WHEEL_BUILD=1` 构建 wheel（或 CI 缓存 artifact）
2. temp venv + `pip install dist/*.whl`
3. temp `XHERMES_HOME`
4. `HermesRuntime(...).start(timeout_s=120)`
5. `websockets` 或 stdlib 连接 `info.ws_url`
6. 收到 JSON-RPC event `gateway.ready`
7. `rt.stop()`

标记 `@pytest.mark.integration` 或单独 job，避免拖慢默认 PR 测试。

- [ ] **Step 2: CI job**

```yaml
# 伪代码
- run: XHERMES_HEADLESS_WHEEL_BUILD=1 scripts/build_headless_wheel.sh
- run: scripts/run_tests.sh tests/hermes_runtime/test_wheel_smoke.py -q
```

- [ ] **Step 3: Commit**

```bash
git commit -m "test(runtime): add headless wheel install smoke test"
```

---

## P0 完成标准（DoD）

- [ ] `XHERMES_HEADLESS_WHEEL_BUILD=1` 可构建 wheel
- [ ] Wheel 不含前端目录；含 skills/locales 等数据资产
- [ ] `HermesRuntime.start()` → 可连 `/api/ws` 并收到 `gateway.ready`
- [ ] 单元测试 + 烟测在 CI 绿

---

# P1 — 单包全量依赖 + UI 清理 + 文档

## Task P1-1: 依赖全量并入（单包策略）

**Files:**
- Modify: `pyproject.toml`
- Modify: `tests/test_project_metadata.py`
- Create: `docs/packaging/headless-wheel-dependencies.md`（迁移表）

**目标:** 实现设计决策「单包全量」：`pip install xhermes-agent` 即含 ACP、messaging 常用栈等后端能力依赖。

- [ ] **Step 1: 编写 extras → core 迁移表**

至少覆盖（与设计 §4.1 对齐）：

| Extra / 能力 | 是否并入 core | 备注 |
|--------------|---------------|------|
| `web` | P0 已并 | serve 必需 |
| `acp` | 是 | ACP 子命令 |
| `mcp` | 是 | MCP client |
| `messaging` | 是 | Telegram/Discord/Slack 等 |
| `dingtalk` / `feishu` | 是 | 国内平台 |
| `edge-tts` / `voice` / `wake` | 是 | TTS/语音后端 |
| `google` / `youtube` | 视技能需求 | 文档说明体积 |
| `matrix` | **否**（保持 lazy） | 现有 policy：Windows 无法 build python-olm |
| `honcho` / `supermemory` / `mem0` | **否**（保持 lazy） | 现有 lazy-install 政策 |

**注意:** 全量并入会显著增大 wheel/解析时间；须在 `headless-wheel-dependencies.md` 如实记录，并保留 `[all]` / lazy 政策测试不回归。

- [ ] **Step 2: 合并 pins 到 `dependencies`**

严格沿用现有 exact pin；合并后 `uv lock`。

- [ ] **Step 3: 更新 `test_lazy_installable_extras_excluded_from_all` 等契约**

若某 extra 已进 core，从 lazy_covered 列表移除并调整断言。

- [ ] **Step 4: 验证**

```bash
uv lock
scripts/run_tests.sh tests/test_project_metadata.py -q
```

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(packaging): merge backend extras into core for headless wheel"
```

---

## Task P1-2: UI 子命令与启动路径清理

**Files:**
- Modify: `hermes_cli/main.py`
- Modify: `hermes_cli/subcommands/dashboard.py`（保留 serve；dashboard 行为见下）
- Modify: `pyproject.toml`（若移除 desktop 相关 entry）
- Create: `tests/hermes_cli/test_headless_ui_guards.py`

**目标:** Headless 发行版不含 UI；误调用时给出清晰错误，而非 spawn 已删目录。

| 入口 | Headless 行为 |
|------|----------------|
| `xhermes serve` | **保留**（主路径） |
| `xhermes dashboard` | 报错：「此发行版不含 Web UI；请使用 serve + WS 客户端」 |
| `xhermes desktop` / `gui` | 同上或移除 subparser |
| `xhermes --tui` | 报错：「此发行版不含 TUI」 |
| `xhermes acp` | **保留** |
| `xhermes gateway …` | **保留** |

实现策略（二选一，推荐 A）：

- **A. 编译期/打包期 flag** — `XHERMES_HEADLESS_DIST=1` 写入 wheel 构建 env，runtime 检测后禁用 UI 入口
- **B. 直接删除 subparser** — 更干净，但与「同源码双发行」冲突

若仓库仍需支持「全功能源码树 + headless wheel」双轨，选 **A**。

- [ ] **Step 1: 实现 UI guard 与测试**

- [ ] **Step 2: 确认 `serve` headless 不依赖 `web_dist`**

复用现有 `XHERMES_SERVE_HEADLESS` / `mount_spa` 禁用逻辑；加测试。

- [ ] **Step 3: Commit**

```bash
git commit -m "feat(cli): disable UI entrypoints in headless wheel distribution"
```

---

## Task P1-3: 集成文档

**Files:**
- Modify: `website/docs/developer-guide/programmatic-integration.md`
- Modify: `website/docs/developer-guide/desktop-gateway-protocol.md`
- Create: `docs/packaging/headless-wheel.md`（若 website 不随 wheel 发布，此文件为集成方主文档）

**内容清单:**

1. 安装：`pip install xhermes-agent`（体积与 Python 版本说明）
2. 最小示例：`HermesRuntime` + WS `prompt.submit`
3. 协议索引：RPC / event 表链接
4. 配置：`XHERMES_HOME`、`config.yaml`、`.env` 密钥
5. 多实例：profile / 不同 `hermes_home`
6. 与官方 Desktop 差异：无 bundled Electron；协议兼容

- [ ] **Step 1: 撰写文档**

- [ ] **Step 2: Commit**

```bash
git commit -m "docs: add headless wheel integration guide"
```

---

## P1 完成标准（DoD）

- [ ] 单包安装后可启用设计列出的后端能力（除 intentional lazy 项）
- [ ] UI 入口在 headless 发行版不可达或明确报错
- [ ] 集成文档可让第三方桌面端独立完成 spawn + WS 对话

---

# P2 — 可选增强

## Task P2-1: Agent Gateway 文档别名

**Files:**
- Modify: `tui_gateway/__init__.py`（模块 docstring）
- Modify: 开发者文档

**目标:** 对外称 **Agent Gateway**；代码包名暂保留 `tui_gateway` 以减少破坏性 diff。

- [ ] 文档与 diagram 统一用语
- [ ] 可选：`import tui_gateway as agent_gateway` 的 compatibility note

---

## Task P2-2: 可选薄 Python RPC Client

**Files:**
- Create: `hermes_runtime/rpc.py`（可选）
- Create: `tests/hermes_runtime/test_rpc.py`

**目标:** Python 宿主不必引入 `@xhermes/shared`；提供最小 `connect()` / `request()` / `on_event()`。

**约束:** 不封装业务语义（无 `chat()`）；与 `apps/shared/src/json-rpc-gateway.ts` 行为对齐即可。

---

## Task P2-3: 发布流水线硬化

**Files:**
- Modify: `.github/workflows/tests.yml` 或新建 release workflow
- Modify: `scripts/build_headless_wheel.sh`

- [ ] PR：wheel 内容门禁（无 frontend 路径）
- [ ] Release：上传 wheel 到 PyPI/私有源 + 版本 tag
- [ ] 发布说明模板：体积、Python 版本、headless 限制

---

# 风险登记与缓解

| 风险 | 阶段 | 缓解 |
|------|------|------|
| `setup.py` 误放开所有 wheel 构建 | P0 | 仅 `XHERMES_HEADLESS_WHEEL_BUILD=1` |
| 数据资产未打进 wheel → skills 空 | P0 | 契约测试 + `get_package_data_root` |
| 全量依赖导致 Windows CI 失败（matrix/olm） | P1 | 保持 matrix lazy；文档说明 |
| 与 cli-tui-prune 分支冲突 | 全程 | 本计划优先；禁止删 `tui_gateway` |
| ready/token 语义漂移 | P0 | 单测锁 regex；对齐 `web_server.py` |
| Wheel 体积过大 | P1 | 文档预期；P2 再评估 extras 减肥 |

---

# 建议执行顺序（单人/单 agent）

```text
P0-1 → P0-2 → P0-3 → P0-4 → P0-5   (MVP 可交付)
       ↓
P1-1 → P1-2 → P1-3                 (生产级 headless 发行)
       ↓
P2-*                               (按需)
```

**MVP 定义:** 完成 P0 即可给第三方桌面端做 PoC 集成（spawn serve + WS JSON-RPC）。

---

# 审批

| 项 | 状态 |
|----|------|
| 设计 spec | ✅ `2026-08-05-headless-agent-wheel-design.md` |
| 实现计划 | ✅ 本文档 |
| 开始编码 | ⏳ 待用户确认本 plan |
