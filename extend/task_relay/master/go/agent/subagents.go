package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const localPlannerInstruction = `You are a local planning subagent.
Break down the assigned sub-goal into clear steps, constraints, and suggested next actions.
Do not call remote workers yourself; return a structured plan the Master can execute locally or via Relay tools when available.`

// NewLocalPlannerSubAgent creates a local-only subagent for decomposition and planning.
func NewLocalPlannerSubAgent(ctx context.Context, chatModel model.BaseModel[*schema.Message]) (adk.Agent, error) {
	if chatModel == nil {
		return nil, fmt.Errorf("chat model is required")
	}
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "local-planner",
		Description:   "Local subagent for goal decomposition and task-spec drafting without remote execution",
		Instruction:   localPlannerInstruction,
		Model:         chatModel,
		MaxIterations: 10,
	})
}

// buildLocalSubAgents returns optional specialized local subagents (no Relay tools).
func buildLocalSubAgents(
	ctx context.Context,
	cfg Config,
	chatModel model.BaseModel[*schema.Message],
) ([]adk.Agent, error) {
	subAgents := make([]adk.Agent, 0, len(cfg.SubAgents)+1)
	subAgents = append(subAgents, cfg.SubAgents...)
	if cfg.DisableLocalPlanner {
		return subAgents, nil
	}
	planner, err := NewLocalPlannerSubAgent(ctx, chatModel)
	if err != nil {
		return nil, fmt.Errorf("local planner subagent: %w", err)
	}
	return append(subAgents, planner), nil
}
