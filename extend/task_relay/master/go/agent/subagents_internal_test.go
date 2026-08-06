package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildLocalSubAgentsSkipsPlannerWhenDisabled(t *testing.T) {
	agents, err := buildLocalSubAgents(context.Background(), Config{
		DisableLocalPlanner: true,
	}, nil)
	require.NoError(t, err)
	require.Empty(t, agents)
}
