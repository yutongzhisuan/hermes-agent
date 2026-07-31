package agent

import (
	"context"
	"testing"
)

func TestBuildLocalSubAgentsSkipsPlannerWhenDisabled(t *testing.T) {
	agents, err := buildLocalSubAgents(context.Background(), Config{
		DisableLocalPlanner: true,
	}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(agents) != 0 {
		t.Fatalf("expected no subagents, got %d", len(agents))
	}
}
