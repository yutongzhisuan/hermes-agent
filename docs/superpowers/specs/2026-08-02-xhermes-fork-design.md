# xhermes-agent Fork 设计文档

- **日期**: 2026-08-02
- **状态**: 待审核
- **基准**: hermes-agent v0.19.1
- **目标**: fork hermes-agent 为 xhermes-agent，与原有 hermes-agent 同机共存、互不干扰，且保留上游同步能力

## 1. 背景与目标

### 1.1 目标

1. 从 hermes-agent fork 出自己的 agent（xhermes-agent）
2. 打包命名、安装命名、目录命名与 hermes-agent **完全错开**
3. 与原有 hermes-agent **同机共存、互不干扰**
4. **保留上游同步能力**：定期拉取 hermes-agent 的修复与新功能

### 1.2 非目标

- 不改内部 Python 模块名（`hermes_cli`、`agent`、`tools`、`gateway`、`tui_gateway`、`hermes_state` 等全部保留）——这是"保留上游同步"的前提
- 不改任何功能性逻辑（纯改名，不新增功能）
- 不做品牌彻底重塑（`xHermes` 浅层品牌，皮肤/文案可后改）

### 1.3 决策记录

| 决策 | 选择 | 理由 |
|---|---|---|
| 共存模型 | **模型 A：最小隔离** | 只改外部表面，内部模块名保留 |
| 上游同步 | **保留** | 定期 cherry-pick/merge upstream 修复 |

---

## 2. 共存机制（核心）

### 2.1 隔离边界总览

xhermes 与 hermes 的隔离依赖 **4 个正交维度**，缺一不可：

| 维度 | hermes | xhermes | 隔离机制 |
|---|---|---|---|
| Python 环境 | 自己的 venv | **独立 venv** | site-packages 永不相交 |
| 家目录 | `~/.hermes` | `~/.xhermes` | 所有状态文件隔离 |
| 命令行 | `hermes` | `xhermes` | PATH 无冲突 |
| 进程/服务名 | `hermes-*` | `xhermes-*` | 互不误杀 |

### 2.2 致命约束：Python 包文件级冲突（必须在 venv 层面隔离）

**问题**：模型 A 保留内部模块名（`hermes_cli`、`agent`、`tools`、`gateway`、`hermes_state`…）。若两个发行版装进**同一个 venv**，pip 安装时同名同路径文件**后者覆盖前者**，互相破坏。site-packages 中不可能共存两个同名包。

**解法（硬性要求）**：

```bash
# 正确：xhermes 永远使用独立 venv
python3 -m venv ~/.xhermes/venv
~/.xhermes/venv/bin/pip install -e /path/to/xhermes
~/.xhermes/venv/bin/xhermes --version

# 或 pipx 隔离
pipx install --python python3.11 /path/to/xhermes
```

**禁止**：`pip install` 装进 hermes 的 venv / 系统 site-packages。

**效果**：命令行 `xhermes` 指向 xhermes venv 的 bin，`hermes` 指向 hermes venv 的 bin，各自装各自的包，永不相交。

---

## 3. 文件级改动清单

### 3.1 Phase 1：发行与入口（pyproject.toml）

| 位置 | 原值 | 改为 |
|---|---|---|
| `pyproject.toml [project] name` | `hermes-agent` | `xhermes-agent` |
| `pyproject.toml [project.scripts]` | `hermes = hermes_cli.main:main` | `xhermes = hermes_cli.main:main` |
| `pyproject.toml [project.scripts]` | `hermes-agent = run_agent:main` | `xhermes-agent = run_agent:main` |
| `pyproject.toml [project.scripts]` | `hermes-acp = acp_adapter.entry:main` | `xhermes-acp = acp_adapter.entry:main` |
| 自引用 extras（termux/termux-all/all） | `hermes-agent[cron]` 等 | `xhermes-agent[...]` |

**关键**：入口函数路径 `hermes_cli.main:main` **不变**（模块未改名），因此 `-m hermes_cli.main` 的 9 处子进程 spawn 无需改动。

验证：`which xhermes && xhermes --version`

### 3.2 Phase 2：家目录与环境变量（`hermes_constants.py`）

#### 3.2a 默认路径

```python
def _get_platform_default_hermes_home() -> Path:
    if sys.platform == "win32":
        ...
        return base / "xhermes"           # 原 "hermes"
    return Path.home() / ".xhermes"       # 原 ".hermes"
```

#### 3.2b 环境变量兼容层（唯一 env 改动点）

```python
def _hermes_home_from_env() -> Path:
    # xhermes: 优先 XHERMES_HOME，兼容遗留 HERMES_HOME
    val = os.environ.get("XHERMES_HOME", "").strip() \
        or os.environ.get("HERMES_HOME", "").strip()
    if val:
        return Path(val)
    return _get_platform_default_hermes_home()
```

**原理**：xhermes 进程把自身 home 传给子进程时仍写 `HERMES_HOME` → 子进程读 `HERMES_HOME` fallback 成功。内部 548 处 `os.environ.get("HERMES_HOME", ...)` 全部保留不动。

#### 3.2c 硬编码 `~/.hermes` fallback 替换（15 处核心 + 22 处插件）

统一改法：`Path.home() / ".hermes"` → `get_hermes_home()`（可 import 处）或 `Path.home() / ".xhermes"`（早期初始化处）。

核心文件清单：

| 文件 | 行 | 改法 |
|---|---|---|
| `hermes_cli/main.py` | 275 | `os.path.join(expanduser("~"), ".xhermes", "config.yaml")` |
| `hermes_cli/main.py` | 549 | `home / ".xhermes" / "profiles" / name` |
| `hermes_cli/main.py` | 1819 | `str(Path.home() / ".xhermes")` |
| `hermes_cli/gateway.py` | 2637 | `Path.home() / ".xhermes"` |
| `hermes_cli/auth.py` | 913 | `get_hermes_home() / "auth.json"` |
| `hermes_cli/dashboard_auth/audit.py` | 66 | `os.environ.get("HERMES_HOME") or str(get_hermes_home())` |
| `hermes_cli/env_loader.py` | 310 | 同上 |
| `hermes_cli/slack_cli.py` | 264 | 同上 |
| `mcp_serve.py` | 69,202,457 | 同上 |
| `agent/file_safety.py` | 16,25 | `get_hermes_home()` |
| `agent/secret_sources/_cache.py` | 85 | `Path(os.getenv("HERMES_HOME") or get_hermes_home())` |
| `agent/transports/codex_app_server.py` | 110 | `os.environ.get("HERMES_HOME") or str(get_hermes_home())` |
| `tools/hook_output_spill.py` | 125 | 同上 |
| `tools/mcp_oauth.py` | 144 | 同上 |
| `gateway/run.py` | 8010,8029 | 同上 |
| `tui_gateway/server.py` | 13083 | `get_hermes_home() / "desktop-attachments"` |
| 插件/optional-skills（22 处） | — | 相同模式，逐个加 import |

#### 3.2d 存量迁移层（可选，推荐）

```python
def _get_platform_default_hermes_home() -> Path:
    new_path = Path.home() / ".xhermes"
    old_path = Path.home() / ".hermes"
    if not new_path.exists() and old_path.exists():
        try:
            old_path.rename(new_path)
        except OSError:
            pass
    return new_path
```

验证：`python -c "from hermes_constants import get_hermes_home; print(get_hermes_home())"` → `~/.xhermes`

### 3.3 Phase 3：进程身份与服务名

| 位置 | 改动 |
|---|---|
| `gateway/run.py:3005-3029` `_resolve_hermes_bin()` | `shutil.which("xhermes")`；fallback `-m hermes_cli.main` 不变 |
| `hermes_cli/relaunch.py:117` | `shutil.which("xhermes")` |
| `gateway/status.py:407,414` | basename：`hermes`→`xhermes`、`hermes.exe`→`xhermes.exe`、`hermes-gateway`→`xhermes-gateway` |
| `hermes_cli/dashboard_procs.py:57-65` | `"hermes dashboard"`→`"xhermes dashboard"`、`"hermes serve"`→`"xhermes serve"`、`"hermes_cli.main dashboard"`→不变（模块名保留） |
| `hermes_cli/gateway.py` tmux session（6498,6805,6858,6977,7332） | `hermes` → `xhermes` |
| `gateway/status.py:37` `_GATEWAY_KIND` | `"hermes-gateway"` → `"xhermes-gateway"` |
| `cron/lifecycle_guard.py:55-60` launchd label | `ai.hermes.gateway` → `ai.xhermes.gateway` |
| `plugins/kanban/systemd/hermes-kanban-dispatcher.service` | 文件名 + `ExecStart=hermes kanban daemon` → `xhermes kanban daemon` |
| `hermes_cli/main.py:227,250`（hermes.exe 更新隔离） | `hermes` → `xhermes` |
| 用户提示串 `"hermes gateway restart"` 等（~12 处） | → `"xhermes gateway restart"` |

### 3.4 Phase 3 补充：profile wrapper 目录（新发现）

`hermes_cli/profiles.py:294-296`：

```python
def _get_wrapper_dir() -> Path:
    return Path.home() / ".local" / "bin"    # 共享目录 → 冲突
```

**问题**：hermes 和 xhermes 若建同名 profile（如 `coder`），wrapper 文件 `~/.local/bin/coder` 互相覆盖；且 wrapper 内容硬编码 `hermes`（`:461,469-470`），xhermes 的 wrapper 会错调 hermes。

**解法**：

```python
# xhermes
def _get_wrapper_dir() -> Path:
    return Path.home() / ".xhermes" / "bin"

# wrapper 内容 (profiles.py:461,470)
hermes_exe = shutil.which("xhermes") or "xhermes"   # 原 "hermes"
```

### 3.5 Phase 3 补充：默认端口错开（新发现）

多个服务有默认端口，同时跑会抢端口：

| 服务 | 原默认端口 | xhermes 改为 | 位置 |
|---|---|---|---|
| `serve` / `dashboard` | 9119 | **9219** | `web_server.py:17061`、`main.py:7264` |
| api_server 平台 | 8642 | **8742** | `web_server.py:2789` |
| webhook 平台 | 8644 | **8744** | `web_server.py:2788` |
| wecom_callback | 8645 | **8745** | `web_server.py:2792` |
| msgraph_webhook / line | 8646 | **8746** | `web_server.py:2790,2796` |

**注**：桌面端 spawn 时用 `--port 0`（OS 分配），不受影响；此改动针对 CLI 手动启动默认值。

### 3.6 Phase 4：安装脚本 / systemd / Docker / 桌面端

| 位置 | 改动 |
|---|---|
| `scripts/install.sh:181,193,397-447` | 安装目录 → xhermes 专用；`.hermes-bootstrap-complete` → `.xhermes-bootstrap-complete` |
| `install.ps1` 对应 | 同上 |
| `scripts/run_tests.sh:54` | `~/.hermes/hermes-agent/venv` → `~/.xhermes/xhermes/venv` |
| `docker-compose.yml:37,71` + `docker-compose.windows.yml:18,31` | 卷挂载 `~/.hermes` → `~/.xhermes` |
| `apps/desktop/electron/backend-command.ts:18` | 前导命令 → `xhermes serve` |
| `apps/desktop/electron/main.ts` | bootstrap marker、`hermes serve` 引用 |
| `apps/desktop/electron/remote-lifecycle.test.ts:118` | 测试路径 `hermes-agent/venv/bin/hermes` |

### 3.7 Phase 5：前端包名与品牌

| 位置 | 改动 | 优先级 |
|---|---|---|
| `ui-tui/package.json:2` | `hermes-tui` → `xhermes-tui` | 高 |
| `ui-tui/packages/hermes-ink/package.json:2` | `@hermes/ink` → `@xhermes/ink` | 高 |
| `apps/shared/package.json:2` | `@hermes/shared` → `@xhermes/shared` | 高（先改它再改依赖方） |
| `apps/bootstrap-installer/package.json:2` | `@hermes/bootstrap-installer` → `@xhermes/bootstrap-installer` | 中 |
| `apps/desktop/package.json:2-3` | name `hermes`→`xhermes`，productName `Hermes`→`xHermes` | 中 |
| `apps/desktop/package.json:174-181` | appId `com.nousresearch.hermes` → `com.<you>.xhermes` | 高（bundle id 不同才能共存） |
| `hermes_cli/skin_engine.py:277-537` | 品牌文案 → xHermes | 低 |
| `run_agent.py:303` 等 User-Agent | `HermesAgent/` → `xHermesAgent/` | 中 |
| `locales/*.yaml`（~20 文件） | `hermes kanban` 等文案 | 低 |
| 文档 URL（~25 处） | → 自己的域名 | 低 |

---

## 4. 已确认安全、无需改动的项

| 项 | 结论 |
|---|---|
| `~/.hermes` vs `~/.xhermes` | ✅ 状态文件全隔离（state.db、config.yaml、auth.json、agent.log 都在 home 下） |
| `gateway.pid` / cron `.tick.lock` / kanban.db | ✅ 都在 home 下，隔离 |
| `active_profile` | ✅ 在 home 下，隔离 |
| `HERMES_HOME` env 兼容层 | ✅ 子进程传 `HERMES_HOME`，fallback 读到自身 home |
| 内部模块名（373 处 import） | ✅ 零改动，merge 上游干净 |
| `toolsets.py` 的 `hermes-telegram` 等工具集 key | ✅ 配置隔离在各自 config.yaml，不冲突 |

---

## 5. 同步策略（长期保持）

### 5.1 分支布局

```
upstream/main  (NousResearch/hermes-agent)  ── 只读，定期 fetch
   │
main (xhermes)  ──────────────────────────── 基于上游 + 改名 patch
```

### 5.2 同步流程

```bash
git fetch upstream
git checkout main
git merge upstream/main          # 改名文件极少，冲突可控
# 若 Phase 2 的 ~15 个 fallback 文件有上游改动 → 手动重放小 diff
git commit
```

### 5.3 冲突最小化技巧

1. 改名的文件集中在少数几个（pyproject.toml、hermes_constants.py、gateway/status.py、gateway/run.py 的 3 行、hermes_cli/gateway.py 的 tmux 名）
2. 内部模块名零改动 → 373 个 import 文件 merge 永远干净
3. `HERMES_HOME` 兼容层保留 → 上游新增的 spawn 点写 `HERMES_HOME` 仍能工作
4. 上游更新 `_resolve_hermes_bin()` 或新增 `shutil.which("hermes")` 时，merge 后手动把 `"hermes"` 改回 `"xhermes"`（一处）

---

## 6. 实施顺序与验证

| 阶段 | 内容 | 验证 |
|---|---|---|
| Phase 1 | 发行/入口 | `which xhermes && xhermes --version` |
| Phase 2 | 家目录/env | `get_hermes_home() == ~/.xhermes` |
| Phase 3 | 进程/服务/wrapper/端口 | 双实例同跑互不干扰；wrapper 指向 xhermes；端口不冲突 |
| Phase 4 | 安装/桌面 | 全新机器安装 + 桌面端启动成功 |
| Phase 5 | 前端/品牌 | 各前端 build 通过 |
| Phase 6 | 同步机制 | `git merge upstream/main` 无致命冲突 |

每阶段独立提交，可单独回滚。

---

## 7. 风险清单

| 风险 | 缓解 |
|---|---|
| **同 venv 安装 → site-packages 文件覆盖** | 硬性要求独立 venv，文档明确禁止混装 |
| `HERMES_HOME` 环境变量被用户全局设置 | 兼容层优先 XHERMES_HOME；文档说明 |
| 存量 `~/.hermes` 数据迁移失败 | 迁移层 try/except，失败不阻塞启动 |
| 前端 `@hermes/shared` 改动波及三方 | 从 shared → 依赖方顺序改，每步 build |
| 上游改动命中改名文件 | 改名集中在少数文件，merge 冲突可控 |
| 桌面端 `backend-command.ts` 写死命令 | 统一改为从环境/配置读 `xhermes` |

---

## 8. 验收标准

1. `xhermes --version` 与 `hermes --version` 各自正常
2. 两套独立 venv 并存，site-packages 无交叉
3. `get_hermes_home()` 在 xhermes 下返回 `~/.xhermes`
4. `xhermes` 与 `hermes` 同时运行 gateway/serve 端口互不冲突
5. profile wrapper 各自指向自己的命令
6. `git merge upstream/main` 无致命冲突
