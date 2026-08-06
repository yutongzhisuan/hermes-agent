//go:build integration

package agent_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/xhermes-agent/extend/task_relay/master/go/agent"
	"github.com/infa/xhermes-agent/extend/task_relay/master/go/internal/testutil"
)

const workflowSession = "master-workflow-test"

// TestMasterAgentWorkflowCompleteness exercises the full Relay orchestration path
// that the Master Agent is expected to follow: plan-like batch dispatch, watch/join,
// result fetch, idempotency, and cancellation.
func TestMasterAgentWorkflowCompleteness(t *testing.T) {
	env := testutil.RequireHubEnv(t)
	ctx, cancel := testutil.TestContext(t, 60*time.Second)
	defer cancel()

	hub := testutil.NewHubClient(t, ctx, env)

	tools, err := (&agent.RelayTools{
		Hub:           hub,
		MasterSession: workflowSession,
	}).Build()
	require.NoError(t, err)
	require.Len(t, tools, 5)
	toolMap := testutil.BuildToolMap(t, tools)

	t.Run("single_task_dispatch_watch_result", func(t *testing.T) {
		taskID := "wf-single-1"
		topic := "wf-single-topic"

		dispatchOut := testutil.InvokeTool[agent.DispatchTaskOutput](ctx, t, toolMap["dispatch_task"], agent.DispatchTaskInput{
			TaskID:        taskID,
			Goal:          "analyze priority queue scheduling",
			CallbackTopic: topic,
		})
		require.Equal(t, taskID, dispatchOut.TaskID)
		require.False(t, dispatchOut.IdempotentHit)

		joinOut := testutil.InvokeTool[agent.WatchJoinOutput](ctx, t, toolMap["watch_and_join"], agent.WatchJoinInput{
			CallbackTopic: topic,
			TaskIDs:       []string{taskID},
			JoinMode:      "ALL",
		})
		require.True(t, joinOut.Satisfied)
		require.Len(t, joinOut.Results, 1)
		require.Equal(t, testutil.TaskStatusCompleted, joinOut.Results[0].Status)

		resultOut := testutil.InvokeTool[agent.GetTaskResultOutput](ctx, t, toolMap["get_task_result"], agent.GetTaskResultInput{
			TaskID: taskID,
		})
		require.Equal(t, taskID, resultOut.TaskID)
		require.Equal(t, testutil.TaskStatusCompleted, resultOut.Status)
		require.NotEmpty(t, resultOut.Summary)
	})

	t.Run("batch_dispatch_join_all", func(t *testing.T) {
		batchID := "wf-batch-1"
		topic := "wf-batch-topic"
		taskIDs := []string{"wf-priority", "wf-fairshare", "wf-deadline"}
		goals := []string{
			"explain priority queue scheduling",
			"explain fair-share scheduling",
			"explain deadline-aware scheduling",
		}

		specs := make([]agent.BatchTaskSpec, len(taskIDs))
		for i, id := range taskIDs {
			specs[i] = agent.BatchTaskSpec{TaskID: id, Goal: goals[i]}
		}

		batchOut := testutil.InvokeTool[agent.DispatchBatchOutput](ctx, t, toolMap["dispatch_batch"], agent.DispatchBatchInput{
			BatchID:       batchID,
			CallbackTopic: topic,
			Tasks:         specs,
		})
		require.Equal(t, batchID, batchOut.BatchID)
		require.Len(t, batchOut.Tasks, len(taskIDs))

		joinOut := testutil.InvokeTool[agent.WatchJoinOutput](ctx, t, toolMap["watch_and_join"], agent.WatchJoinInput{
			CallbackTopic: topic,
			TaskIDs:       taskIDs,
			JoinMode:      "ALL",
		})
		require.True(t, joinOut.Satisfied)
		require.Len(t, joinOut.Results, len(taskIDs))
		for _, result := range joinOut.Results {
			require.Equal(t, testutil.TaskStatusCompleted, result.Status, "task %s", result.TaskID)
		}
	})

	t.Run("dispatch_idempotency", func(t *testing.T) {
		taskID := "wf-idem-1"
		topic := "wf-idem-topic"
		input := agent.DispatchTaskInput{
			TaskID:        taskID,
			Goal:          "idempotency probe",
			CallbackTopic: topic,
		}

		testutil.InvokeTool[agent.DispatchTaskOutput](ctx, t, toolMap["dispatch_task"], input)
		second := testutil.InvokeTool[agent.DispatchTaskOutput](ctx, t, toolMap["dispatch_task"], input)
		require.True(t, second.IdempotentHit)

		joinOut := testutil.InvokeTool[agent.WatchJoinOutput](ctx, t, toolMap["watch_and_join"], agent.WatchJoinInput{
			CallbackTopic: topic,
			TaskIDs:       []string{taskID},
		})
		require.True(t, joinOut.Satisfied)
	})

	t.Run("batch_idempotency", func(t *testing.T) {
		batchID := "wf-idem-batch"
		topic := "wf-idem-batch-topic"
		input := agent.DispatchBatchInput{
			BatchID:       batchID,
			CallbackTopic: topic,
			Tasks: []agent.BatchTaskSpec{
				{TaskID: "wf-idem-b1", Goal: "batch idempotency a"},
				{TaskID: "wf-idem-b2", Goal: "batch idempotency b"},
			},
		}

		testutil.InvokeTool[agent.DispatchBatchOutput](ctx, t, toolMap["dispatch_batch"], input)
		second := testutil.InvokeTool[agent.DispatchBatchOutput](ctx, t, toolMap["dispatch_batch"], input)
		require.Len(t, second.Tasks, 2)
		for i, ack := range second.Tasks {
			require.True(t, ack.IdempotentHit, "task %d", i)
		}

		taskIDs := []string{input.Tasks[0].TaskID, input.Tasks[1].TaskID}
		joinOut := testutil.InvokeTool[agent.WatchJoinOutput](ctx, t, toolMap["watch_and_join"], agent.WatchJoinInput{
			CallbackTopic: topic,
			TaskIDs:       taskIDs,
		})
		require.True(t, joinOut.Satisfied)
	})

	t.Run("join_mode_any", func(t *testing.T) {
		topic := "wf-any-topic"
		taskIDs := []string{"wf-any-1", "wf-any-2"}
		specs := make([]agent.BatchTaskSpec, len(taskIDs))
		for i, id := range taskIDs {
			specs[i] = agent.BatchTaskSpec{TaskID: id, Goal: "join any probe"}
		}

		testutil.InvokeTool[agent.DispatchBatchOutput](ctx, t, toolMap["dispatch_batch"], agent.DispatchBatchInput{
			BatchID:       "wf-any-batch",
			CallbackTopic: topic,
			Tasks:         specs,
		})

		joinOut := testutil.InvokeTool[agent.WatchJoinOutput](ctx, t, toolMap["watch_and_join"], agent.WatchJoinInput{
			CallbackTopic: topic,
			TaskIDs:       taskIDs,
			JoinMode:      "ANY",
		})
		require.True(t, joinOut.Satisfied)
		require.NotEmpty(t, joinOut.Results)

		completed := 0
		for _, result := range joinOut.Results {
			if result.Status == testutil.TaskStatusCompleted {
				completed++
			}
		}
		require.GreaterOrEqual(t, completed, 1)
	})

	t.Run("cancel_pending_task", func(t *testing.T) {
		taskID := "wf-cancel-pending"
		topic := "wf-cancel-pending-topic"

		dispatchOut := testutil.InvokeTool[agent.DispatchTaskOutput](ctx, t, toolMap["dispatch_task"], agent.DispatchTaskInput{
			TaskID:        taskID,
			Goal:          "should be cancelled before worker pickup",
			CallbackTopic: topic,
			TargetWorker:  "non-existent-worker",
		})
		require.Equal(t, testutil.TaskStatusPending, dispatchOut.Status)

		cancelOut := testutil.InvokeTool[agent.CancelTaskOutput](ctx, t, toolMap["cancel_task"], agent.CancelTaskInput{
			TaskID: taskID,
			Reason: "workflow test cancel pending",
		})
		require.Equal(t, []string{taskID}, cancelOut.CancelledTaskIDs)

		resultOut := testutil.InvokeTool[agent.GetTaskResultOutput](ctx, t, toolMap["get_task_result"], agent.GetTaskResultInput{
			TaskID: taskID,
		})
		require.Equal(t, testutil.TaskStatusCancelled, resultOut.Status)
	})

	t.Run("cancel_terminal_task", func(t *testing.T) {
		taskID := "wf-cancel-terminal"
		topic := "wf-cancel-terminal-topic"

		testutil.InvokeTool[agent.DispatchTaskOutput](ctx, t, toolMap["dispatch_task"], agent.DispatchTaskInput{
			TaskID:        taskID,
			Goal:          "complete then attempt cancel",
			CallbackTopic: topic,
		})
		testutil.InvokeTool[agent.WatchJoinOutput](ctx, t, toolMap["watch_and_join"], agent.WatchJoinInput{
			CallbackTopic: topic,
			TaskIDs:       []string{taskID},
		})

		cancelOut := testutil.InvokeTool[agent.CancelTaskOutput](ctx, t, toolMap["cancel_task"], agent.CancelTaskInput{
			TaskID: taskID,
			Reason: "workflow test cancel terminal",
		})
		require.Equal(t, []string{taskID}, cancelOut.AlreadyTerminalTaskIDs)
	})
}
