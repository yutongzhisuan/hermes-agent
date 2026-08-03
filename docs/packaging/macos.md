# xHermes Agent macOS 打包指南

本文档描述如何在 macOS 上构建可分发的安装包。当前**唯一已验证**的产物是
**CLI 的 `pkg` 安装包**（自包含，装完即用）；桌面端（Electron）仅记录了
现状与规划，尚未实现。

参考实现：`scripts/build_macos_pkg.sh`（一键构建，见 §3.2）。

---

## 1. 分发方式总览

xHermes Agent 的官方分发方式有四种，`pkg` 是其中针对 macOS 原生安装体验
的一种补充：

| 方式 | 目标平台 | 需要网络 | 需要 uv | 安装位置 | 状态 |
|---|---|---|---|---|---|
| `scripts/install.sh` | Linux / macOS / Termux | 是（git clone + uv sync） | 安装时 | `$HERMES_HOME/xhermes-agent` | 官方主路径 |
| Docker 镜像 | Linux | 是 | 否 | 容器内 | 官方 |
| Nix（uv2nix） | Linux / macOS | 构建时 | 否 | Nix store | 官方 |
| **macOS pkg（本文）** | **macOS arm64** | **构建时** | **构建时** | `/usr/local/lib/xhermes-agent` | **已验证** |

### 为什么不能打 wheel / sdist

`setup.py` 明确禁止 `bdist_wheel` / `sdist`（`HERMES_NIX_BUILD=1` 除外）：

- wheel 只包含 Python 包，**不含运行时资产**：`skills/`、`optional-skills/`、
  `locales/`、`hermes_cli/web_dist/`（dashboard 前端）、`ui-tui/dist/`（TUI 前端）
- 这些资产在运行时通过**源码布局**（`__file__` 相对解析）或环境变量覆盖
  （`HERMES_BUNDLED_SKILLS` 等）定位，wheel 装完找不到它们
- 因此 pkg 采用**源码树 + 自包含 Python** 布局，语义与 `install.sh` 一致，
  只是把「安装时拉源码 + uv sync」提前到「构建时」完成，用户侧变成一次
  离线安装

---

## 2. 环境要求

| 项 | 要求 | 说明 |
|---|---|---|
| macOS | arm64（Apple Silicon） | 当前仅支持 arm64；Intel 需另行验证 |
| uv | ≥ 0.11 | 依赖安装 + uv 托管 Python |
| 磁盘 | 约 1.5 GB 临时空间 | 依赖树 + Python + 源码 staging |
| 网络 | 首次构建需要 | `uv pip install` 拉核心依赖（之后可复用缓存） |
| Xcode CLT | 有即可 | `pkgbuild` 随 CLT 提供 |

---

## 3. CLI pkg 构建

### 3.1 产物结构

构建产物是标准 macOS 安装包，payload 布局如下：

```
/usr/local/lib/xhermes-agent/
├── python/                 # 自包含 CPython 3.11（uv 托管版整体拷贝，可迁移）
├── site-packages/          # 26 个核心依赖（openai/httpx/pydantic 等）
└── xhermes-agent/          # 精简源码树（72MB，含 skills/locales/web_dist）
/usr/local/bin/xhermes      # postinstall 创建的 launcher（符号链接）
```

`python/` 是**关键**：uv 托管的 CPython 是真实 Mach-O 二进制（非软链），
拷贝到任意路径都能运行，因此 pkg 完全自包含——目标机不需要 uv、Homebrew
或网络。

`xhermes` launcher 内容：

```sh
#!/bin/sh
BASE=/usr/local/lib/xhermes-agent
export PYTHONPATH="$BASE/site-packages:$BASE/xhermes-agent"
exec "$BASE/python/bin/python3.11" -m hermes_cli.main "$@"
```

### 3.2 一键构建（推荐）

```bash
scripts/build_macos_pkg.sh                 # 默认输出 dist/
scripts/build_macos_pkg.sh --out-dir /tmp/pkg
scripts/build_macos_pkg.sh --version 0.20.0
```

产物：`dist/xHermes-CLI-<version>-arm64.pkg`。staging 目录
`dist/pkg-stage/` 会保留，便于调试。

### 3.3 手工步骤（脚本的等价说明）

脚本按以下顺序执行，任一步骤失败即退出非零：

1. **定位 uv 托管 Python**：从 `uv python dir`（托管根）按版本 glob 定位，
   缺失则 `uv python install 3.11`。**不能**用 `uv python find`——在 repo
   内运行时会优先返回项目 `.venv` 的 python，其 site-packages 含 editable
   安装和构建机绝对路径，会被整个拷进 pkg（见 §7 已知限制中的遗留事故）
2. **拷贝 Python**：整体拷贝 uv 托管目录到 `python/`
3. **提取核心依赖**：从 `pyproject.toml` 的 `dependencies` 数组提取，
   并**按构建机平台求值环境标记**（`sys_platform` / `platform_machine`
   的 `==`/`!=` 比较 + `and`/`or`/括号）——不是一刀切丢弃带标记的依赖：
   `ptyprocess`（`sys_platform != 'win32'`）和 `nemo-relay`（darwin/arm64
   标记）在 macOS 上是必需的，必须保留；win32-only 的
   `tzdata`/`pywinpty`/`pywin32`/`concurrent-log-handler` 则被正确排除
4. **安装依赖**：`uv pip install --target site-packages -r req.txt`
   —— 必须用 `--target` 独立目录，因为 uv 拒绝修改其托管的 Python
5. **拷贝源码树**：`rsync` 精简拷贝，排除 `.git`/`.venv`/`node_modules`/
   `tests`/`web_dist` 等构建期或超大目录；删除源码副本内运行时不需要的
   Go 构建产物（`extend/task_relay/hub/go` 等）
6. **清理扩展属性**：`xattr -cr`（防御性；`com.apple.provenance` 由系统
   维护清不掉，但不影响产物）
7. **写 postinstall**：生成 launcher 并创建 `/usr/local/bin/xhermes` 符号链接
8. **pkgbuild**：`COPYFILE_DISABLE=1 pkgbuild --root <stage> --identifier
   com.xhermes.cli --install-location /`

### 3.4 关于 AppleDouble（BOM 里的 `._*` 条目）

`pkgutil --payload-files` 或 `lsbom` 查看 BOM 时会出现大量 `._*` 条目
（如 `._python`、`._main.py`）。这是 **pkgbuild 记录扩展属性的标准机制**，
不是文件污染：

- `pkgutil --expand-full` 解出的**实体 Payload 中没有** `._*` 文件
- installer 消费这些条目用于恢复 xattr，**不会在目标磁盘创建 `._*` 文件**
- `COPYFILE_DISABLE=1` 对 pkgbuild 无效（它是 ditto/cpio 的开关），脚本中
  保留它和 `xattr -cr` 作为无害防御

---

## 4. 安装与验证

### 4.1 安装

```bash
sudo installer -pkg dist/xHermes-CLI-0.19.1-arm64.pkg -target /
```

### 4.2 验证清单

```bash
xhermes --version            # 应输出版本、Python、OpenAI SDK 版本
xhermes doctor               # 配置/依赖自检
xhermes --help               # 命令帮助
```

### 4.3 卸载

```bash
sudo rm -f /usr/local/bin/xhermes
sudo rm -rf /usr/local/lib/xhermes-agent
```

用户数据（`~/.xhermes/`）不受影响，可保留。

---

## 5. 签名与公证

> 现状：当前 pkg **未签名**。本机安装（`sudo installer`）不受影响；
> 分发给他人时 Gatekeeper 会拦截「未验证的开发者」提示。

### 5.1 证书要求

| 证书 | 用途 |
|---|---|
| Developer ID Application | 签 `.app`（桌面端） |
| Developer ID Installer | 签 `.pkg`（CLI） |

两者是**不同的证书**，需要在 Apple Developer 后台分别创建。pkg 签名必须
用 Installer 证书。

### 5.2 签名命令

```bash
# 配置证书（二选一）
export CSC_NAME="Developer ID Installer: Your Name (TEAMID)"   # 钥匙串
# 或 export CSC_LINK=/path/to/cert.p12 && export CSC_PASSWORD=...

# pkgbuild 时直接签名
COPYFILE_DISABLE=1 pkgbuild --root <stage> \
    --identifier com.xhermes.cli --version 0.19.1 \
    --scripts <stage>/scripts --install-location / \
    --sign "Developer ID Installer: Your Name (TEAMID)" \
    dist/xHermes-CLI-0.19.1-arm64.pkg
```

### 5.3 公证（notarization）

```bash
# 方式一：keychain-profile（推荐，凭证存钥匙串）
xcrun notarytool submit dist/xHermes-CLI-0.19.1-arm64.pkg \
    --keychain-profile hermes-notary --wait

# 方式二：API key（与桌面端 notarize.mjs 同款凭证）
xcrun notarytool submit dist/xHermes-CLI-0.19.1-arm64.pkg \
    --key <api-key.p8> --key-id <KEY_ID> --issuer <ISSUER> --wait

# 打 stapler（把公证凭证嵌入 pkg，离线验证也通过）
xcrun stapler staple dist/xHermes-CLI-0.19.1-arm64.pkg
```

### 5.4 pkg 与 .app 公证的差异

| 项 | `.app`（桌面端） | `.pkg`（CLI） |
|---|---|---|
| 提交对象 | 压缩 zip | pkg 文件本身 |
| 签名证书 | Application | Installer |
| 公证流程 | 现有 `notarize.mjs`（afterSign 钩子） | 需 pkg 构建后单独跑 notarytool |
| stapler | 嵌进 app 包 | 嵌进 pkg |

### 5.5 常见坑

- **证书类型错误**：用 Application 证书签 pkg 会报 `no identity found`
  或签名无效
- **公证后改动**：stapler 之后不能再改 pkg（签名失效），需重签重公证
- **`--wait` 超时**：公证队列繁忙时可到几分钟；用 `notarytool log` 查详情

---

## 6. 桌面端 pkg（未实现）

> 本节为**规划**，尚未实施。

### 6.1 现状

- `apps/desktop` 使用 electron-builder 26.x，mac target 为 `dmg` + `zip`
- 已配置：`appId: com.xhermes.app`、`hardenedRuntime`、entitlements、
  `afterSign: scripts/notarize.mjs`（notarytool 公证）
- 桌面应用是 Electron 壳，运行时通过 `main.ts` 从 managed install / PATH
  解析 `xhermes serve` 启动 Python 后端

### 6.2 规划

1. **加 pkg target**：`build.mac.target` 增加 `"pkg"`，或
   `npx electron-builder --mac pkg`
2. **解决后端运行时**：pkg 只装 Electron 壳，目标机没有 Python 运行时
   会启动失败。二选一：
   - pkg `postinstall` 调用 `scripts/install.sh` 装 Python 运行时到
     `~/.xhermes/`
   - `extraResources` 捆绑自建 venv（体积大，需每 arch 构建）
3. **pkg 公证**：electron-builder 的 `mac.notarize` 或
   `afterAllArtifactBuild` 钩子对 `.pkg` 再跑一次 notarytool
4. **确认无自更新依赖**：桌面端当前**未使用** electron-updater/autoUpdater，
   无 pkg 破坏自动更新的问题

### 6.3 建议

- 目标若为「本机 / 团队内分发」：加 pkg target + postinstall 装运行时
- 目标若为「对外发布」：先备齐 Developer ID Installer 证书 + pkg 公证步骤

---

## 7. 已知限制与后续

**当前限制：**

- 未签名（Gatekeeper 限制对外分发）
- 仅 arm64
- 可选依赖（anthropic / telegram 等 extras）走运行时 lazy install
  （`tools/lazy_deps.py` 的 uv→pip→ensurepip 阶梯），不预装
- **非 root 用户的 lazy install 会失败**：`/usr/local/lib/xhermes-agent`
  归 root 所有，普通用户触发可选依赖安装时 `pip install` 写
  site-packages 会 PermissionError（与 install.sh 的用户级布局不同）
- **真实 sudo 安装尚未端到端验证**：stage 运行、payload 展开、postinstall
  生成均已验证，但 `sudo installer` 的完整安装路径（postinstall 实际执行、
  `/usr/local/bin/xhermes` 符号链接创建）尚未在本机跑通
- 升级需重装（无自更新机制；`hermes update` 面向 install.sh 布局）

**已验证并修复的构建事故（2026-08-03）：**

- **venv 污染**：`uv python find` 在 repo 内返回项目 `.venv`，导致 322M
  editable 安装 + 构建机绝对路径被打进 pkg。已改为从 `uv python dir`
  托管根定位
- **依赖缺失**：一刀切过滤平台标记丢掉了 macOS 必需的 `ptyprocess` 和
  `nemo-relay`。已改为按构建机平台求值标记
- **launcher 版本写死**：postinstall 写死 `python3.11`，`--python-version`
  传其他版本时会调用不存在的解释器。已改为变量注入
- **`uv python find` 引号 bug**：`"uv python find"` 被当作单一命令名，
  静默失败。已修复

**后续路线：**

- [ ] 签名 + 公证流程（§5）落地到 CI
- [ ] 桌面端 pkg（§6）
- [ ] universal2 双架构构建
- [ ] 与 `scripts/release.py` 发布流程集成

---

## 附录：验证记录（2026-08-03）

以下为构建脚本产出物的实际验证结果：

```
xHermes-CLI-0.19.1-arm64.pkg    75MB（修复 venv 污染 + 依赖缺失后）
BOM 实体 Payload               15388+ 个文件，0 个 ._* 文件
核心依赖                       28 个（含 ptyprocess、nemo-relay）
payload 大小                   172MB（源码 72M + python 63M + deps 41M）

xhermes --version 输出:
  xHermes Agent v0.19.1 (2026.7.30)
  Python: 3.11.15
  OpenAI SDK: 2.24.0

模块解析验证（任意 cwd）:
  hermes_cli → payload/xhermes-agent/hermes_cli   ✓
  openai     → payload/site-packages/openai       ✓
  ptyprocess / nemo_relay 导入                     ✓
  ensurepip / pip 26.0.1 在位                     ✓（lazy install pip 分支可用）
```
