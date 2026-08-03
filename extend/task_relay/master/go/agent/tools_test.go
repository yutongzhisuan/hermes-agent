package agent_test

import (
	"context"
	"testing"

	"github.com/infa/xhermes-agent/extend/task_relay/master/go/agent"
)

func TestNewLocalPlannerSubAgentRequiresModel(t *testing.T) {
	_, err := agent.NewLocalPlannerSubAgent(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil model")
	}
}

func TestRelayToolsBuild(t *testing.T) {
	tools, err := (&agent.RelayTools{Hub: nil}).Build()
	if err == nil || tools != nil {
		t.Fatalf("expected error when hub is nil")
	}
}

func TestDefaultModeIsDeep(t *testing.T) {
	if agent.ModeDeep != "deep" || agent.ModeReAct != "react" {
		t.Fatalf("unexpected mode constants")
	}
}
