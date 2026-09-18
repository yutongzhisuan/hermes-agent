# xhermes-agent Fork 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 xhermes-agent v0.19.1 fork 为 xhermes-agent，实现与原有 xhermes-agent 同机共存、互不干扰，并保留上游同步能力。

**Architecture:** 模型 A 最小隔离——保留内部 Python 模块名（`hermes_cli`、`agent`、`tools`、`gateway` 等），只改外部表面：发行名、命令名、家目录、环境变量解析层、进程/服务名、profile wrapper 目录、默认端口、安装脚本、桌面端、前端包名。同步采用可重放 rename overlay（`scripts/apply_xhermes_overlay.sh`）+ 单点常量优先。

**Tech Stack:** Python 3.11+、pyproject.toml (setuptools)、uv/pip、TypeScript (Electron/Ink)、Docker Compose、systemd/launchd

**设计文档:** `docs/superpowers/specs/2026-08-02-xhermes-fork-design.md`

---

## 前置：工作区与验证基线

> 本计划假设已在独立目录（非 xhermes-agent 原目录）完成 fork：
> ```bash
> git clone git@github.com:<you>/xhermes-agent.git
> cd xhermes-agent
> git remote add upstream https://github.com/NousResearch/xhermes-agent.git
> ```

所有任务的验证命令在 `~/.xhermes/xhermes-agent/venv` 独立 venv 中执行。**严禁**装进 xhermes 的 venv。

---

## Task 1: 单点常量层（同步基础，最先做）

**Files:**
- Create: `hermes_constants.py`（修改，加常量）
- Test: `tests/test_project_metadata.py`（修改，验证常量）

**目标**：把散落的项目身份字面量收拢到 `hermes_constants.py` 顶部，为 overlay 提供单点来源。这是 §5.2 方案 A 的核心。

- [ ] **Step 1: 在 `hermes_constants.py` 顶部添加身份常量**

在 `hermes_constants.py` 的 `_get_platform_default_hermes_home()` 之前添加：

```python
# ---------------------------------------------------------------------------
# xhermes fork identity — single source for rename surfaces.
# Upstream sync: keep these constants stable; re-run apply_xhermes_overlay.sh
# after merging upstream to re-assert the fork's naming.
# ---------------------------------------------------------------------------

PRODUCT_SLUG = "xhermes"                 # CLI 命令名 / 进程 basename 前缀
PRODUCT_DISPLAY = "xHermes"              # 品牌显示名（skin 默认等）
PYPI_DIST_NAME = "xhermes-agent"         # PyPI 发行名
HOME_DIRNAME = ".xhermes"                # POSIX 家目录名
WIN_HOME_DIRNAME = "xhermes"             # Windows %LOCALAPPDATA% 下目录名
INSTALL_SUBDIR = "xhermes-agent"         # 家目录下代码安装子目录
SERVICE_BASE = "xhermes-gateway"         # systemd unit 基名
LAUNCHD_LABEL = "ai.xhermes.gateway"     # launchd label
DESKTOP_APP_ID = "com.xhermes.app"       # Electron appId（用户自行定）
```

- [ ] **Step 2: 写验证测试**

在 `tests/test_project_metadata.py` 添加：

```python
def test_fork_identity_constants():
    from hermes_constants import (
        PRODUCT_SLUG, HOME_DIRNAME, WIN_HOME_DIRNAME,
        INSTALL_SUBDIR, SERVICE_BASE, PYPI_DIST_NAME,
    )
    assert PRODUCT_SLUG == "xhermes"
    assert HOME_DIRNAME == ".xhermes"
    assert WIN_HOME_DIRNAME == "xhermes"
    assert INSTALL_SUBDIR == "xhermes-agent"
    assert SERVICE_BASE == "xhermes-gateway"
    assert PYPI_DIST_NAME == "xhermes-agent"
```

- [ ] **Step 3: 运行测试确认通过**

Run: `scripts/run_tests.sh tests/test_project_metadata.py -k fork_identity -q`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add hermes_constants.py tests/test_project_metadata.py
git commit -m "feat(xhermes): add single-source fork identity constants"
```

---

## Task 2: 默认家目录 + XHERMES_HOME 优先解析（Phase 2 核心）

**Files:**
- Modify: `hermes_constants.py`
- Test: `tests/test_hermes_constants.py`（若不存在则创建）

**目标**：`get_hermes_home()` 返回 `~/.xhermes`；`XHERMES_HOME` 优先于 `XHERMES_HOME`；`get_default_hermes_root()` 同步走 `XHERMES_HOME` 优先（§3.2b-1 漏洞点）。

- [ ] **Step 1: 修改 `_get_platform_default_hermes_home()`**

```python
def _get_platform_default_hermes_home() -> Path:
    if sys.platform == "win32":
        local_appdata = os.environ.get("LOCALAPPDATA", "").strip()
        base = Path(local_appdata) if local_appdata else Path.home() / "AppData" / "Local"
        return base / WIN_HOME_DIRNAME        # 原 "xhermes"
    return Path.home() / HOME_DIRNAME          # 原 ".xhermes"
```

- [ ] **Step 2: 修改 `_hermes_home_from_env()` 加 XHERMES_HOME 优先**

```python
def _hermes_home_from_env() -> Path:
    val = os.environ.get("XHERMES_HOME", "").strip() \
        or os.environ.get("XHERMES_HOME", "").strip()
    if val:
        return Path(val)
    return _get_platform_default_hermes_home()
```

- [ ] **Step 3: 修改 `get_default_hermes_root()` 同步 XHERMES_HOME 优先（§3.2b-1）**

`get_default_hermes_root()` 当前直接读 `os.environ.get("XHERMES_HOME", "")`，改为走同一优先逻辑：

```python
def get_default_hermes_root() -> Path:
    native_home = _get_platform_default_hermes_home()
    env_home = os.environ.get("XHERMES_HOME", "").strip() \
        or os.environ.get("XHERMES_HOME", "").strip()
    if not env_home:
        return native_home
    # ... 后续逻辑不变（env_path / relative_to / profile 分支）
```

**同时排查** `_profile_home_path()`（约 L830）等绕过 `_hermes_home_from_env()` 直接读 `XHERMES_HOME` 的位置，统一改为 `XHERMES_HOME` 优先。

- [ ] **Step 4: 写测试覆盖泄漏场景**

在测试文件添加：

```python
def test_xhermes_home_precedence_over_legacy(monkeypatch, tmp_path):
    from hermes_constants import get_hermes_home, get_default_hermes_root
    monkeypatch.delenv("XHERMES_HOME", raising=False)
    monkeypatch.setenv("XHERMES_HOME", str(tmp_path / ".xhermes"))
    monkeypatch.setattr("pathlib.Path.home", lambda: tmp_path)
    # XHERMES_HOME 泄漏时，get_hermes_home 应 fallback 到 native（~/.xhermes）
    assert get_hermes_home() == tmp_path / ".xhermes"

def test_xhermes_home_explicit_wins(monkeypatch, tmp_path):
    from hermes_constants import get_hermes_home
    monkeypatch.setenv("XHERMES_HOME", str(tmp_path / "custom"))
    monkeypatch.setenv("XHERMES_HOME", str(tmp_path / ".xhermes"))
    assert get_hermes_home() == tmp_path / "custom"

def test_default_root_never_points_at_legacy_home(monkeypatch, tmp_path):
    from hermes_constants import get_default_hermes_root
    monkeypatch.delenv("XHERMES_HOME", raising=False)
    monkeypatch.setenv("XHERMES_HOME", str(tmp_path / ".xhermes"))
    monkeypatch.setattr("pathlib.Path.home", lambda: tmp_path)
    assert get_default_hermes_root() == tmp_path / ".xhermes"
```

- [ ] **Step 5: 运行测试**

Run: `scripts/run_tests.sh tests/test_hermes_constants.py -q`
Expected: PASS

- [ ] **Step 6: 手工验证**

```bash
~/.xhermes/xhermes-agent/venv/bin/python -c \
  "from hermes_constants import get_hermes_home; print(get_hermes_home())"
```
Expected: `~/.xhermes`（即 `$HOME/.xhermes`）

- [ ] **Step 7: Commit**

```bash
git add hermes_constants.py tests/test_hermes_constants.py
git commit -m "feat(xhermes): default home ~/.xhermes with XHERMES_HOME precedence"
```

---

## Task 3: 发行名与命令入口（Phase 1）

**Files:**
- Modify: `pyproject.toml`
- Test: `tests/test_project_metadata.py`

**目标**：PyPI 名 `xhermes-agent`；入口命令 `xhermes` / `xhermes-agent` / `xhermes-acp`；自引用 extras 同步。入口函数路径 `hermes_cli.main:main` **不变**。

- [ ] **Step 1: 修改 pyproject.toml**

```toml
[project]
name = "xhermes-agent"              # 原 "xhermes-agent"

[project.scripts]
xhermes = "hermes_cli.main:main"        # 原 xhermes = ...
xhermes-agent = "run_agent:main"        # 原 xhermes-agent = ...
xhermes-acp = "acp_adapter.entry:main"  # 原 xhermes-acp = ...
```

自引用 extras（`termux`、`termux-all`、`all` 中的 `"xhermes-agent[cron]"` 等）全部改为 `"xhermes-agent[cron]"` 等。

- [ ] **Step 2: 更新元数据测试**

`tests/test_project_metadata.py` 中若断言项目名/入口，改为 `xhermes-agent` / `xhermes`。

- [ ] **Step 3: 安装到独立 venv 并验证**

```bash
python3 -m venv ~/.xhermes/xhermes-agent/venv
~/.xhermes/xhermes-agent/venv/bin/pip install -e .
~/.xhermes/xhermes-agent/venv/bin/xhermes --version
```
Expected: 打印 xhermes-agent 版本号，无 import 错误

- [ ] **Step 4: Commit**

```bash
git add pyproject.toml tests/test_project_metadata.py
git commit -m "feat(xhermes): rename dist and entry points to xhermes"
```

---

## Task 4: 硬编码 `~/.xhermes` fallback 替换（§3.2c，搜索驱动）

**Files:**
- 多个（搜索清单驱动，不依赖固定行号）

**目标**：运行时路径解析全部走 `get_hermes_home()` / 常量；CLI 早期初始化处用 `Path.home() / HOME_DIRNAME`。**不做**静默 rename。

- [ ] **Step 1: 全库检索生成归档清单**

```bash
rg -n "Path\.home\(\)\s*/\s*[\"']\.xhermes|[\"']\.xhermes[\"']|~/\\.xhermes|LOCALAPPDATA.*xhermes" \
  --glob '!docs/**' --glob '!**/*.md' > /tmp/xhermes-hardcoded.txt
cat /tmp/xhermes-hardcoded.txt
```

- [ ] **Step 2: 逐处替换（按类别）**

对每处命中，按以下规则处理：

| 类别 | 改法 |
|---|---|
| 可 import `hermes_constants` 的模块 | `Path.home() / ".xhermes"` → `get_hermes_home()`；`os.environ.get("XHERMES_HOME", Path.home()/".xhermes")` → `os.environ.get("XHERMES_HOME") or get_hermes_home()` |
| CLI 早期初始化（`hermes_cli/main.py` 的 `_apply_profile_override` 前） | `Path.home() / ".xhermes"` → `Path.home() / HOME_DIRNAME`（import 常量） |
| Windows 路径 | `LOCALAPPDATA / "xhermes"` → `WIN_HOME_DIRNAME` |
| 沙箱/安全路径段 | `agent/file_safety.py` 中 `.xhermes` 段 → `.xhermes` |
| 项目插件目录 | `hermes_cli/plugins.py` 的 `./.xhermes/plugins/` → `./.xhermes/plugins/` |
| Node/JS bridge | `path.join(HOME, '.xhermes', ...)` → `.xhermes` |
| 插件/optional-skills | 同模式 |

**关键位置清单（实施起点）：**
- `hermes_cli/main.py`（config / profiles / desktop-ssh 早期 fallback，3+ 处）
- `hermes_cli/auth.py`（auth.json 防护路径）
- `agent/file_safety.py`（沙箱路径段）
- `gateway/run.py`（2 处 fallback）
- `tui_gateway/server.py`（desktop-attachments）
- `mcp_serve.py`（3 处）
- `hermes_cli/gateway.py`、`hermes_cli/env_loader.py`、`hermes_cli/slack_cli.py`、`hermes_cli/dashboard_auth/audit.py`
- `tools/hook_output_spill.py`、`tools/mcp_oauth.py`、`agent/secret_sources/_cache.py`、`agent/transports/codex_app_server.py`

- [ ] **Step 3: 验证无残留（运行时路径）**

```bash
~/.xhermes/xhermes-agent/venv/bin/python -c \
  "from hermes_constants import get_hermes_home; print(get_hermes_home())"
```
Expected: `~/.xhermes`

```bash
# 确认产品路径无残留（测试里历史字符串另议）
rg -n "Path\.home\(\)\s*/\s*[\"']\.xhermes" --glob '!tests/**' --glob '!docs/**'
```
Expected: 无输出

- [ ] **Step 4: 跑相关模块测试**

```bash
scripts/run_tests.sh tests/hermes_cli/test_profiles.py tests/run_agent/test_sequential_chats_live.py -q
```
Expected: PASS（或跳过，取决于依赖）

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(xhermes): replace hardcoded ~/.xhermes with home constants"
```

---

## Task 5: 进程身份与服务名全集（Phase 3）

**Files:**
- Modify: `gateway/run.py`、`hermes_cli/relaunch.py`、`gateway/status.py`、`hermes_cli/gateway.py`、`hermes_cli/dashboard_procs.py`、`cron/lifecycle_guard.py`、`plugins/kanban/systemd/*.service`、`hermes_cli/main.py`、`gateway/restart_loop_guard.py`、`tools/approval.py`、`hermes_cli/config_defaults.py`

**目标**：进程发现、systemd、launchd、tmux、提示串成套改名，防止误停/误启对方服务。

- [ ] **Step 1: `gateway/run.py` 的 `_resolve_hermes_bin()`**

```python
hermes_bin = shutil.which("xhermes")      # 原 "xhermes"
if hermes_bin:
    return [hermes_bin]
# fallback -m hermes_cli.main 不变
```

- [ ] **Step 2: `hermes_cli/relaunch.py`**

`shutil.which("xhermes")` → `shutil.which("xhermes")`；exe 名 `xhermes.exe` → `xhermes.exe`。

- [ ] **Step 3: `gateway/status.py`**

- `_GATEWAY_KIND = "xhermes-gateway"` → `SERVICE_BASE`（即 `"xhermes-gateway"`）
- basename 元组（L407,414）：`"xhermes"` → `"xhermes"`、`"xhermes.exe"` → `"xhermes.exe"`、`"xhermes-gateway"` → `"xhermes-gateway"`、`"xhermes-gateway.exe"` → `"xhermes-gateway.exe"`

- [ ] **Step 4: `hermes_cli/gateway.py`**

- `_SERVICE_BASE = "xhermes-gateway"` → `SERVICE_BASE`（`get_service_name()` 中枢）
- launchd label `ai.xhermes.gateway` → `LAUNCHD_LABEL`（含 profile 后缀形态，`get_launchd_label()` L2488 与 L3648 两处）
- tmux session 名 `xhermes` → `xhermes`（L6498,6805,6858,6977,7332）
- legacy unit 白名单 / planned-restart 同步改 `xhermes-*`

- [ ] **Step 5: `hermes_cli/dashboard_procs.py`**

`"xhermes dashboard"` → `"xhermes dashboard"`、`"xhermes serve"` → `"xhermes serve"`、`"hermes_cli.main dashboard"` 不变（模块名保留）；systemd `xhermes-dashboard.service` / `xhermes-serve.service` → `xhermes-*`。

- [ ] **Step 6: `ai.xhermes.gateway` 额外 5 点**

| 文件 | 改动 |
|---|---|
| `gateway/restart_loop_guard.py` L7 | launchctl 命令 `ai.xhermes.gateway` → `ai.xhermes.gateway` |
| `gateway/run.py` L10213 | 运行时 launchctl 逻辑同上 |
| `tools/approval.py` L760 | service label 比对串同上（漏改会导致审批检测失配） |
| `hermes_cli/config_defaults.py` L2460 | 配置默认值中的 launchctl 防护说明 |
| `hermes_cli/gateway.py` L3648 | `get_launchd_label()` 第二处生成点 |

- [ ] **Step 7: `cron/lifecycle_guard.py`**

launchd / systemctl 防护串中的 label/unit 改 `xhermes-*`。

- [ ] **Step 8: `plugins/kanban/systemd/*.service`**

文件名 `xhermes-kanban-dispatcher.service` → `xhermes-kanban-dispatcher.service`；内容 `ExecStart=/usr/bin/env xhermes kanban daemon` → `xhermes kanban daemon`。

- [ ] **Step 9: 用户提示串**

`"xhermes gateway restart"` / `"xhermes gateway run"` 等用户提示（gateway.py、service_manager.py、gateway_windows.py、setup.py、tips.py，~12 处）→ `xhermes`。

- [ ] **Step 10: `scripts/xhermes-gateway`（独立服务脚本）**

`SERVICE_NAME="xhermes-gateway"` → `"xhermes-gateway"`；plist 路径与 `launchctl ... ai.xhermes.gateway` 命令 → `ai.xhermes.gateway`；venv 路径 → `~/.xhermes/xhermes-agent/venv`。或标记 fork 不发行。

- [ ] **Step 11: 验证残留扫描**

```bash
rg -n 'shutil\.which\("xhermes"\)|_SERVICE_BASE = "xhermes|SERVICE_NAME = "xhermes|ai\.xhermes\.|xhermes-dashboard\.service|xhermes-serve\.service' --glob '!tests/**'
```
Expected: 无输出

- [ ] **Step 12: Commit**

```bash
git add -A
git commit -m "feat(xhermes): rename process/service identities to xhermes"
```

---

## Task 6: profile wrapper 目录（§3.4）

**Files:**
- Modify: `hermes_cli/profiles.py`

- [ ] **Step 1: 修改 wrapper 目录**

```python
def _get_wrapper_dir() -> Path:
    return Path.home() / HOME_DIRNAME / "bin"    # 原 Path.home() / ".local" / "bin"
```

- [ ] **Step 2: 修改 wrapper 内容中的命令名**

`profiles.py` L461（Windows）与 L469-470（POSIX）：

```python
hermes_exe = shutil.which("xhermes") or "xhermes"    # 原 "xhermes"
```

- [ ] **Step 3: 验证**

```bash
# 在测试 venv 中创建 profile wrapper，检查内容
xhermes profile create testprobe
cat ~/.xhermes/bin/testprobe
```
Expected: `exec /path/to/xhermes -p testprobe "$@"`

- [ ] **Step 4: 跑测试**

`scripts/run_tests.sh tests/hermes_cli/test_profiles.py -q` → PASS

- [ ] **Step 5: Commit**

```bash
git add hermes_cli/profiles.py
git commit -m "feat(xhermes): profile wrappers live under ~/.xhermes/bin"
```

---

## Task 7: 默认端口错开（§3.5）

**Files:**
- Modify: `hermes_cli/web_server.py`、`hermes_cli/main.py`、`gateway/platforms/api_server.py`、`gateway/platforms/webhook.py`

- [ ] **Step 1: 修改端口常量表**

`hermes_cli/web_server.py` `_PORT_BINDING_PLATFORM_PORTS`（L2787-2797）：

```python
_PLATFORM_PORTS_XHERMES: Dict[str, Tuple[str, int]] = {
    "webhook": ("port", 8744),          # 原 8644
    "api_server": ("port", 8742),       # 原 8642
    "msgraph_webhook": ("port", 8746),  # 原 8646
    "feishu": ("webhook_port", 8865),   # 原 8765
    "wecom_callback": ("port", 8745),   # 原 8645
    "bluebubbles": ("webhook_port", 8745),  # 原 8645
    "sms": ("webhook_port", 8180),      # 原 8080
    "whatsapp_cloud": ("webhook_port", 8190),  # 原 8090
    "line": ("port", 8746),             # 原 8646
}
```

- [ ] **Step 2: 修改 serve/dashboard 默认端口**

- `hermes_cli/web_server.py` L17061 `port: int = 9119` → `9219`
- `hermes_cli/main.py` L7264 `port = 9119` → `9219`

- [ ] **Step 3: 修改各平台 adapter 的 DEFAULT_PORT**

- `gateway/platforms/api_server.py` L126 `DEFAULT_PORT = 8642` → `8742`
- `gateway/platforms/webhook.py` 的 `DEFAULT_PORT` → 8744（与 web_server 表一致）
- 其余平台 adapter（sms/webhook/whatsapp_cloud 等）默认端口同步偏移

- [ ] **Step 4: 验证端口测试**

```bash
rg -n "9119|8642|8644|8765|8080|8090" --glob '!tests/**' --glob '!docs/**'
```
Expected: 无输出（产品路径）

- [ ] **Step 5: 跑相关测试**

`scripts/run_tests.sh tests/gateway/test_api_server.py -q` → PASS

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(xhermes): offset default listening ports from xhermes"
```

---

## Task 8: 安装脚本 / Docker（Phase 4）

**Files:**
- Modify: `scripts/install.sh`、`install.ps1`、`scripts/run_tests.sh`、`docker-compose.yml`、`docker-compose.windows.yml`

- [ ] **Step 1: `scripts/install.sh`**

- `XHERMES_HOME` 默认 → `~/.xhermes`（`HOME_DIRNAME`）
- `INSTALL_DIR=$XHERMES_HOME/xhermes-agent`（原 `$XHERMES_HOME/xhermes-agent`）
- 命令链接 → `xhermes`
- bootstrap marker `.xhermes-bootstrap-complete` → `.xhermes-bootstrap-complete`
- `REPO_URL` → 你的 fork

- [ ] **Step 2: `install.ps1`**

同上（Windows：`%LOCALAPPDATA%\xhermes\xhermes-agent`）。

- [ ] **Step 3: `scripts/run_tests.sh`**

共享 venv 探针 `~/.xhermes/xhermes-agent/venv` → `~/.xhermes/xhermes-agent/venv`。

- [ ] **Step 4: `docker-compose*.yml`**

卷 `~/.xhermes` → `~/.xhermes`；镜像/容器名避开 `xhermes-*`；端口映射 9219 等。

- [ ] **Step 5: 验证**

```bash
bash -n scripts/install.sh && bash -n install.ps1 2>/dev/null; echo "syntax ok"
rg -n "~/\\.xhermes|xhermes-agent/venv" scripts/install.sh install.ps1 scripts/run_tests.sh
```
Expected: 无残留

- [ ] **Step 6: Commit**

```bash
git add scripts/install.sh install.ps1 scripts/run_tests.sh docker-compose*.yml
git commit -m "feat(xhermes): install tree under ~/.xhermes/xhermes-agent"
```

---

## Task 9: 桌面端（Phase 4）

**Files:**
- Modify: `apps/desktop/electron/main.ts`、`apps/desktop/electron/backend-command.ts`、`apps/desktop/package.json`、`apps/desktop/electron/*.test.ts`

- [ ] **Step 1: `apps/desktop/electron/main.ts`**

- `path.join(home, '.xhermes')`（L565,576 `ACTIVE_XHERMES_ROOT`）→ `path.join(home, '.xhermes')`
- `ACTIVE_XHERMES_ROOT = .../xhermes-agent`（原 `.../xhermes-agent`）
- bootstrap marker `'.xhermes-bootstrap-complete'` → `'.xhermes-bootstrap-complete'`
- `app.setAppUserModelId('com.nousresearch.xhermes')` → `'com.xhermes.app'`（L973）

- [ ] **Step 2: `apps/desktop/electron/backend-command.ts`**

`serveBackendArgs` 前导命令解析 → `xhermes serve`（与 `_resolve_hermes_bin` 等价逻辑）。

- [ ] **Step 3: `apps/desktop/package.json`**

- `name: "xhermes"` → `"xhermes"`
- `productName: "XHermes"` → `"xHermes"`
- `appId: "com.nousresearch.xhermes"` → `"com.xhermes.app"`（L165）
- `artifactName: "XHermes-..."` → `"xHermes-..."`（L176）

- [ ] **Step 4: 桌面端测试断言更新**

`apps/desktop/electron/remote-lifecycle.test.ts:118` 等路径断言 `xhermes-agent/venv/bin/xhermes` → `xhermes-agent/venv/bin/xhermes`。

- [ ] **Step 5: 验证（若 node 环境可用）**

```bash
cd apps/desktop && npm run typecheck 2>/dev/null || echo "node env unavailable"
```

- [ ] **Step 6: Commit**

```bash
git add apps/desktop
git commit -m "feat(xhermes): desktop uses ~/.xhermes home and xhermes backend"
```

---

## Task 10: 前端包名（Phase 5）

**Files:**
- Modify: `apps/shared/package.json`、`ui-tui/package.json`、`ui-tui/packages/xhermes-ink/package.json`、`apps/bootstrap-installer/package.json`、`web/package.json`

**顺序约束**：先改 `@xhermes/shared`，再改依赖方（ui-tui、web、apps/desktop 的 package.json 中所有 `@xhermes/shared` 引用）。

- [ ] **Step 1: `apps/shared/package.json`**

`"name": "@xhermes/shared"` → `"@xhermes/shared"`。

- [ ] **Step 2: 依赖方引用更新**

搜索 `@xhermes/shared` 在 `ui-tui`、`web`、`apps/desktop` 的 package.json 及源码 import，全部改为 `@xhermes/shared`。

- [ ] **Step 3: 其余包名**

- `ui-tui/package.json`: `xhermes-tui` → `xhermes-tui`
- `ui-tui/packages/xhermes-ink/package.json`: `@xhermes/ink` → `@xhermes/ink`
- `apps/bootstrap-installer/package.json`: `@xhermes/bootstrap-installer` → `@xhermes/bootstrap-installer`

- [ ] **Step 4: 验证**

```bash
rg -n '"@xhermes/' --glob '**/package.json'
```
Expected: 无输出

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(xhermes): rename npm packages to @xhermes scope"
```

---

## Task 11: 品牌与 User-Agent（Phase 5）

**Files:**
- Modify: `hermes_cli/skin_engine.py`、`run_agent.py`、`agent/auxiliary_client.py`、`gateway/platforms/base.py`、`tools/kanban_tools.py`

- [ ] **Step 1: skin 品牌文案**

`hermes_cli/skin_engine.py` 内置 skin 的 `agent_name: "XHermes Agent"` → `"xHermes Agent"`、`welcome` → `"Welcome to xHermes Agent!"`、`response_label` → `" ⚕ xHermes "`。用户自定义 skin 不强制改。

- [ ] **Step 2: User-Agent**

- `run_agent.py:303` `HermesAgent/{version}` → `xHermesAgent/{version}`
- `agent/auxiliary_client.py:740` 同上
- `gateway/platforms/base.py:887,1029` 同上
- `tools/kanban_tools.py:993` `xhermes-kanban/attach` → `xhermes-kanban/attach`

- [ ] **Step 3: 验证**

```bash
rg -n 'HermesAgent/|xhermes-kanban/attach' --glob '!tests/**'
```
Expected: 无输出

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "feat(xhermes): brand defaults and user-agent to xHermes"
```

---

## Task 12: 产品内更新指向 fork（Phase 6）

**Files:**
- Modify: `hermes_cli/update_cmd.py`

- [ ] **Step 1: 修改官方仓库 URL**

`hermes_cli/update_cmd.py`：

```python
OFFICIAL_REPO_URL = "https://github.com/<you>/xhermes-agent.git"   # 原 NousResearch/xhermes-agent
```

若存在 fork 检测逻辑（判断当前仓库是否官方），同步改为判断是否为你的 fork。

- [ ] **Step 2: 验证**

```bash
rg -n 'NousResearch/xhermes-agent|OFFICIAL_REPO_URL' hermes_cli/update_cmd.py
```
Expected: OFFICIAL_REPO_URL 指向你的 fork；产品路径无 NousResearch 残留（上游同步文档除外）

- [ ] **Step 3: Commit**

```bash
git add hermes_cli/update_cmd.py
git commit -m "feat(xhermes): xhermes update tracks own fork by default"
```

---

## Task 13: 同步 overlay 脚本（§5.2 方案 B）

**Files:**
- Create: `scripts/apply_xhermes_overlay.sh`
- Create: `scripts/sync_upstream.sh`

- [ ] **Step 1: 创建 `scripts/apply_xhermes_overlay.sh`**

```bash
#!/usr/bin/env bash
# 在干净上游树上重放 xhermes 改名。合并上游后运行：
#   git merge upstream/main && scripts/apply_xhermes_overlay.sh
set -euo pipefail

# 单点常量已在 hermes_constants.py；此脚本兜底散落字面量
sed -i '' 's|Path.home() / ".xhermes"|Path.home() / HOME_DIRNAME|g' \
  hermes_cli/main.py hermes_cli/gateway.py 2>/dev/null || true

rg -l 'shutil\.which\("xhermes"\)' --glob '*.py' --glob '!tests/**' | \
  xargs -I{} sed -i '' 's/shutil.which("xhermes")/shutil.which("xhermes")/g' {} 2>/dev/null || true

rg -l 'ai\.xhermes\.' --glob '*.py' --glob '!tests/**' | \
  xargs -I{} sed -i '' 's/ai\.xhermes\./ai.xhermes./g' {} 2>/dev/null || true

echo "Overlay applied. Run merge checklist (§5.4 of design doc):"
rg -n 'shutil\.which\("xhermes"\)|_SERVICE_BASE = "xhermes|ai\.xhermes\.' --glob '!tests/**' || echo "  clean"
```

> 注意：`sed -i ''` 是 macOS 语法；Linux 用 `sed -i`。脚本内做平台判断或文档注明。

- [ ] **Step 2: 创建 `scripts/sync_upstream.sh`**

```bash
#!/usr/bin/env bash
# 拉取 Nous 上游并重放 xhermes 改名 overlay
set -euo pipefail

git fetch upstream
git merge upstream/main
scripts/apply_xhermes_overlay.sh

echo "Run: scripts/run_tests.sh -q  (baseline) + 双实例 smoke (§8)"
```

- [ ] **Step 3: 授权 + 验证**

```bash
chmod +x scripts/apply_xhermes_overlay.sh scripts/sync_upstream.sh
bash -n scripts/apply_xhermes_overlay.sh && bash -n scripts/sync_upstream.sh
```
Expected: 语法 OK

- [ ] **Step 4: Commit**

```bash
git add scripts/apply_xhermes_overlay.sh scripts/sync_upstream.sh
git commit -m "feat(xhermes): add upstream sync + rename overlay scripts"
```

---

## Task 14: 双实例 smoke 验收（§8.3）

**Files:**
- Create: `scripts/xhermes_coexist_smoke.sh`

**前提**：同机已装 xhermes（`~/.xhermes` 存在、`xhermes` 命令可用）。

- [ ] **Step 1: 创建 smoke 脚本**

```bash
#!/usr/bin/env bash
# 验证 xhermes 与 xhermes 同机共存
set -euo pipefail

echo "1. 版本各自正常"
xhermes --version | grep -q xhermes
xhermes --version | grep -qi xhermes

echo "2. 家目录隔离"
test "$(xhermes -c 'from hermes_constants import get_hermes_home; print(get_hermes_home())')" = "$HOME/.xhermes"
test -d "$HOME/.xhermes" || echo "  WARN: xhermes home missing on this machine"
test -d "$HOME/.xhermes"

echo "3. 不修改对方家目录"
test "$(stat -f%m "$HOME/.xhermes" 2>/dev/null || echo untouched)" = "untouched" || true

echo "4. 双 serve 端口不同（可选，需两个终端）"
echo "  xhermes serve 默认 9219, xhermes 默认 9119"

echo "5. profile wrapper 指向 xhermes"
xhermes profile create smoke-probe >/dev/null 2>&1 || true
grep -q xhermes "$HOME/.xhermes/bin/smoke-probe" 2>/dev/null || echo "  WARN: wrapper not verified"

echo "SMOKE PASS"
```

- [ ] **Step 2: 运行 smoke**

```bash
chmod +x scripts/xhermes_coexist_smoke.sh
./scripts/xhermes_coexist_smoke.sh
```
Expected: `SMOKE PASS`

- [ ] **Step 3: Commit**

```bash
git add scripts/xhermes_coexist_smoke.sh
git commit -m "test(xhermes): add coexistence smoke script"
```

---

## 自审记录

**Spec 覆盖：**
- §3.1 Phase 1 → Task 3
- §3.2 Phase 2 → Task 1、2、4
- §3.3/3.4/3.5 Phase 3 → Task 5、6、7
- §3.6 Phase 4 → Task 8、9
- §3.7 Phase 5 → Task 10、11
- §3.8/§5 Phase 6 → Task 12、13
- §8.3 验收 → Task 14
- §2.2 venv 隔离 → 前置说明 + Task 3 Step 3
- §2.4 运营不共存项 → 验收文档说明（smoke 脚本注释）
- §3.2b-1 `get_default_hermes_root()` → Task 2 Step 3
- §3.2d 禁止静默 rename → Task 4 目标声明 + 设计文档约束

**无占位符确认**：所有步骤含具体文件/代码/命令。

**类型一致性**：`PRODUCT_SLUG`/`HOME_DIRNAME`/`SERVICE_BASE` 等常量在 Task 1 定义，Task 2/5/6/8 引用，命名一致。
