//go:build integration

package agent_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent"
	"github.com/infa/task_relay/master/internal/testutil"
)

func TestRelayToolsDispatchWatchJoin(t *testing.T) {
	env := testutil.RequireHubEnv(t)
	ctx, cancel := testutil.TestContext(t, 30*time.Second)
	defer cancel()

	hub := testutil.NewHubClient(t, ctx, env)

	tools, err := (&agent.RelayTools{
		Hub:           hub,
		MasterSession: "tools-integration",
	}).Build()
	require.NoError(t, err)
	require.Len(t, tools, 5)

	toolMap := testutil.BuildToolMap(t, tools)

	taskID := "go-tools-1"
	topic := "go-tools-topic"
	dispatchOut := testutil.InvokeTool[agent.DispatchTaskOutput](ctx, t, toolMap["dispatch_task"], agent.DispatchTaskInput{
		TaskID:        taskID,
		Goal:          "relay tools integration",
		CallbackTopic: topic,
	})
	require.Equal(t, taskID, dispatchOut.TaskID)

	joinOut := testutil.InvokeTool[agent.WatchJoinOutput](ctx, t, toolMap["watch_and_join"], agent.WatchJoinInput{
		CallbackTopic: topic,
		TaskIDs:       []string{taskID},
		JoinMode:      "ALL",
	})
	require.True(t, joinOut.Satisfied)
	require.Len(t, joinOut.Results, 1)
	require.Equal(t, testutil.TaskStatusCompleted, joinOut.Results[0].Status)
}
