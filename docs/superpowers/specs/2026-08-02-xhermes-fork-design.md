# xhermes-agent Fork 设计文档

- **日期**: 2026-08-02
- **状态**: 待审核（已按评审修订）
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
- **不新增产品能力**；仅为共存做表面隔离（路径 / 命令 / 端口 / 服务名 / 包名）。端口与服务名改动属于隔离必需，不算新功能
- 不做品牌彻底重塑（`xHermes` 浅层品牌，皮肤/文案可后改）
- **不保证**同一 messaging bot token / 同一平台账号可被两套 agent 同时占用（平台 API 侧单连接限制，见 §2.4）

### 1.3 决策记录

| 决策 | 选择 | 理由 |
|---|---|---|
| 共存模型 | **模型 A：最小隔离** | 只改外部表面，内部模块名保留 |
| 上游同步 | **保留** | 定期 merge upstream + 可重放 rename overlay |
| 存量迁移 | **禁止静默 rename** | rename `~/.hermes`→`~/.xhermes` 会破坏已装 hermes，与共存目标冲突 |
| 产品更新 | **origin=fork；upstream=Nous 只读** | `xhermes update` 不得盲拉上游覆盖改名 patch |
| 改名维持 | **可重放 overlay / 单点常量优先** | 散落手改长期必漂 |

### 1.4 命名约定（全文档统一）

| 概念 | hermes | xhermes |
|---|---|---|
| PyPI / 发行名 | `hermes-agent` | `xhermes-agent` |
| CLI | `hermes` | `xhermes` |
| 家目录 (POSIX) | `~/.hermes` | `~/.xhermes` |
| 家目录 (Windows) | `%LOCALAPPDATA%\hermes` | `%LOCALAPPDATA%\xhermes` |
| 代码安装子目录 | `$HOME/hermes-agent` | `$HOME/xhermes-agent` |
| 完整默认安装树 | `~/.hermes/hermes-agent` | `~/.xhermes/xhermes-agent` |
| systemd unit 基名 | `hermes-gateway` | `xhermes-gateway` |
| launchd label | `ai.hermes.gateway` | `ai.xhermes.gateway` |
| Electron appId | `com.nousresearch.hermes` | `com.<you>.xhermes` |

---

## 2. 共存机制（核心）

### 2.1 隔离边界总览

xhermes 与 hermes 的隔离依赖 **4 个正交维度**，缺一不可：

| 维度 | hermes | xhermes | 隔离机制 |
|---|---|---|---|
| Python 环境 | 自己的 venv | **独立 venv** | site-packages 永不相交 |
| 家目录 | `~/.hermes` | `~/.xhermes` | 所有状态文件隔离 |
| 命令行 | `hermes` | `xhermes` | PATH 无冲突 |
| 进程/服务名 | `hermes-*` | `xhermes-*` | 互不误杀 / 互不抢 unit |

### 2.2 致命约束：Python 包文件级冲突（必须在 venv 层面隔离）

**问题**：模型 A 保留内部模块名（`hermes_cli`、`agent`、`tools`、`gateway`、`hermes_state`…）。若两个发行版装进**同一个 venv**，pip 安装时同名同路径文件**后者覆盖前者**，互相破坏。site-packages 中不可能共存两个同名包。

**解法（硬性要求）**：

```bash
# 正确：xhermes 永远使用独立 venv（推荐装在家目录下）
python3 -m venv ~/.xhermes/xhermes-agent/venv
~/.xhermes/xhermes-agent/venv/bin/pip install -e /path/to/xhermes
~/.xhermes/xhermes-agent/venv/bin/xhermes --version

# 或 pipx 隔离
pipx install --python python3.11 /path/to/xhermes
```

**禁止**：`pip install` 装进 hermes 的 venv / 系统 site-packages。

**效果**：命令行 `xhermes` 指向 xhermes venv 的 bin，`hermes` 指向 hermes venv 的 bin，各自装各自的包，永不相交。

### 2.3 环境变量策略

内部代码大量读取 `HERMES_*`（`HERMES_HOME`、`HERMES_BIN`、`HERMES_DASHBOARD_PORT`、`HERMES_GATEWAY_*` 等）。完整改名为 `XHERMES_*` 会严重破坏上游同步，因此：

| 变量 | 策略 |
|---|---|
| `XHERMES_HOME` | **新增**；解析 home 时优先于 `HERMES_HOME` |
| `HERMES_HOME` | 保留；xhermes 向子进程传播时仍写此名（兼容内部 500+ 读取点） |
| 其他 `HERMES_*` | **不批量改名**；安装脚本与文档明确：**勿在 shell profile 全局导出** `HERMES_HOME` / `HERMES_BIN` / `HERMES_DASHBOARD_PORT` 等 |
| 安装脚本 | 只写入 xhermes 自己的 wrapper / systemd `Environment=`，不写用户 `~/.bashrc` 全局 `export HERMES_HOME=...` |

进程级 environ 天然隔离；真正的串扰来自用户全局导出——用文档 + 安装约束缓解，而不是改遍所有 env 名。

### 2.4 运营级不共存项（本地隔离解决不了）

以下即使四维本地隔离完整，仍不能同机双开共用同一凭证：

- 同一 Telegram / Discord / Slack / 飞书 bot token（平台侧单连接 / token lock）
- 同一 WhatsApp session / 同一 webhook 公网回调 URL（若未分 path/域名）
- 同一浏览器 CDP 端口（默认 9222）被两边 browser 工具同时占用

验收与用户文档必须写明：**换 bot / 换端口 / 分配置**，不要假设"装了两套就能共享同一个 bot"。

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

**关键**：入口函数路径 `hermes_cli.main:main` **不变**（模块未改名），因此 `-m hermes_cli.main` 的子进程 spawn 无需改动。

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

#### 3.2b 环境变量兼容层

```python
def _hermes_home_from_env() -> Path:
    # xhermes: 优先 XHERMES_HOME，兼容遗留 HERMES_HOME
    val = os.environ.get("XHERMES_HOME", "").strip() \
        or os.environ.get("HERMES_HOME", "").strip()
    if val:
        return Path(val)
    return _get_platform_default_hermes_home()
```

**原理**：xhermes 进程把自身 home 传给子进程时仍写 `HERMES_HOME` → 子进程读 `HERMES_HOME` fallback 成功。内部大量 `os.environ.get("HERMES_HOME", ...)` 全部保留不动。

#### 3.2b-1 关键例外：`get_default_hermes_root()` 必须同步加 `XHERMES_HOME` 优先

`hermes_constants.py` 的 `get_default_hermes_root()`（约 L161-198）**直接读** `os.environ.get("HERMES_HOME", "")`（L179 附近），**不经过** `_hermes_home_from_env()`。若只改 `_hermes_home_from_env()` 而漏改此处，当 `HERMES_HOME=~/.hermes` 泄漏进 xhermes 进程环境时：

1. `native_home = ~/.xhermes`，`env_home = ~/.hermes`
2. `~/.hermes` 不在 `~/.xhermes` 下 → `relative_to` 抛 `ValueError`
3. `~/.hermes`.parent.name 是用户名而非 `"profiles"` → 不走 profile 分支
4. **返回 `~/.hermes`** → xhermes 把 hermes 的 home 当作自身 root

此函数用于 `profile list` 等 profile 级操作，错配会直接违反"互不干扰"目标。§3.2c 笼统说"优先 `get_default_hermes_root()`"，但该函数本身就是漏洞点，必须先修。

**改法**：`get_default_hermes_root()` 解析 env 时同样走 `XHERMES_HOME` 优先逻辑（或统一改用 `_hermes_home_from_env()`），确保即使 `HERMES_HOME` 泄漏也不会指向 hermes home。同理排查 `_profile_home_path()`（L830 附近，亦直接读 `HERMES_HOME`）等绕过 `_hermes_home_from_env()` 的读取点。

验证：`XHERMES_HOME` unset 且 `HERMES_HOME=~/.hermes` 时，`get_default_hermes_root()` 应返回 `~/.xhermes`（native_home），**而非** `~/.hermes`。

#### 3.2c 硬编码 `~/.hermes` fallback 替换

统一改法：优先 `get_hermes_home()` / `get_default_hermes_root()`；早期初始化处用 `Path.home() / ".xhermes"`（Windows：`LOCALAPPDATA/xhermes`）。

**范围说明**：仓库内字符串/`~/.hermes` 出现点远多于"十几处"。实施时以**运行时路径解析**为准，用搜索清单驱动，不依赖固定行号。至少覆盖：

| 类别 | 代表位置 | 改法 |
|---|---|---|
| CLI 早期 fallback | `hermes_cli/main.py`（config / profiles / desktop-ssh 等） | `.hermes` → `.xhermes` 或 `get_hermes_home()` |
| Auth seatbelt | `hermes_cli/auth.py`（pytest 防护写死 `~/.hermes/auth.json`） | 改为 `~/.xhermes`（测试防护目标随 fork 变） |
| 文件安全 / 沙箱路径段 | `agent/file_safety.py`（镜像路径中的 `.hermes` 段） | 段名改为 `.xhermes`，并与沙箱 home 布局一致 |
| 网关 / TUI / MCP | `gateway/run.py`、`tui_gateway/server.py`、`mcp_serve.py` | `get_hermes_home()` |
| Node bridge | WhatsApp bridge 等 JS 中 `path.join(HOME, '.hermes', ...)` | `.xhermes` |
| 插件 / optional-skills | 各硬编码 fallback | 同模式 |
| 项目插件目录 | `hermes_cli/plugins.py` 扫描 `./.hermes/plugins/` | 改为 `./.xhermes/plugins/`（避免与同 cwd 的 hermes 项目插件树共享） |

实施前跑一次全库检索并归档清单（可进 overlay 脚本输入）：

```bash
rg -n "Path\.home\(\)\s*/\s*[\"']\.hermes|[\"']\.hermes[\"']|~/\\.hermes|LOCALAPPDATA.*hermes" \
  --glob '!docs/**' --glob '!**/*.md'
```

#### 3.2d 存量迁移（显式、可选；禁止静默 rename）

**禁止**在启动路径里 `Path.rename(~/.hermes → ~/.xhermes)`——同机已装 hermes 时会直接搬走对方数据，与共存目标冲突。

| 方案 | 行为 | 何时用 |
|---|---|---|
| **默认** | 始终使用空的 `~/.xhermes`；不碰 `~/.hermes` | 同机共存 / 全新安装 |
| **可选** | `xhermes migrate --from-hermes`：**copy**（非 rename）选定子集到 `~/.xhermes` | 用户明确要从 hermes 拷配置/技能 |
| **禁止** | 静默 rename / 无确认覆盖 | — |

`migrate` 命令行为约束：
1. 目标已存在且非空 → 拒绝，除非 `--force`
2. 默认只 copy：`config.yaml`、`.env`（提示含密钥）、`skills/`、`SOUL.md`；**不** copy 正在运行的 `state.db` / gateway pid / lock，除非用户加 `--include-state`
3. 完成后打印："hermes 的 `~/.hermes` 未改动；两套可继续并存"

验证：`python -c "from hermes_constants import get_hermes_home; print(get_hermes_home())"` → `~/.xhermes`  
且：同机存在 `~/.hermes` 时启动 xhermes **不修改**该目录。

### 3.3 Phase 3：进程身份与服务名（全集）

进程发现、systemd、launchd、tmux、提示串必须成套改，否则会误停/误启对方服务。

| 位置 | 改动 |
|---|---|
| `gateway/run.py` `_resolve_hermes_bin()` | `shutil.which("xhermes")`；fallback `-m hermes_cli.main` 不变 |
| `hermes_cli/relaunch.py` | `shutil.which("xhermes")` |
| `gateway/status.py` basename / `_GATEWAY_KIND` | `hermes`→`xhermes`、`hermes-gateway`→`xhermes-gateway`；kind 同改 |
| `hermes_cli/gateway.py` `_SERVICE_BASE` | `"hermes-gateway"` → `"xhermes-gateway"`（`get_service_name()` 中枢） |
| `hermes_cli/gateway.py` launchd | `ai.hermes.gateway` → `ai.xhermes.gateway`（含 profile 后缀形态） |
| `hermes_cli/gateway.py` tmux session 名 | `hermes` → `xhermes` |
| `hermes_cli/gateway.py` legacy unit 白名单 / planned-restart | 同步改为 `xhermes-*`，避免误匹配上游 unit |
| `hermes_cli/dashboard_procs.py` | `"xhermes dashboard"` / `"xhermes serve"`；systemd：`xhermes-dashboard.service` / `xhermes-serve.service` |
| `cron/lifecycle_guard.py` | launchd / systemctl 防护串中的 label/unit |
| `plugins/kanban/systemd/*.service` | 文件名 + `ExecStart=xhermes kanban daemon` |
| `hermes_cli/main.py`（exe 更新隔离） | `hermes.exe` → `xhermes.exe` 等 |
| 用户提示串 | `"hermes gateway restart"` → `"xhermes gateway restart"` 等 |
| **`scripts/hermes-gateway`（独立服务脚本）** | `SERVICE_NAME="hermes-gateway"`→`"xhermes-gateway"`；plist 路径 `ai.hermes.gateway.plist`→`ai.xhermes.gateway.plist`；plist 内 `<string>ai.hermes.gateway</string>`→`ai.xhermes.gateway`；所有 `launchctl ... ai.hermes.gateway` 命令；**venv 路径 `PROJECT_DIR/venv`→`~/.xhermes/xhermes-agent/venv`**；或标记为 fork 不发行（若 xhermes 统一走 `xhermes gateway` CLI 路径） |

> **`ai.hermes.gateway` 标签额外出现点**（§3.3 表自称"全集"，以下必须覆盖，否则 launchctl 检测/防护失配；§5.4 checklist regex `ai\.hermes\.` 可在合并后兜底扫描，但实施时应前置覆盖）：

| 文件 | 位置 | 用途 |
|---|---|---|
| `gateway/restart_loop_guard.py` | L7 | 重启循环防护的 launchctl 命令 |
| `gateway/run.py` | L10213 | 网关运行时 launchctl 逻辑 |
| `tools/approval.py` | L760 | 审批工具直接比对 service label 字符串（漏改会导致审批的 launchctl 检测失配） |
| `hermes_cli/config_defaults.py` | L2460 | 配置默认值中的 launchctl 防护说明 |
| `hermes_cli/gateway.py` | L3648 | `get_launchd_label()` 第二处生成点（除 L2488 外，profile 后缀形态） |

### 3.4 Phase 3 补充：profile wrapper 目录

`hermes_cli/profiles.py`：

```python
def _get_wrapper_dir() -> Path:
    return Path.home() / ".local" / "bin"    # 共享目录 → 冲突
```

**问题**：同名 profile（如 `coder`）会互相覆盖 `~/.local/bin/coder`；wrapper 内容硬编码 `hermes` 会错调对方。

**解法**：

```python
def _get_wrapper_dir() -> Path:
    return Path.home() / ".xhermes" / "bin"

# wrapper 内容
hermes_exe = shutil.which("xhermes") or "xhermes"
```

安装/文档提示用户把 `~/.xhermes/bin` 加入 PATH（可排在 hermes 之后或之前，互不影响文件名空间）。

### 3.5 Phase 3 补充：默认端口错开

**端口全集**（`_PORT_BINDING_PLATFORM_PORTS` + 各平台 `DEFAULT_PORT`）：

| 监听方 | 原默认端口 | xhermes 改为 | 位置 |
|---|---|---|---|
| `serve` / `dashboard` | 9119 | **9219** | `web_server.py`、`main.py` |
| api_server 平台 | 8642 | **8742** | `web_server.py`、`api_server.py` |
| webhook 平台 | 8644 | **8744** | `web_server.py`、`webhook.py` |
| wecom_callback | 8645 | **8745** | `web_server.py` |
| msgraph_webhook / line | 8646 | **8746** | `web_server.py` |
| feishu webhook | 8765 | **8865** | `web_server.py` |
| bluebubbles | 8645 | **8745** | `web_server.py` |
| sms | 8080 | **8180** | `web_server.py` |
| whatsapp_cloud | 8090 | **8190** | `web_server.py` |
| browser CDP（若 fork 默认拉起） | 9222 | **9322**（或文档要求显式配置） | browser / CDP 相关默认 |

**冲突判定**：
- ✅ 桌面端 spawn 常用 `--port 0`（OS 分配）→ 无固定端口冲突
- ✅ Telegram/Discord/飞书(WS) 等出站长连接 → 无本地监听冲突
- ⚠️ CLI 手动启动 + webhook 类平台 → 靠默认端口错开；用户自定义端口仍可能撞
- ⚠️ 同一 bot token → 见 §2.4，与端口无关

xhermes 有独立 `config.yaml`；改默认值是为了**开箱不冲突**。

### 3.6 Phase 4：安装脚本 / Docker / 桌面端

安装树必须闭合为 `~/.xhermes/xhermes-agent`（venv、bootstrap marker、desktop `ACTIVE_*_ROOT` 一致）。

| 位置 | 改动 |
|---|---|
| `scripts/install.sh` | `HERMES_HOME` 默认 `~/.xhermes`；`INSTALL_DIR=$HERMES_HOME/xhermes-agent`；命令链到 `xhermes`；bootstrap `.xhermes-bootstrap-complete`；**REPO_URL 指向你的 fork** |
| `install.ps1` | 同上（Windows：`%LOCALAPPDATA%\xhermes\xhermes-agent`） |
| `scripts/run_tests.sh` | 共享 venv 探针 → `~/.xhermes/xhermes-agent/venv` |
| `docker-compose*.yml` | 卷 `~/.xhermes`；镜像/容器名避开 `hermes-*`；端口映射用 9219 等 |
| `apps/desktop/electron/main.ts` | home → `.xhermes`；`ACTIVE_HERMES_ROOT = .../xhermes-agent`；bootstrap marker；`setAppUserModelId('com.<you>.xhermes')` |
| `apps/desktop/electron/backend-command.ts` | `xhermes serve` |
| `apps/desktop/electron/*.test.ts` | 路径断言改为 `xhermes-agent` / `xhermes` |
| Electron `userData` / 更新 marker | 随 appId / home 隔离；`.hermes-update-in-progress` → `.xhermes-update-in-progress`（若仍落在 home 下） |

### 3.7 Phase 5：前端包名与品牌

| 位置 | 改动 | 优先级 |
|---|---|---|
| `ui-tui/package.json` | `hermes-tui` → `xhermes-tui` | 高 |
| `ui-tui/packages/hermes-ink/package.json` | `@hermes/ink` → `@xhermes/ink` | 高 |
| `apps/shared/package.json` | `@hermes/shared` → `@xhermes/shared` | 高（先改它再改依赖方） |
| `apps/bootstrap-installer/package.json` | `@hermes/bootstrap-installer` → `@xhermes/bootstrap-installer` | 中 |
| `apps/desktop/package.json` | name / productName / **appId** | 高（bundle id 不同才能共存） |
| `hermes_cli/skin_engine.py` | 品牌文案 → xHermes | 低 |
| User-Agent | `HermesAgent/` → `xHermesAgent/` | 中 |
| `locales/*.yaml` | CLI 提示中的命令名 | 低 |
| 文档 / Portal URL | 按需换成自有域名；OAuth redirect 若用官方 portal 需单独评估 | 低 |

### 3.8 Phase 6：产品内更新（`xhermes update`）

`hermes_cli/update_cmd.py` 中：

```text
OFFICIAL_REPO_URL = "https://github.com/NousResearch/hermes-agent.git"
```

若保留为 Nous，则 `xhermes update` 会按上游树覆盖 fork 改名，与"保留上游同步"的**人工 merge 模型**冲突。

**规定**：

| 远程 | 角色 |
|---|---|
| `origin` | 你的 xhermes fork（`xhermes update` 默认拉这个的 release/branch） |
| `upstream` | `NousResearch/hermes-agent`（只读；**不**作为 `update` 的默认 pull 源） |

行为：
1. 改 `OFFICIAL_REPO_URL(S)` / fork 检测逻辑，使"官方"对 xhermes 而言是你的 fork
2. 上游同步走 §5 的 `git fetch upstream && merge` + overlay，**不**由日常 `xhermes update` 完成
3. 文档区分两套命令：`xhermes update`（吃自己的发布）vs `scripts/sync_upstream.sh`（吃 Nous + 重放改名）

---

## 4. 已确认安全 / 需注意的项

| 项 | 结论 |
|---|---|
| `~/.hermes` vs `~/.xhermes` | ✅ 状态文件全隔离（在**禁止静默 rename**前提下） |
| `gateway.pid` / cron `.tick.lock` / kanban.db | ✅ 都在 home 下 |
| `active_profile` | ✅ 在 home 下 |
| 内部模块名 | ✅ 零改动，业务 import merge 干净 |
| `toolsets.py` 的 `hermes-telegram` 等 key | ✅ 配置在各自 config.yaml |
| 项目插件 `./.xhermes/plugins/` | ⚠️ 必须与 hermes 的 `./.hermes/plugins/` 错开（见 3.2c） |
| 全局 `HERMES_*` 用户导出 | ⚠️ 文档约束，见 §2.3 |
| 同一 bot token | ❌ 不共存，见 §2.4 |

### 4.1 四维度审计

| 维度 | 是否冲突 | 措施 |
|---|---|---|
| 监听端口 | ⚠️ 部分 | 默认端口错开（§3.5）；CDP 纳入表或文档 |
| 日志路径 | ✅ | `{home}/logs/` |
| 环境变量 | ⚠️ | `XHERMES_HOME` 优先；禁止安装脚本全局 export |
| 桌面端 | ⚠️ | home + `xhermes-agent` 子目录 + appId + AppUserModelId 成套改 |
| systemd/launchd | ⚠️ | `_SERVICE_BASE` / dashboard / serve / kanban 成套改（§3.3） |

---

## 5. 同步策略（长期保持）

### 5.1 分支布局

```
upstream/main  (NousResearch/hermes-agent)  ── 只读，定期 fetch
   │
main (xhermes)  ──────────────────────────── 上游 + rename overlay / 改名提交
```

### 5.2 推荐：可重放 rename overlay

散落手改在上游频繁改 install/gateway/desktop 时必漂。采用其一（可组合）：

| 方案 | 优点 | 缺点 |
|---|---|---|
| **A. 单点常量**（如 `PRODUCT_SLUG=xhermes`、`CLI_NAME`、`HOME_DIRNAME`）集中读取 | 同步成本最低 | 初次需把散落字面量收拢 |
| **B. `scripts/apply_xhermes_overlay.sh`** 在干净上游树上重放替换 | merge 上游最干净 | 要维护脚本与清单 |
| C. 仅散落手改 + merge 后人工修 | 起步快 | **不推荐**作长期方案 |

默认推荐：**A 为主，B 作补充**（install/desktop 等难收拢的字面量进 overlay 清单）。

### 5.3 同步流程

```bash
git fetch upstream
git checkout main
git merge upstream/main
# 若采用 overlay：scripts/apply_xhermes_overlay.sh
# 跑双实例 smoke（§8）
git commit   # 若有冲突解决 / overlay 结果
```

### 5.4 合并后 checklist（必跑）

1. `rg -n 'shutil\.which\("hermes"\)|_SERVICE_BASE = "hermes|SERVICE_NAME = "hermes|ai\.hermes\.|~/\.hermes/hermes-agent|OFFICIAL_REPO_URL.*NousResearch'` 在**产品路径**上应无残留（测试里的历史字符串另议）
2. 默认端口表仍为 xhermes 偏移值
3. `get_hermes_home()` → `~/.xhermes`；安装树 → `~/.xhermes/xhermes-agent`
4. `xhermes update` 的 origin/官方 URL 仍指向 fork，不是 Nous
5. 双实例 smoke（§8）通过

### 5.5 冲突预期（务实）

以下文件**会反复冲突**，不要假设"极少"：`pyproject.toml`、`hermes_constants.py`、`hermes_cli/gateway.py`、`gateway/status.py`、`hermes_cli/update_cmd.py`、`scripts/install.sh`、`apps/desktop/**`、端口常量、locales。业务模块（`agent/`、`tools/` 大部）因未改模块名，merge 通常干净。

---

## 6. 实施顺序与验证

| 阶段 | 内容 | 验证 |
|---|---|---|
| Phase 1 | 发行/入口 | `which xhermes && xhermes --version` |
| Phase 2 | 家目录/env；**无静默迁移** | `get_hermes_home()==~/.xhermes`；存在 `~/.hermes` 时不被改动 |
| Phase 3 | 进程/服务/wrapper/端口 | unit 名、which、端口双开 |
| Phase 4 | 安装/桌面/Docker | 安装树 `~/.xhermes/xhermes-agent`；桌面启动 |
| Phase 5 | 前端/品牌/appId | build 通过；与 Hermes.app 可并存 |
| Phase 6 | update + upstream sync | `xhermes update` 吃 fork；`sync_upstream` + overlay + smoke |

每阶段独立提交，可单独回滚。

---

## 7. 风险清单

| 风险 | 缓解 |
|---|---|
| 同 venv 安装 → site-packages 覆盖 | 硬性独立 venv；文档禁止混装 |
| 静默 rename 毁掉 hermes 数据 | **已删除该设计**；仅显式 copy 迁移 |
| `xhermes update` 拉上游覆盖改名 | 官方 URL 改指向 fork；upstream 只读 merge |
| 全局 `HERMES_*` 导出串扰 | 安装不写全局 export；文档警告 |
| systemd/launchd 误操作对方 | `_SERVICE_BASE` 与 unit 全集改名 |
| 安装子目录与 desktop root 不一致 | 统一 `xhermes-agent` 子目录 |
| 上游改动命中改名面 | overlay + 合并后 checklist |
| 同一 bot token 双开失败 | §2.4 写明运营约束 |
| 前端包名波及 | shared → 依赖方顺序改 |
| 测试断言绑死 `.hermes` / `hermes` 文案 | 优先依赖 `HERMES_HOME` tmp 夹具；产品路径字面量随 overlay 更新；避免写死枚举快照 |
| `get_default_hermes_root()` 直接读 `HERMES_HOME` 绕过 `XHERMES_HOME` 优先层 | §3.2b-1：该函数同步走 `XHERMES_HOME` 优先；`HERMES_HOME=~/.hermes` 泄漏时仍返回 `~/.xhermes` |
| `scripts/hermes-gateway` 独立脚本硬编码服务名/label/venv 路径 | §3.3：补入改动全集；或 fork 不发行该脚本 |

---

## 8. 验收标准

### 8.1 功能共存

1. `xhermes --version` 与 `hermes --version` 各自正常
2. 两套独立 venv 并存，site-packages 无交叉
3. `get_hermes_home()` 在 xhermes 下返回 `~/.xhermes`（Windows：`...\xhermes`）
4. 代码树在 `~/.xhermes/xhermes-agent`（或文档约定的等价路径）
5. 同机已有 `~/.hermes` 时，启动 xhermes **不 rename/不删除**该目录
6. `xhermes` 与 `hermes` 同时跑 gateway/serve：默认端口与 systemd/launchd unit 互不冲突
7. profile wrapper 落在 `~/.xhermes/bin`，内容调用 `xhermes`
8. 桌面端 appId / AppUserModelId 与 Hermes 不同，可并列安装

### 8.2 更新与同步

9. `xhermes update` 默认只跟踪 fork（origin），不盲拉 Nous 覆盖改名
10. `git fetch upstream && merge` + overlay 后，§5.4 checklist 通过

### 8.3 双实例 smoke（建议脚本化）

在一台已装 hermes 的机器上：

```bash
# 伪代码级验收步骤
xhermes --version
hermes --version
test "$(xhermes -c 'from hermes_constants import get_hermes_home; print(get_hermes_home())')" = "$HOME/.xhermes"
test -d "$HOME/.hermes" && test -d "$HOME/.xhermes"
# 两端各 start serve/gateway（或 dashboard），确认监听端口不同且互不 kill
# 创建 xhermes profile wrapper，确认 shebang/exec 指向 xhermes
```

---

## 9. 修订记录

| 日期 | 变更 |
|---|---|
| 2026-08-02 | 初稿 |
| 2026-08-02 | 评审修订：删除静默 rename；补全服务名/安装子目录/update 策略/env 策略/运营不共存项；改为 overlay 同步模型；收紧验收 |
| 2026-08-02 | 二次修订（代码核查驱动）：补入 `scripts/hermes-gateway` 独立脚本（硬编码 `SERVICE_NAME`/plist/label/venv 路径）至 §3.3 改动全集；补入 `get_default_hermes_root()` 绕过 `XHERMES_HOME` 的修复要求（§3.2b-1）；补全 `ai.hermes.gateway` 标签 5 个额外出现点；§5.4 checklist regex 增 `SERVICE_NAME` 模式；§7 风险清单增 2 条 |
