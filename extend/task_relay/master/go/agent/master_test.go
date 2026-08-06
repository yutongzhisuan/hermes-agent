package agent_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent"
)

func TestMasterNewRequiresHubAddr(t *testing.T) {
	_, err := agent.New(context.Background(), agent.Config{
		OpenAIAPIKey: "test-key",
	})
	require.Error(t, err)
}

func TestMasterNewRequiresAPIKey(t *testing.T) {
	_, err := agent.New(context.Background(), agent.Config{
		HubAddr: "127.0.0.1:1",
	})
	require.Error(t, err)
}

func TestMasterCloseNilSafe(t *testing.T) {
	var m *agent.Master
	require.NoError(t, m.Close())
}

func TestDefaultModeIsDeep(t *testing.T) {
	require.Equal(t, agent.Mode("deep"), agent.ModeDeep)
	require.Equal(t, agent.Mode("react"), agent.ModeReAct)
}
