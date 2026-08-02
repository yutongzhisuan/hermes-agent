# Hermes Agent 快速学习路线图

> 目标：如果你是资深后端工程师，如何在 **3~5 小时内** 掌握这个 17k+ 测试用例、90+ 模块的 AI Agent 项目

---

## 一、项目哲学（必读，5 分钟）

在碰任何代码前，先理解两条核心设计原则（出自 `AGENTS.md`）：

| 原则 | 含义 |
|------|------|
| **窄腰（Narrow Waist）** | 核心工具集越小越好，每个核心工具随每次 API 调用发送。新能力优先走：扩展已有代码 → CLI + Skill → Service-gated tool → Plugin → MCP Server → 最后才是新核心工具 |
| **Prompt Caching 神圣不可侵犯** | 一次对话中 system prompt 必须字节稳定。任何导致中途变更 system prompt 的操作（如切换 toolset、注入合成消息）都会使缓存失效，成倍增加成本 |

> **你的学习策略也应该遵循这个哲学**：先理解窄腰核心，再向外探索扩展层。

---

## 二、架构全景图（10 分钟）

```
┌─────────────────────────────────────────────────────────┐
│                   用户界面层 (UI)                         │
│  CLI (cli.py) │ TUI (ui-tui/) │ Desktop │ Gateway │ ACP  │
└──────────────────────┬──────────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────────┐
│                  CLI 框架层 (hermes_cli/)                 │
│  命令分发  │ 配置加载 │ MCP管理 │ 插件管理 │ 安装向导     │
└──────────────────────┬──────────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────────┐
│                 核心 Agent 引擎 (run_agent.py)             │
│  AIAgent 类 │ 对话循环 │ 工具调用 │ 流式响应 │ 上下文压缩  │
└────┬──────────┬──────────┬──────────┬──────────────────┘
     │          │          │          │
┌────▼───┐ ┌───▼────┐ ┌──▼────┐ ┌──▼─────────────┐
│tools/  │ │model_  │ │agent/ │ │provider 适配器   │
│工具实现 │ │tools.py│ │内部模块│ │(OpenAI/Anthropic │
│~40模块  │ │编排层   │ │记忆/  │ │/Gemini/Bedrock)│
└────────┘ └────────┘ └───────┘ └────────────────┘
     │
┌────▼───────────────────────────────────────────────────┐
│            扩展层 (Skills / Plugins / MCP)              │
│  skills/ (内置20+) │ plugins/ (memory/model/platform)  │
│  optional-skills/  │ optional-mcps/ (blender/linear等) │
└────────────────────────────────────────────────────────┘
```

---

## 三、学习路径（分阶段）

### Phase 1：摸骨架 — 30 分钟

从入口点切入，通读关键文件的前 50-100 行，理解项目边界。

| 步骤 | 文件 | 关注什么 |
|------|------|---------|
| 1 | `pyproject.toml` | 项目元数据、依赖策略（exact pin）、console_scripts 入口点 |
| 2 | `hermes_bootstrap.py` | 所有入口点的第一行导入——修复 Win UTF-8、加固 sys.path |
| 3 | `hermes_cli/main.py` | CLI 主入口，`fire` 库自动参数解析 + 子命令分发 |
| 4 | `run_agent.py` | **核心：AIAgent 类**，~12k LOC，对话循环的主体 |
| 5 | `hermes_constants.py` | `get_hermes_home()`——所有路径解析的单一真相源 |
| 6 | `hermes_state.py` | `SessionDB`——SQLite + FTS5 会话存储 |
| 7 | `cli-config.yaml.example` | 查看完整配置结构，了解有哪些可配置项 |

**产出理解**：
- 启动流程：`hermes` → `hermes_cli/main.py` → 解析子命令 → 初始化 → 进入交互/网关/定时任务模式
- 所有持久化数据按 `HERMES_HOME` 隔离（默认 `~/.hermes`）

---

### Phase 2：掌握核心循环 — 2 小时

这是项目的大脑，也是你作为后端工程师最应该深入的部分。

```
用户输入
   │
   ▼
┌─────────────────────────────────────────────────────┐
│ prompt_builder.py: 构建 system prompt                │
│   - 加载 identity (SOUL.md)                          │
│   - 注入 tools 定义（来自 model_tools.py）             │
│   - 注入记忆 / 上下文                                 │
│   - 写入 prompt cache                                │
└────────────────────────┬────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────┐
│ chat_completion_helpers.py: 调用 LLM API             │
│   - 流式/非流式请求                                   │
│   - 适配不同 provider (Anthropic/OpenAI/Gemini)       │
└────────────────────────┬────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────┐
│ model_tools.py: 工具调度                              │
│   handle_function_call() → tools/ 下的具体实现        │
│   支持异步、重试、错误恢复                              │
└────────────────────────┬────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────┐
│ context_compressor.py: 上下文压缩                     │
│   当对话过长时，选择性压缩/摘要旧轮次                   │
└────────────────────────┬────────────────────────────┘
                         │
                         ▼
                   输出给用户
```

**关键文件阅读顺序**：

| 文件 | 行数 | 核心职责 |
|------|------|---------|
| `run_agent.py` | ~12k | AIAgent 类：管理整个对话生命周期 |
| `model_tools.py` | ~500 | 工具注册发现 + 函数调用路由 |
| `toolsets.py` | ~200 | `_HERMES_CORE_TOOLS` 窄腰定义 + toolset 别名系统 |
| `agent/prompt_builder.py` | ~1k | System prompt 组装 |
| `agent/prompt_caching.py` | ~300 | Prompt 缓存管理（字节稳定性保障） |
| `agent/chat_completion_helpers.py` | ~1.5k | LLM 调用 + 流式处理 + 工具调度 |
| `agent/context_compressor.py` | ~800 | 上下文压缩策略 |
| `agent/conversation_loop.py` | ~500 | 轮次驱动逻辑 |

**学习技巧**：从 `model_tools.py` 的 `handle_function_call()` 入口开始追踪一次工具调用的完整生命周期，这对后端工程师来说是最自然的切入点。

---

### Phase 3：扩展机制 — 1 小时

**这是你理解"窄腰"哲学在工程实践中的关键。**

#### 3.1 Tool 自动发现

```
tools/ 下的每个 .py 文件
    │
    ▼
tools/registry.py: discover_builtin_tools()
    - 扫描 tools/ 包下所有模块
    - 每个模块调用 register() 自注册 schema + handler
    │
    ▼
model_tools.py: get_tool_definitions()
    - 过滤：enabled_toolsets / disabled_toolsets / check_fn 门控
    - 返回最终发送给 LLM 的 tool 列表
```

#### 3.2 三种扩展方式对比

| 方式 | 复杂度 | 何时使用 | 示例 |
|------|--------|---------|------|
| **Skill** | 低 | 教 Agent 完成特定任务（领域知识 + 指令） | `skills/github/`、`skills/research/` |
| **Plugin** | 中 | 替换/扩展核心能力 | 记忆后端 (mem0/honcho)、模型提供方、图像生成 |
| **MCP Server** | 中高 | 标准协议的外部工具集成 | blender、linear、n8n、unreal-engine |

**Skill 是 Hermes 的特色**：Agent 能从经验中自动创建 Skill（`agent/learning_graph.py`、`agent/learn_prompt.py`），实现自我改进。

---

### Phase 4：消息网关 — 1 小时

**如果你对多平台消息系统感兴趣，这是重点。**

| 文件 | 职责 |
|------|------|
| `gateway/run.py` | 网关主循环 |
| `gateway/session.py` | 每会话管理（映射平台 session → Hermes session） |
| `gateway/platforms/` | **20+ 平台适配器**（Telegram/Discord/Slack/WhatsApp/微信/飞书/钉钉等） |
| `gateway/relay/` | WebSocket 中继（desktop app 通信） |
| `gateway/stream_dispatch.py` | 流式响应分发 |
| `gateway/turn_lease.py` | Turn 管理（避免重复处理） |

**架构模式**：每个平台适配器实现统一基类，处理平台特有的事件格式 → 转成 Hermes 内部消息 → 调用核心 Agent → 将流式输出转回平台格式。

---

## 四、调试与学习工具

### 4.1 日志系统

```python
# hermes_logging.py 按 profile 隔离日志
~/.hermes/{profile}/
├── agent.log       # Agent 运行日志
├── errors.log      # 错误日志
├── gateway.log     # 网关日志
└── hermes.db       # SQLite 会话数据库
```

### 4.2 关键调试命令

```bash
# 查看当前配置
hermes doctor

# 查看可用 toolsets
hermes tools

# 测试 MCP 连接
hermes mcp test <server-name>

# 查看会话历史
hermes sessions
```

### 4.3 测试体系概览

```
tests/ (~900 文件, ~17k 测试)
├── agent/          # 核心 Agent 测试
├── cli/            # CLI 测试
├── gateway/        # 网关测试
├── hermes_cli/     # CLI 框架测试
├── cron/           # 定时任务测试
├── skills/         # Skill 测试
├── plugins/        # 插件测试
├── e2e/            # 端到端测试
├── integration/    # 集成测试（默认排除）
└── stress/         # 压力测试
```

测试哲学（出自 AGENTS.md）：**行为契约优先于快照**——测试不变量，不冻结当前值。

---

## 五、进阶理解（可选的深度模块）

当你想深入特定领域时：

| 领域 | 关键文件 | 前置条件 |
|------|---------|---------|
| **记忆系统** | `agent/memory_manager.py`, `agent/memory_provider.py` | 先理解插件系统 |
| **自改进/Skill 创建** | `agent/learning_graph.py`, `agent/learn_prompt.py` | 理解核心循环 |
| **多 Agent Kanban** | `hermes_cli/kanban*.py`, `tools/kanban_tools.py` | 理解工具系统 |
| **Mixture-of-Agents** | `agent/moa_loop.py`, `agent/moa_trace.py` | 理解核心循环 |
| **上下文压缩** | `agent/context_compressor.py`, `agent/conversation_compression.py` | 理解 LLM 调用 |
| **Editor ACP 集成** | `acp_adapter/` | VS Code/Cursor 集成 |
| **Docker 部署** | `Dockerfile`, `docker/`, `docker-compose.yml` | s6-overlay 监督树 |

---

## 六、学习建议总结

```
Phase 1: 摸骨架 ────────────────── 30 min
  pyproject.toml → hermes_bootstrap → main.py → run_agent.py → constants

Phase 2: 核心循环 ──────────────── 2 hours ★ 最重点
  model_tools.py → toolsets.py → prompt_builder → chat_completion_helpers
  → context_compressor → conversation_loop

Phase 3: 扩展机制 ──────────────── 1 hour
  tools/registry.py → skills/ → plugins/ → MCP

Phase 4: 消息网关 ──────────────── 1 hour  (按需)
  gateway/run.py → platforms/ → relay/ → session management

进阶: 按兴趣选择 ──────────────── 可选
  记忆 / 自改进 / Kanban / MoA / ACP
```

**如果你是后端架构师**，建议从 Phase 2 的 `model_tools.py` 和 `toolsets.py` 开始——这里是架构决策最集中的地方，能看到"窄腰"哲学在代码层面如何落地。

---

*Generated by 🧭 砚 | 2026-07-23*
