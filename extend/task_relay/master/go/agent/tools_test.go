package agent_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent"
)

func TestNewLocalPlannerSubAgentRequiresModel(t *testing.T) {
	_, err := agent.NewLocalPlannerSubAgent(context.Background(), nil)
	require.Error(t, err)
}

func TestRelayToolsBuild(t *testing.T) {
	tools, err := (&agent.RelayTools{Hub: nil}).Build()
	require.Error(t, err)
	require.Nil(t, tools)
}
