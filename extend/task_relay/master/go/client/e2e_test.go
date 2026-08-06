//go:build integration

package client_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
	"github.com/infa/xhermes-agent/extend/task_relay/master/go/client"
	"github.com/infa/xhermes-agent/extend/task_relay/master/go/internal/testutil"
	"github.com/infa/xhermes-agent/extend/task_relay/master/go/join"
)

func TestGoMasterDispatchWatchTerminal(t *testing.T) {
	env := testutil.RequireHubEnv(t)
	ctx, cancel := testutil.TestContext(t, 30*time.Second)
	defer cancel()

	hub := testutil.NewHubClient(t, ctx, env)

	taskID := "go-e2e-1"
	topic := "go-e2e-topic"
	_, err := hub.DispatchTask(ctx, &pb.TaskSpec{
		TaskId:        taskID,
		Goal:          "go master sdk e2e",
		CallbackTopic: topic,
	}, "go-e2e-session", false)
	require.NoError(t, err)

	stream, err := hub.Watch(ctx, client.WatchFilter{Topic: topic})
	require.NoError(t, err)

	snap, err := client.CollectTerminals(ctx, stream, []string{taskID})
	require.NoError(t, err)

	result, ok := snap.Results[taskID]
	require.True(t, ok, "missing terminal result for %s", taskID)
	require.Equal(t, pb.TaskStatus_TASK_STATUS_COMPLETED, result.Status)
}

func TestGoMasterBatchJoinAll(t *testing.T) {
	env := testutil.RequireHubEnv(t)
	ctx, cancel := testutil.TestContext(t, 30*time.Second)
	defer cancel()

	hub := testutil.NewHubClient(t, ctx, env)

	topic := "go-e2e-batch"
	batchID := "go-batch-1"
	taskIDs := []string{"go-e2e-b1", "go-e2e-b2"}
	specs := make([]*pb.TaskSpec, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		specs = append(specs, &pb.TaskSpec{
			TaskId:        taskID,
			Goal:          "go master batch join",
			CallbackTopic: topic,
		})
	}

	_, err := hub.DispatchTaskBatch(ctx, &pb.DispatchTaskBatchRequest{
		BatchId:         batchID,
		Specs:           specs,
		MasterSessionId: "go-e2e-session",
		CallbackTopic:   topic,
	})
	require.NoError(t, err)

	outcome, err := join.JoinBatch(
		ctx,
		hub,
		client.WatchFilter{Topic: topic},
		taskIDs,
		join.Policy{Mode: join.ModeAll},
	)
	require.NoError(t, err)
	require.True(t, outcome.Satisfied)
	require.Len(t, outcome.Results, len(taskIDs))

	for _, taskID := range taskIDs {
		result, ok := outcome.Results[taskID]
		require.True(t, ok, "missing result for %s", taskID)
		require.Equal(t, pb.TaskStatus_TASK_STATUS_COMPLETED, result.Status)
	}
}
