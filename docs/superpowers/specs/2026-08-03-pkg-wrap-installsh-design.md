# pkg 包装 install.sh 设计文档

日期：2026-08-03
状态：待评审
关联：`docs/packaging/macos.md`（现状 pkg 方案）、`scripts/install.sh`（被包装对象）、`scripts/build_macos_pkg.sh`（构建脚本）

---

## 1. 背景与动机

现有 macOS pkg（自包含方案，见 `docs/packaging/macos.md`）把 CPython +
site-packages + 精简源码树整体打进 pkg，优点是离线秒装、构建时锁定；但
**更新断裂**——payload 无 `.git`，`detect_install_method` 返回 `unknown`，
`xhermes update` 无法 git pull，升级只能重下 pkg 重装。

本文提出第二种形态：**pkg 退化为薄壳，只携带 `scripts/install.sh`，
postinstall 在安装时执行它完成真正的安装**。安装后布局与 install.sh
完全一致（带 `.git` + `.install_method=git`），`xhermes update` 完整可用，
两条分发路径最终收敛为同一套代码与同一份安装产物。

### 为什么可行

install.sh 已为被包装场景预埋完整机制：

- **`--stage <name>` 分阶段协议**：`prerequisites / repository / venv /
  python-deps / node-deps / path / config / setup / gateway / desktop`，
  每个 stage 独立进程执行、自带 `detect_os`/`resolve_install_layout`，
  可被外部编排器逐个调用
- **`--json` 输出**：每个 stage 输出一行
  `{"ok":bool,"stage":"...","skipped":bool,"reason":"..."}` JSON 结果帧
  （subshell 隔离保证失败也 emit 干净结果）
- **`--manifest`**：一次性输出全部 stage 清单（含 `needs_user_input`）
- **`--skip-setup`**：跳过交互式 setup wizard；`NON_INTERACTIVE` 下
  `setup`/`gateway` 两个交互 stage 自动跳过
- 结尾自动写 `.install_method = git` → 更新机制天然对齐

## 2. 目标与非目标

### 目标

- pkg 安装后 == install.sh 安装后（字节级等价：同一 INSTALL_DIR、同一
  venv、同一命令链接、同一 `.install_method`）
- `xhermes update` 在 pkg 安装的实例上完整可用
- 单一实现：pkg 不再维护自包含 payload 逻辑，只跟 install.sh 演进
- 保留现有 distribution 外壳（`enable_currentUserHome`），GUI 双击可用

### 非目标

- 不解决离线安装（本方案需要网络：git clone + uv + pip + node）
- 不保留自包含 pkg 的「构建时锁定」审计属性
- 不做 universal2 双架构（沿用单 arm64）
- 不替代 install.sh 本身；install.sh 仍是 Linux/macOS/Termux 的官方路径

## 3. 总体架构

```
macOS pkg（薄壳，~1MB）
├── Distribution            ← 现有外壳不变（enable_currentUserHome）
├── payload
│   └── scripts/install.sh  ← 从 repo 打包，版本随构建时 commit
└── Scripts/postinstall     ← 编排器：用户转换 + stage 驱动 + 失败处理
```

pkg 不再携带 Python/依赖/源码树。postinstall 是唯一的编排逻辑：

```
postinstall
 ├─ 1. 解析安装用户（console user / 调用者）
 ├─ 2. 以该用户身份运行 install.sh 的 stage 序列
 │     prerequisites → repository → venv → python-deps
 │     → node-deps（可跳过）→ path → config
 ├─ 3. 跳过 setup / gateway（交互，NON_INTERACTIVE 自动跳过）
 └─ 4. 任一 stage 失败 → 报告失败帧，pkg 安装失败
```

## 4. postinstall 用户转换

### 4.1 场景矩阵

| 安装方式 | postinstall 运行身份 | HOME | 需要转换 |
|---|---|---|---|
| GUI 双击（Installer.app） | root | `/var/root` | ✅ 转换到 console 用户 |
| `installer -pkg ... -target CurrentUserHomeDirectory` | 当前用户 | 用户 home | ❌ 直接用 |

### 4.2 转换逻辑（已有，复用现有 pkg 的 postinstall 骨架）

```sh
# 当前用户路径（命令行方式）：HOME 即用户 home，直接使用
HOME_DIR="$HOME"

# root 路径（GUI 方式）：解析 console 用户并取其 home
if [ "$(id -u)" = "0" ] && [ "$HOME_DIR" = "/var/root" ]; then
    CONSOLE_USER="$(stat -f%Su /dev/console 2>/dev/null)"
    HOME_DIR="$(dscl . -read "/Users/$CONSOLE_USER" NFSHomeDirectory 2>/dev/null | awk '{print $2}')"
fi
```

### 4.3 以用户身份执行 stage

两种机制，按安装方式选择：

- **命令行方式**：postinstall 即用户身份，直接 `bash install.sh --stage X`
- **GUI 方式**：root 需降权到 console 用户：

  ```sh
  # 方式 A（推荐）：launchctl asuser 保留用户 GUI session 环境
  launchctl asuser "$CONSOLE_UID" \
      env HOME="$HOME_DIR" bash "$INSTALL_DIR/scripts/install.sh" --stage "$STAGE"

  # 方式 B：su -l 简单但环境更干净
  su -l "$CONSOLE_USER" -c "bash '$INSTALL_DIR/scripts/install.sh' --stage '$STAGE'"
  ```

  推荐 A（`launchctl asuser`）：保留 Aqua session 的 LaunchServices /
  钥匙串访问，`run_setup_wizard` 之外的 stage 不依赖这些，但 node/浏览器
  相关 stage 在 GUI 环境下行为更接近真实用户操作。

### 4.4 环境隔离

- 显式传 `HOME="$HOME_DIR"`、`TERM=dumb`（无 tty）
- `NON_INTERACTIVE=true`（等价 `--skip-setup`，且 JSON 输出可用）
- `JSON_OUTPUT=true`：stage 结果以 JSON 帧回传
- 避免继承 postinstall 的 root 环境变量（`env -i` 白名单：HOME、PATH、
  TERM、LANG）

## 5. stage 编排

### 5.1 编排序列

| 顺序 | stage | 说明 | 失败处理 |
|---|---|---|---|
| 1 | `prerequisites` | OS 检测、装 uv、check python/git/node/网络、系统包 | 终止 |
| 2 | `repository` | git clone 到 `$HOME_DIR/.xhermes/xhermes-agent` | 终止 |
| 3 | `venv` | 建 `.venv` + uv sync | 终止 |
| 4 | `python-deps` | 核心依赖安装 | 终止 |
| 5 | `node-deps` | 可选：browser 工具链 `npm install` | **跳过可接受**（见 §5.3） |
| 6 | `path` | 建命令链接（`~/.local/bin/xhermes`） | 终止 |
| 7 | `config` | 拷贝 config 模板 | 终止（可告警） |
| — | `setup` | **跳过**（交互；`NON_INTERACTIVE` 自动跳过） | — |
| — | `gateway` | **跳过**（交互；装完后用户自行 `xhermes gateway`） | — |

### 5.2 stage 调用细节

```sh
run_stage() {
    local stage="$1"
    # 单 stage 输出只有 JSON 帧（--json）；失败时 install.sh 返回非零
    # 但 JSON 帧已包含 {ok:false, reason}
    out="$("$INSTALL_DIR/scripts/install.sh" --stage "$stage" --json \
           --non-interactive 2>&1)" || {
        echo "stage $stage failed: $out"
        exit 1
    }
    # 解析 out 中的 ok 字段，false → 失败
}
```

> 注意：`--stage` 的 stage body 在 subshell 中执行，`exit 1` 只退出
> subshell；父进程拿到返回码 + JSON 帧。postinstall 必须以**JSON 帧的
> `ok` 字段**为准，不能只看退出码（stage body 的 helper 可能 set -e
> 提前终止 subshell 但 JSON 帧仍 emit）。

### 5.3 node-deps 的取舍

- `node-deps` 需要网络 + npm registry（`npm install --silent`），且
  Playwright 浏览器下载更重
- CLI 纯文本会话**不需要** node（browser 工具运行时 lazily 自装 node +
  playwright，见 `hermes_constants.py` managed-node 机制）
- **推荐：编排时跳过 node-deps**（`--skip-browser`，install.sh 原生参数），
  把选择权留给用户首次使用 browser 工具时；文档标注此差异

## 6. 失败处理

### 6.1 失败语义

- **任一非可选 stage 失败** → postinstall `exit 1` → pkg 安装失败，
  Installer 报「安装失败」
- 失败时已完成的 stage 产物保留（源码树/venv），便于用户手动重试：
  `cd ~/.xhermes/xhermes-agent && bash scripts/install.sh --stage X` 续跑
- postinstall 捕获每个 stage 的 JSON 帧，失败原因（`reason` 字段）写入
  `$HOME_DIR/.xhermes/install-error.log` 便于诊断

### 6.2 半安装状态的识别

半安装（clone 成功但 venv/依赖失败）时目录里**已有 `.git`**——
`detect_install_method` 会返回 `git`，`xhermes update` 可能把半装当完整。
处理：

- install.sh 在**全部 stage 完成后**才写 `.install_method = git`
  （现有行为：`main()` 末尾写）——半装时无此 stamp
- postinstall 失败时**补写** `.install_method = pkg-incomplete`（新值），
  让 `detect_install_method` 返回非 `git`，`xhermes update` 拒绝执行并
  提示先完成安装
- `detect_install_method` 需新增对该值的处理（见 §8 代码变更）

### 6.3 网络/中断

- 安装中拔网 / 断网：stage 超时由 install.sh 内部的
  `run_with_timeout` 处理（npm/下载有 time-box），postinstall 侧加
  总超时（建议 30 分钟）兜底
- Installer 强制中断（用户取消）：postinstall 收到 SIGTERM，清理
  临时文件，保留已下载产物（下次续跑）

## 7. 与现有 distribution 外壳的整合

### 7.1 保留不变的

- `Distribution` XML：`enable_currentUserHome="true"`、
  `enable_localSystem="false"`、`hostArchitectures="arm64"` —— 现有
  system-volume 拦截规避依赖这层，保持不变
- pkg 标识：`com.xhermes.cli`
- 构建入口：`scripts/build_macos_pkg.sh`

### 7.2 变化的

| 项 | 自包含 pkg（现状） | 薄壳 pkg（新方案） |
|---|---|---|
| payload | python + site-packages + 源码树（解压 ~253MB） | `scripts/install.sh`（~40KB） |
| postinstall | shebang 重写 + 符号链接 | stage 编排 + 用户转换 + 失败处理 |
| pkg 体积 | 75MB（压缩后） | ~1MB |
| install-location | `.xhermes/xhermes-agent` | 仍为 `.xhermes/xhermes-agent`（install.sh 的 INSTALL_DIR 落点一致，payload 实际只放 install.sh 一个文件，安装后由 stage 填充目录） |

> 细节：payload 的 install-location 保持 `.xhermes/xhermes-agent`，
> 但薄壳只放 `scripts/install.sh` 一个文件；真正的 `INSTALL_DIR`
> 由 `repository` stage 的 git clone 填充。postinstall 需要先定位
> 该文件（`$HOME_DIR/.xhermes/xhermes-agent/scripts/install.sh`），
> 再以用户身份执行 stage。

### 7.3 构建脚本改动

`scripts/build_macos_pkg.sh` 第 8 步替换为：

```
1. 拷贝 repo 的 scripts/install.sh 到 stage（版本随构建时 commit）
2. 写 postinstall（编排器）
3. pkgbuild → component pkg（install-location .xhermes/xhermes-agent）
4. productbuild → distribution pkg（现有 XML 不变）
```

不再需要：Python 拷贝、req.txt 提取、uv pip install、site-packages、
rsync 源码树、xattr 清理、launcher 生成、shebang 重写。构建时间从分钟
级降到秒级。

## 8. 代码变更清单

| 文件 | 变更 |
|---|---|
| `scripts/build_macos_pkg.sh` | payload 改为只含 install.sh；postinstall 改为编排器；删除自包含逻辑 |
| `scripts/install.sh` | 基本不变；确认 `--skip-browser`/`NON_INTERACTIVE` 在 stage 模式下行为正确 |
| `hermes_cli/config.py` | `detect_install_method` 识别 `pkg-incomplete` 值；`recommended_update_command_for_method` 对 `pkg-incomplete` 返回「重跑安装」提示 |
| `docs/packaging/macos.md` | 增加薄壳方案章节；两方案对比表；node/browser 差异说明 |

## 9. 取舍分析（对照现状）

| 维度 | 自包含 pkg（现状） | 薄壳 pkg（新方案） |
|---|---|---|
| 离线安装 | ✅ | ❌（需网络） |
| 安装时长 | 秒级 | 分钟级 |
| 自动更新 | ❌ 断裂 | ✅ `xhermes update` 完整 |
| 实现唯一性 | 双份（pkg + install.sh） | 单份 |
| 供应链审计 | 构建时锁定 | 安装时点 |
| 体积 | 75MB | 1MB |
| browser 工具 | 需运行时自装 node | 需运行时自装 node（同） |
| 失败恢复 | 重装 | 支持续跑 stage |

**推荐形态**：薄壳为默认。若未来需要离线分发，可给 install.sh 增加
`--offline-bundle` 参数（携带预下载 tarball），pkg 不变——单一实现内
扩展，而非并行两套。

## 10. 验证清单

1. **构建**：`scripts/build_macos_pkg.sh` 产出 ~1MB pkg，无 python/
   site-packages 残留
2. **安装（命令行）**：`installer -pkg x.pkg -target CurrentUserHomeDirectory`
   → postinstall 以用户身份跑全部 stage → 退出 0
3. **安装（GUI）**：双击 → console 用户转换正确，产物属主为该用户
4. **布局等价**：`~/.xhermes/xhermes-agent/` 含 `.git`、
   `.install_method=git`、`.venv`；`~/.local/bin/xhermes` 链接存在
5. **运行**：任意 cwd `xhermes --version` / `--help` / `doctor`
6. **更新**：`xhermes update` 执行 git pull + uv sync 成功
7. **失败注入**：断网/中途 kill → postinstall 非零退出，写
   `pkg-incomplete` stamp，`xhermes update` 拒绝并提示
8. **续跑**：手动补跑失败 stage 后 install.sh 完成、stamp 变 `git`
9. **卸载**：删 `~/.xhermes/xhermes-agent` + `~/.local/bin/xhermes`

## 11. 风险与缓解

| 风险 | 缓解 |
|---|---|
| GUI root 降权后 node/浏览器 stage 行为差异 | 默认跳过 node-deps；`launchctl asuser` 保留 GUI 环境 |
| 安装时长波动（网络慢） | stage 级超时 + postinstall 总超时 30min + 失败可续跑 |
| 半装被误认完整 | `pkg-incomplete` stamp 挡住 `xhermes update` |
| install.sh 上游演进破坏 stage 契约 | stage 清单来自 `--manifest` 动态读取，不硬编码 |
| 离线需求回归 | 预留 `--offline-bundle` 扩展点，不在本文实现 |
