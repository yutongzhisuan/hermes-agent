package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/infa/task_relay/master/agent/executor"
	"github.com/infa/task_relay/master/agent/filetools"
	"github.com/infa/task_relay/master/agent/policy"
	"github.com/infa/task_relay/master/agent/search"
	"github.com/infa/task_relay/master/client"
)

const defaultInstruction = `You are a Task Relay Master agent coordinating local and remote workers.

Local vs remote:
- Use the "task" tool to delegate LOCAL subagent work (planning, analysis, drafting).
  Available local subagents include the general-purpose agent and local-planner.
- Use dispatch_task / dispatch_batch + watch_and_join for REMOTE XHermes workers via Relay Hub.
  Remote work requires toolsets, NAT traversal, checkpoints, and leases that local subagents do not provide.

Workflow:
1. Use write_todos to plan when the goal spans multiple steps.
2. Optionally use "task" with local-planner to refine decomposition.
3. Choose a unique callback_topic for each remote batch/session.
4. dispatch_task or dispatch_batch for remote execution.
5. watch_and_join before summarizing remote outcomes.
6. Use local subagents to synthesize or refine answers from remote results.

Never invent remote task results; always wait for watch_and_join before claiming remote completion.`

const localOnlyInstruction = `You are a local Master agent running inside this process.

You have NO remote Relay Hub and NO remote workers.
Do not call or invent dispatch_task, dispatch_batch, watch_and_join, get_task_result, or cancel_task.

How to work:
1. Reason about the user goal directly.
2. Use write_todos to plan when the goal spans multiple steps.
3. Use the "task" tool with local-planner or the general-purpose local subagent for decomposition, analysis, or drafting.
4. Produce the final answer yourself from local reasoning and local subagent outputs.

Stay entirely local to this process.`

// Mode selects the Eino ADK agent implementation.
type Mode string

const (
	// ModeDeep uses DeepAgent: write_todos + local subagents (task tool) + optional Relay tools.
	ModeDeep Mode = "deep"
	// ModeReAct uses ChatModelAgent with ToolsConfig (ReAct loop, no DeepAgent helpers).
	ModeReAct Mode = "react"
)

// Config holds Hub, auth, LLM, and agent-mode settings.
type Config struct {
	// HubAddr is the Relay Hub gRPC address. Empty enables local-only mode
	// (no remote workers; requests are handled in this process).
	HubAddr          string
	MasterJWT        string
	MasterSession    string
	OpenAIAPIKey     string
	OpenAIModel      string
	OpenAIBaseURL    string
	OpenAISmallModel string
	Instruction      string
	MaxIterations    int
	Mode             Mode

	// DisableLocalSubAgents turns off DeepAgent's general-purpose "task" subagent.
	DisableLocalSubAgents bool
	// DisableLocalPlanner skips the built-in local-planner subagent.
	DisableLocalPlanner bool
	// SubAgents registers additional local subagents available through the "task" tool.
	SubAgents []adk.Agent

	// ChatModel injects a model and skips OpenAI construction (primarily for tests).
	ChatModel model.BaseModel[*schema.Message]
	// Tools injects tools and skips Hub RelayTools / MCP / search construction (primarily for tests).
	Tools []tool.BaseTool

	// ConfigPath loads the unified master YAML/JSON (mcpServers + search).
	ConfigPath string
	// MCPConfigPath is deprecated; prefer ConfigPath. Still loads MCP-only when ConfigPath is empty.
	MCPConfigPath string
	// MCPServers registers MCP servers inline (merged after file config).
	MCPServers map[string]MCPServerConfig
	// Search configures web_search / web_extract (overrides file search section when non-nil).
	Search *search.Config
	// Exec configures the bash tool (overrides file exec section when non-nil).
	Exec *ExecConfig
	// File configures the file tools (overrides file section when non-nil).
	File *FileToolsConfig

	HubTLS        client.TLSConfig
	EnableMetrics bool
	MetricsAddr   string
	EnableTracing bool
	OTelEndpoint  string
}

// Master wraps an Eino ADK Runner backed by local subagents and optional Relay tools.
type Master struct {
	Runner          *adk.Runner
	Hub             *client.Client
	LocalOnly       bool
	Model           string
	shutdownTracing func(context.Context) error
	mcpClose        func() error
}

// New builds a Master agent.
// When HubAddr and MasterJWT are both empty, the agent runs in local-only mode:
// no Hub connection and no remote Relay tools; user goals are handled by the
// local LLM and local subagents in this process.
// ChatModel / Tools may be injected for tests to skip OpenAI and Hub construction.
func New(ctx context.Context, cfg Config) (*Master, error) {
	useInjectedTools := len(cfg.Tools) > 0
	useInjectedModel := cfg.ChatModel != nil
	localOnly := cfg.HubAddr == "" && cfg.MasterJWT == ""

	if !localOnly {
		if cfg.HubAddr == "" {
			return nil, fmt.Errorf("hub address is required when master JWT is set")
		}
		if cfg.MasterJWT == "" {
			return nil, fmt.Errorf("master JWT is required when hub address is set")
		}
	}
	if !useInjectedModel && cfg.OpenAIAPIKey == "" {
		return nil, fmt.Errorf("openai api key is required")
	}
	var err error
	cfg, err = applyFileConfig(cfg)
	if err != nil {
		return nil, err
	}
	cfg = applyConfigDefaults(cfg, localOnly && !useInjectedTools)

	shutdownTracing := func(context.Context) error { return nil }
	if cfg.EnableTracing {
		shutdownTracing, err = client.InitTracing(ctx, "task-relay-master", cfg.OTelEndpoint)
		if err != nil {
			return nil, err
		}
	}
	if cfg.EnableMetrics && cfg.MetricsAddr != "" {
		go func() {
			_ = client.StartMetricsServer(ctx, cfg.MetricsAddr, prometheus.DefaultGatherer)
		}()
	}

	var hub *client.Client
	if !useInjectedTools && !localOnly {
		hub, err = client.New(ctx, client.Config{
			Addr:          cfg.HubAddr,
			MasterJWT:     cfg.MasterJWT,
			TLS:           cfg.HubTLS,
			EnableMetrics: cfg.EnableMetrics,
			EnableTracing: cfg.EnableTracing,
		})
		if err != nil {
			_ = shutdownTracing(context.Background())
			return nil, err
		}
	}

	chatModel := cfg.ChatModel
	if chatModel == nil {
		modelCfg := &openai.ChatModelConfig{
			APIKey: cfg.OpenAIAPIKey,
			Model:  cfg.OpenAIModel,
		}
		if cfg.OpenAIBaseURL != "" {
			modelCfg.BaseURL = cfg.OpenAIBaseURL
		}
		chatModel, err = openai.NewChatModel(ctx, modelCfg)
		if err != nil {
			_ = closeHub(hub)
			_ = shutdownTracing(context.Background())
			return nil, fmt.Errorf("openai chat model: %w", err)
		}
	}

	plannerModel := chatModel
	if cfg.OpenAISmallModel != "" {
		smallCfg := &openai.ChatModelConfig{
			APIKey: cfg.OpenAIAPIKey,
			Model:  cfg.OpenAISmallModel,
		}
		if cfg.OpenAIBaseURL != "" {
			smallCfg.BaseURL = cfg.OpenAIBaseURL
		}
		plannerModel, err = openai.NewChatModel(ctx, smallCfg)
		if err != nil {
			_ = closeHub(hub)
			_ = shutdownTracing(context.Background())
			return nil, fmt.Errorf("openai small chat model: %w", err)
		}
	}

	tools := cfg.Tools
	var mcpClose func() error
	if !useInjectedTools {
		if !localOnly {
			tools, err = (&RelayTools{Hub: hub, MasterSession: cfg.MasterSession}).Build()
			if err != nil {
				_ = closeHub(hub)
				_ = shutdownTracing(context.Background())
				return nil, err
			}
		}
		mcpToolkit, mcpErr := loadConfiguredMCP(ctx, cfg)
		if mcpErr != nil {
			_ = closeHub(hub)
			_ = shutdownTracing(context.Background())
			return nil, mcpErr
		}
		if mcpToolkit != nil {
			mcpClose = mcpToolkit.Close
			tools = append(tools, mcpToolkit.Tools...)
		}
		searchTools, searchErr := BuildSearchTools(cfg.Search)
		if searchErr != nil {
			if mcpClose != nil {
				_ = mcpClose()
			}
			_ = closeHub(hub)
			_ = shutdownTracing(context.Background())
			return nil, searchErr
		}
		tools = append(tools, searchTools...)
		if cfg.Exec != nil && cfg.Exec.Enabled {
			bashTool, bashErr := buildBashTool(cfg)
			if bashErr != nil {
				if mcpClose != nil {
					_ = mcpClose()
				}
				_ = closeHub(hub)
				_ = shutdownTracing(context.Background())
				return nil, bashErr
			}
			if bashTool != nil {
				tools = append(tools, bashTool)
			}
		}
		if cfg.File != nil && cfg.File.Enabled {
			fileTools, fileErr := buildFileTools(cfg)
			if fileErr != nil {
				if mcpClose != nil {
					_ = mcpClose()
				}
				_ = closeHub(hub)
				_ = shutdownTracing(context.Background())
				return nil, fileErr
			}
			tools = append(tools, fileTools...)
		}
		if err = ensureUniqueToolNames(ctx, tools); err != nil {
			if mcpClose != nil {
				_ = mcpClose()
			}
			_ = closeHub(hub)
			_ = shutdownTracing(context.Background())
			return nil, err
		}
	}
	toolsConfig := adk.ToolsConfig{
		ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
	}

	agentImpl, err := buildAgent(ctx, cfg, chatModel, plannerModel, toolsConfig, localOnly && !useInjectedTools)
	if err != nil {
		if mcpClose != nil {
			_ = mcpClose()
		}
		_ = closeHub(hub)
		_ = shutdownTracing(context.Background())
		return nil, err
	}

	return &Master{
		Runner:          adk.NewRunner(ctx, adk.RunnerConfig{Agent: agentImpl}),
		Hub:             hub,
		LocalOnly:       localOnly && !useInjectedTools,
		Model:           cfg.OpenAIModel,
		shutdownTracing: shutdownTracing,
		mcpClose:        mcpClose,
	}, nil
}

func applyFileConfig(cfg Config) (Config, error) {
	path := cfg.ConfigPath
	if path == "" {
		path = cfg.MCPConfigPath
	}
	if path == "" {
		return cfg, nil
	}
	fileCfg, err := LoadMasterConfigFile(path)
	if err != nil {
		return cfg, err
	}
	merged, _, err := MergeFileIntoConfig(cfg, fileCfg)
	return merged, err
}

func loadConfiguredMCP(ctx context.Context, cfg Config) (*MCPToolkit, error) {
	if len(cfg.MCPServers) == 0 {
		return nil, nil
	}
	return LoadMCPTools(ctx, cfg.MCPServers)
}

func ensureUniqueToolNames(ctx context.Context, tools []tool.BaseTool) error {
	seen := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		if t == nil {
			continue
		}
		info, err := t.Info(ctx)
		if err != nil {
			return fmt.Errorf("tool info: %w", err)
		}
		if info == nil || info.Name == "" {
			return fmt.Errorf("tool has empty name")
		}
		if _, ok := seen[info.Name]; ok {
			return fmt.Errorf("duplicate tool name %q", info.Name)
		}
		seen[info.Name] = struct{}{}
	}
	return nil
}

func applyConfigDefaults(cfg Config, localOnly bool) Config {
	if cfg.OpenAIModel == "" {
		cfg.OpenAIModel = "gpt-4o-mini"
	}
	if cfg.MasterSession == "" {
		cfg.MasterSession = "master-session"
	}
	if cfg.Instruction == "" {
		if localOnly {
			cfg.Instruction = localOnlyInstruction
		} else {
			cfg.Instruction = defaultInstruction
		}
	}
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = 20
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeDeep
	}
	return cfg
}

func closeHub(hub *client.Client) error {
	if hub == nil {
		return nil
	}
	return hub.Close()
}

func buildBashTool(cfg Config) (tool.BaseTool, error) {
	execCfg := cfg.Exec.WithDefaults()
	if execCfg.DefaultBackend == "remote" {
		return nil, fmt.Errorf("exec default_backend=remote is not yet implemented (remote backend is phase 2)")
	}
	exec, err := executor.NewLocal(executor.LocalOptions{})
	if err != nil {
		return nil, fmt.Errorf("exec local backend: %w", err)
	}
	audit, err := policy.NewAuditLogger(execCfg.AuditPath)
	if err != nil {
		return nil, fmt.Errorf("exec audit: %w", err)
	}
	bash := NewBashTool(BashToolDeps{
		Evaluator:    policy.NewEvaluator(execCfg.Policy),
		Executor:     exec,
		Audit:        audit,
		Limits:       execCfg.Limits,
		EnvAllowKeys: execCfg.EnvAllowKeys,
		Session:      cfg.MasterSession,
	})
	t, err := toolutils.InferTool(
		"bash",
		"Execute a shell command under policy control (allow-list, audit). Use for local system commands.",
		bash.Run,
	)
	if err != nil {
		return nil, fmt.Errorf("bash tool: %w", err)
	}
	return t, nil
}

func buildFileTools(cfg Config) ([]tool.BaseTool, error) {
	wd, _ := os.Getwd()
	fileCfg := cfg.File.WithDefaults(wd)
	paths, err := policy.NewPathEvaluator(fileCfg.Root, fileCfg.Policy)
	if err != nil {
		return nil, fmt.Errorf("file root: %w", err)
	}
	auditPath := filepath.Join(filepath.Dir(fileCfg.Root), ".task-relay", "file-audit.jsonl")
	if cfg.Exec != nil && cfg.Exec.AuditPath != "" {
		auditPath = cfg.Exec.AuditPath
	}
	audit, err := policy.NewAuditLogger(auditPath)
	if err != nil {
		return nil, fmt.Errorf("file audit: %w", err)
	}
	deps := &filetools.Deps{
		Paths: paths, Audit: audit,
		MaxReadBytes: fileCfg.MaxReadBytes, MaxWriteBytes: fileCfg.MaxWriteBytes,
		Session: cfg.MasterSession,
	}
	viewT, err := toolutils.InferTool("view", "Read a file with line numbers (policy-gated, audited)", filetools.NewViewTool(deps).Run)
	if err != nil {
		return nil, fmt.Errorf("view tool: %w", err)
	}
	writeT, err := toolutils.InferTool("write", "Write a file, creating parent dirs (policy-gated, audited)", filetools.NewWriteTool(deps).Run)
	if err != nil {
		return nil, fmt.Errorf("write tool: %w", err)
	}
	editT, err := toolutils.InferTool("edit", "Replace exact text in a file; old_string must match uniquely unless replace_all (policy-gated, audited)", filetools.NewEditTool(deps).Run)
	if err != nil {
		return nil, fmt.Errorf("edit tool: %w", err)
	}
	multieditT, err := toolutils.InferTool("multiedit", "Apply multiple exact replacements atomically; all-or-nothing (policy-gated, audited)", filetools.NewMultiEditTool(deps).Run)
	if err != nil {
		return nil, fmt.Errorf("multiedit tool: %w", err)
	}
	return []tool.BaseTool{viewT, writeT, editT, multieditT}, nil
}

func buildAgent(
	ctx context.Context,
	cfg Config,
	chatModel model.BaseModel[*schema.Message],
	plannerModel model.BaseModel[*schema.Message],
	toolsConfig adk.ToolsConfig,
	localOnly bool,
) (adk.Agent, error) {
	switch cfg.Mode {
	case ModeReAct:
		desc := "ReAct Master with local reasoning and remote Relay worker orchestration"
		if localOnly {
			desc = "ReAct Master handling requests locally in this process"
		}
		return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name:          "task-relay-master",
			Description:   desc,
			Instruction:   cfg.Instruction,
			Model:         chatModel,
			ToolsConfig:   toolsConfig,
			MaxIterations: cfg.MaxIterations,
		})
	case ModeDeep:
		localSubAgents, err := buildLocalSubAgents(ctx, cfg, plannerModel)
		if err != nil {
			return nil, err
		}
		desc := "Deep Master coordinating local subagents and remote XHermes workers via Task Relay"
		if localOnly {
			desc = "Deep Master handling requests with local subagents in this process"
		}
		return deep.New(ctx, &deep.Config{
			Name:                   "task-relay-master",
			Description:            desc,
			Instruction:            cfg.Instruction,
			ChatModel:              chatModel,
			ToolsConfig:            toolsConfig,
			SubAgents:              localSubAgents,
			MaxIteration:           cfg.MaxIterations,
			WithoutGeneralSubAgent: cfg.DisableLocalSubAgents,
		})
	default:
		return nil, fmt.Errorf("unsupported agent mode %q (use deep or react)", cfg.Mode)
	}
}

// Close releases Hub, MCP, and tracing resources.
func (m *Master) Close() error {
	if m == nil {
		return nil
	}
	var err error
	if m.mcpClose != nil {
		err = m.mcpClose()
	}
	if m.Hub != nil {
		if hubErr := m.Hub.Close(); err == nil {
			err = hubErr
		}
	}
	if m.shutdownTracing != nil {
		_ = m.shutdownTracing(context.Background())
	}
	return err
}

// Run executes one user goal and returns the final assistant text.
// Pass WithVerbose(os.Stderr) for AgentEvent text traces and/or WithSlog for
// ChatModel/Tool slog callbacks; both may be enabled together.
func (m *Master) Run(ctx context.Context, goal string, opts ...RunOption) (string, error) {
	cfg := applyRunOptions(opts)
	ctx, trace := withRunTrace(ctx)
	trace.setModel(m.Model)
	started := time.Now()
	if cfg.verbose != nil {
		fmt.Fprintf(cfg.verbose, "=== master run start run_id=%s model=%s local_only=%v goal=%q ===\n",
			trace.runID, trace.Model(), m.LocalOnly, goal)
	}
	if cfg.slog != nil {
		cfg.slog.InfoContext(ctx, "master run start",
			"run_id", trace.runID,
			"model", trace.Model(),
			"local_only", m.LocalOnly,
			"goal", goal,
		)
	}

	var queryOpts []adk.AgentRunOption
	if len(cfg.callbacks) > 0 {
		queryOpts = append(queryOpts, adk.WithCallbacks(cfg.callbacks...))
	}
	iter := m.Runner.Query(ctx, goal, queryOpts...)
	var last string
	step := 0
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		step++
		if cfg.verbose != nil {
			logAgentEvent(cfg.verbose, step, event)
		}
		if event.Err != nil {
			if cfg.slog != nil {
				cfg.slog.ErrorContext(ctx, "master run error",
					"run_id", trace.runID,
					"model", trace.Model(),
					"steps", step,
					"llm_calls", trace.llmCalls(),
					"tool_calls", trace.toolCalls(),
					"duration", time.Since(started).String(),
					"err", event.Err,
				)
			}
			return last, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		msg, err := event.Output.MessageOutput.GetMessage()
		if err != nil || msg == nil {
			continue
		}
		if msg.Content != "" && (event.Output.MessageOutput.Role == schema.Assistant || msg.Role == schema.Assistant) {
			last = msg.Content
		}
	}
	if cfg.verbose != nil {
		fmt.Fprintf(cfg.verbose, "=== master run end run_id=%s model=%s steps=%d llm_calls=%d tool_calls=%d ===\n",
			trace.runID, trace.Model(), step, trace.llmCalls(), trace.toolCalls())
	}
	if cfg.slog != nil {
		cfg.slog.InfoContext(ctx, "master run end",
			"run_id", trace.runID,
			"model", trace.Model(),
			"steps", step,
			"llm_calls", trace.llmCalls(),
			"tool_calls", trace.toolCalls(),
			"duration", time.Since(started).String(),
			"answer_len", len(last),
		)
	}
	if last == "" {
		return "", fmt.Errorf("agent produced no final message")
	}
	return last, nil
}
