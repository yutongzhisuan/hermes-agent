package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/prometheus/client_golang/prometheus"

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
	HubAddr       string
	MasterJWT     string
	MasterSession string
	OpenAIAPIKey  string
	OpenAIModel   string
	OpenAIBaseURL string
	Instruction   string
	MaxIterations int
	Mode          Mode

	// DisableLocalSubAgents turns off DeepAgent's general-purpose "task" subagent.
	DisableLocalSubAgents bool
	// DisableLocalPlanner skips the built-in local-planner subagent.
	DisableLocalPlanner bool
	// SubAgents registers additional local subagents available through the "task" tool.
	SubAgents []adk.Agent

	// ChatModel injects a model and skips OpenAI construction (primarily for tests).
	ChatModel model.BaseModel[*schema.Message]
	// Tools injects tools and skips Hub RelayTools construction (primarily for tests).
	Tools []tool.BaseTool

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
	shutdownTracing func(context.Context) error
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
	cfg = applyConfigDefaults(cfg, localOnly && !useInjectedTools)

	var err error
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

	tools := cfg.Tools
	if !useInjectedTools && !localOnly {
		tools, err = (&RelayTools{Hub: hub, MasterSession: cfg.MasterSession}).Build()
		if err != nil {
			_ = closeHub(hub)
			_ = shutdownTracing(context.Background())
			return nil, err
		}
	}
	toolsConfig := adk.ToolsConfig{
		ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
	}

	agentImpl, err := buildAgent(ctx, cfg, chatModel, toolsConfig, localOnly && !useInjectedTools)
	if err != nil {
		_ = closeHub(hub)
		_ = shutdownTracing(context.Background())
		return nil, err
	}

	return &Master{
		Runner:          adk.NewRunner(ctx, adk.RunnerConfig{Agent: agentImpl}),
		Hub:             hub,
		LocalOnly:       localOnly && !useInjectedTools,
		shutdownTracing: shutdownTracing,
	}, nil
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

func buildAgent(
	ctx context.Context,
	cfg Config,
	chatModel model.BaseModel[*schema.Message],
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
		localSubAgents, err := buildLocalSubAgents(ctx, cfg, chatModel)
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

// Close releases Hub and tracing resources.
func (m *Master) Close() error {
	if m == nil {
		return nil
	}
	var err error
	if m.Hub != nil {
		err = m.Hub.Close()
	}
	if m.shutdownTracing != nil {
		_ = m.shutdownTracing(context.Background())
	}
	return err
}

// Run executes one user goal and returns the final assistant text.
// Pass WithVerbose(os.Stderr) to print the full agent interaction flow.
func (m *Master) Run(ctx context.Context, goal string, opts ...RunOption) (string, error) {
	cfg := applyRunOptions(opts)
	if cfg.verbose != nil {
		fmt.Fprintf(cfg.verbose, "=== master run start local_only=%v goal=%q ===\n", m.LocalOnly, goal)
	}

	iter := m.Runner.Query(ctx, goal)
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
		fmt.Fprintf(cfg.verbose, "=== master run end steps=%d ===\n", step)
	}
	if last == "" {
		return "", fmt.Errorf("agent produced no final message")
	}
	return last, nil
}
