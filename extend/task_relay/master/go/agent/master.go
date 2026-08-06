package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/compose"
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

// Mode selects the Eino ADK agent implementation.
type Mode string

const (
	// ModeDeep uses DeepAgent: write_todos + local subagents (task tool) + Relay tools.
	ModeDeep Mode = "deep"
	// ModeReAct uses ChatModelAgent with ToolsConfig (ReAct loop, no DeepAgent helpers).
	ModeReAct Mode = "react"
)

// Config holds Hub, auth, LLM, and agent-mode settings.
type Config struct {
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

	HubTLS        client.TLSConfig
	EnableMetrics bool
	MetricsAddr   string
	EnableTracing bool
	OTelEndpoint  string
}

// Master wraps an Eino ADK Runner backed by Task Relay tools.
type Master struct {
	Runner          *adk.Runner
	Hub             *client.Client
	shutdownTracing func(context.Context) error
}

// New dials the Hub, builds Relay tools, and returns a DeepAgent or ReAct agent runner.
func New(ctx context.Context, cfg Config) (*Master, error) {
	if cfg.HubAddr == "" {
		return nil, fmt.Errorf("hub address is required")
	}
	var err error
	if cfg.OpenAIAPIKey == "" {
		return nil, fmt.Errorf("openai api key is required")
	}
	if cfg.OpenAIModel == "" {
		cfg.OpenAIModel = "gpt-4o-mini"
	}
	if cfg.MasterSession == "" {
		cfg.MasterSession = "master-session"
	}
	if cfg.Instruction == "" {
		cfg.Instruction = defaultInstruction
	}
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = 20
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeDeep
	}

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

	hub, err := client.New(ctx, client.Config{
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

	modelCfg := &openai.ChatModelConfig{
		APIKey: cfg.OpenAIAPIKey,
		Model:  cfg.OpenAIModel,
	}
	if cfg.OpenAIBaseURL != "" {
		modelCfg.BaseURL = cfg.OpenAIBaseURL
	}
	chatModel, err := openai.NewChatModel(ctx, modelCfg)
	if err != nil {
		_ = hub.Close()
		_ = shutdownTracing(context.Background())
		return nil, fmt.Errorf("openai chat model: %w", err)
	}

	tools, err := (&RelayTools{Hub: hub, MasterSession: cfg.MasterSession}).Build()
	if err != nil {
		_ = hub.Close()
		_ = shutdownTracing(context.Background())
		return nil, err
	}
	toolsConfig := adk.ToolsConfig{
		ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
	}

	agentImpl, err := buildAgent(ctx, cfg, chatModel, toolsConfig)
	if err != nil {
		_ = hub.Close()
		_ = shutdownTracing(context.Background())
		return nil, err
	}

	return &Master{
		Runner:          adk.NewRunner(ctx, adk.RunnerConfig{Agent: agentImpl}),
		Hub:             hub,
		shutdownTracing: shutdownTracing,
	}, nil
}

func buildAgent(
	ctx context.Context,
	cfg Config,
	chatModel *openai.ChatModel,
	toolsConfig adk.ToolsConfig,
) (adk.Agent, error) {
	switch cfg.Mode {
	case ModeReAct:
		return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name:          "task-relay-master",
			Description:   "ReAct Master with local reasoning and remote Relay worker orchestration",
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
		return deep.New(ctx, &deep.Config{
			Name:                   "task-relay-master",
			Description:            "Deep Master coordinating local subagents and remote XHermes workers via Task Relay",
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
func (m *Master) Run(ctx context.Context, goal string) (string, error) {
	iter := m.Runner.Query(ctx, goal)
	var last string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return last, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		if msg := event.Output.MessageOutput.Message; msg != nil && msg.Content != "" {
			last = msg.Content
		}
	}
	if last == "" {
		return "", fmt.Errorf("agent produced no final message")
	}
	return last, nil
}
