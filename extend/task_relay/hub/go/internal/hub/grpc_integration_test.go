package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/registry"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/testutil"
	"github.com/infa/xhermes-agent/extend/task_relay/master/go/client"
)

func TestGoHubGRPCDispatchTask(t *testing.T) {
	h, dialer := testutil.StartTestHubGRPC(t)
	ctx, cancel := testutil.TestContext(t, 5*time.Second)
	defer cancel()

	master := testutil.NewMasterClient(t, ctx, dialer, testutil.IssueMasterJWT(t, h, "master-1"))

	resp, err := master.DispatchTask(ctx, &pb.TaskSpec{
		TaskId:        "go-hub-1",
		Goal:          "dispatch via go hub",
		CallbackTopic: "topic-go-hub",
	}, "sess", false)
	require.NoError(t, err)
	require.False(t, resp.IdempotentHit)
	require.Equal(t, pb.TaskStatus_TASK_STATUS_PENDING, resp.Status)

	again, err := master.DispatchTask(ctx, &pb.TaskSpec{
		TaskId: "go-hub-1",
		Goal:   "dispatch via go hub",
	}, "sess", false)
	require.NoError(t, err)
	require.True(t, again.IdempotentHit)
}

func TestGoHubGRPCGetTaskResult(t *testing.T) {
	h, dialer := testutil.StartTestHubGRPC(t)
	ctx, cancel := testutil.TestContext(t, 5*time.Second)
	defer cancel()

	master := testutil.NewMasterClient(t, ctx, dialer, testutil.IssueMasterJWT(t, h, "master-1"))

	_, err := master.DispatchTask(ctx, &pb.TaskSpec{
		TaskId: "go-hub-result-1",
		Goal:   "get result",
	}, "sess", false)
	require.NoError(t, err)

	pending, err := master.GetTaskResult(ctx, "go-hub-result-1", false)
	require.NoError(t, err)
	require.Equal(t, pb.TaskStatus_TASK_STATUS_PENDING, pending.Status)
	require.Equal(t, "go-hub-result-1", pending.TaskId)

	_, err = h.Router().Complete(ctx, "go-hub-result-1", router.StatusLost, "worker timeout", router.CompleteInput{})
	require.NoError(t, err)

	completed, err := master.GetTaskResult(ctx, "go-hub-result-1", false)
	require.NoError(t, err)
	require.Equal(t, pb.TaskStatus_TASK_STATUS_LOST, completed.Status)
	require.Equal(t, "worker timeout", completed.Summary)
}

func TestGoHubGRPCRequiresMasterJWT(t *testing.T) {
	_, dialer := testutil.StartTestHubGRPC(t)
	ctx, cancel := testutil.TestContext(t, 5*time.Second)
	defer cancel()

	master := testutil.NewMasterClient(t, ctx, dialer, "")
	_, err := master.DispatchTask(ctx, &pb.TaskSpec{TaskId: "no-jwt"}, "sess", false)
	require.Error(t, err)
}

func TestGoHubGRPCCancelTask(t *testing.T) {
	h, dialer := testutil.StartTestHubGRPC(t)
	ctx, cancel := testutil.TestContext(t, 5*time.Second)
	defer cancel()

	master := testutil.NewMasterClient(t, ctx, dialer, testutil.IssueMasterJWT(t, h, "master-1"))

	_, err := master.DispatchTask(ctx, &pb.TaskSpec{
		TaskId: "go-hub-cancel-1",
		Goal:   "cancel me",
	}, "sess", false)
	require.NoError(t, err)

	resp, err := master.CancelTask(ctx, &pb.CancelTaskRequest{
		TaskId: "go-hub-cancel-1",
		Reason: "test cancel",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"go-hub-cancel-1"}, resp.CancelledTaskIds)

	result, err := master.GetTaskResult(ctx, "go-hub-cancel-1", false)
	require.NoError(t, err)
	require.Equal(t, pb.TaskStatus_TASK_STATUS_CANCELLED, result.Status)
}

func TestGoHubGRPCWatchTask(t *testing.T) {
	h, dialer := testutil.StartTestHubGRPC(t)
	ctx, cancel := testutil.TestContext(t, 5*time.Second)
	defer cancel()

	master := testutil.NewMasterClient(t, ctx, dialer, testutil.IssueMasterJWT(t, h, "master-1"))

	taskID := "go-hub-watch-1"
	_, err := master.DispatchTask(ctx, &pb.TaskSpec{
		TaskId:        taskID,
		Goal:          "watch me",
		CallbackTopic: "watch-topic",
	}, "sess", false)
	require.NoError(t, err)

	stream, err := master.Watch(ctx, client.WatchFilter{TaskID: taskID})
	require.NoError(t, err)

	first, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, pb.TaskEventKind_TASK_EVENT_KIND_STATUS, first.Kind)
	require.Equal(t, taskID, first.TaskId)

	_, err = master.CancelTask(ctx, &pb.CancelTaskRequest{
		TaskId: taskID,
		Reason: "done watching",
	})
	require.NoError(t, err)

	second, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, pb.TaskEventKind_TASK_EVENT_KIND_TERMINAL, second.Kind)
}

func TestGoHubGRPCListTasks(t *testing.T) {
	h, dialer := testutil.StartTestHubGRPC(t)
	ctx, cancel := testutil.TestContext(t, 5*time.Second)
	defer cancel()

	master := testutil.NewMasterClient(t, ctx, dialer, testutil.IssueMasterJWT(t, h, "master-1"))

	_, err := master.DispatchTask(ctx, &pb.TaskSpec{
		TaskId:        "list-task-1",
		Goal:          "listed",
		CallbackTopic: "list-topic",
	}, "sess", false)
	require.NoError(t, err)

	list, err := master.ListTasks(ctx, &pb.ListTasksRequest{
		CallbackTopic: "list-topic",
		Limit:         10,
	})
	require.NoError(t, err)
	require.Len(t, list.Tasks, 1)
	require.Equal(t, "list-task-1", list.Tasks[0].TaskId)
}

func TestGoHubGRPCDispatchTaskBatch(t *testing.T) {
	h, dialer := testutil.StartTestHubGRPC(t)
	ctx, cancel := testutil.TestContext(t, 5*time.Second)
	defer cancel()

	master := testutil.NewMasterClient(t, ctx, dialer, testutil.IssueMasterJWT(t, h, "master-1"))

	batch, err := master.DispatchTaskBatch(ctx, &pb.DispatchTaskBatchRequest{
		BatchId:       "go-batch-1",
		CallbackTopic: "batch-topic",
		Specs: []*pb.TaskSpec{
			{TaskId: "go-batch-1-t1", Goal: "first"},
			{TaskId: "go-batch-1-t2", Goal: "second"},
		},
	})
	require.NoError(t, err)
	require.False(t, batch.IdempotentHit)
	require.Len(t, batch.Tasks, 2)

	again, err := master.DispatchTaskBatch(ctx, &pb.DispatchTaskBatchRequest{
		BatchId:       "go-batch-1",
		CallbackTopic: "batch-topic",
		Specs: []*pb.TaskSpec{
			{TaskId: "go-batch-1-t1", Goal: "first"},
			{TaskId: "go-batch-1-t2", Goal: "second"},
		},
	})
	require.NoError(t, err)
	require.True(t, again.IdempotentHit)
	require.Len(t, again.Tasks, 2)
}

func TestGoHubGRPCCancelBatch(t *testing.T) {
	h, dialer := testutil.StartTestHubGRPC(t)
	ctx, cancel := testutil.TestContext(t, 5*time.Second)
	defer cancel()

	master := testutil.NewMasterClient(t, ctx, dialer, testutil.IssueMasterJWT(t, h, "master-1"))

	_, err := master.DispatchTaskBatch(ctx, &pb.DispatchTaskBatchRequest{
		BatchId:       "go-batch-cancel",
		CallbackTopic: "cancel-topic",
		Specs: []*pb.TaskSpec{
			{TaskId: "go-batch-cancel-t1", Goal: "one"},
			{TaskId: "go-batch-cancel-t2", Goal: "two"},
		},
	})
	require.NoError(t, err)

	resp, err := master.CancelTask(ctx, &pb.CancelTaskRequest{
		BatchId: "go-batch-cancel",
		Reason:  "batch cancel",
	})
	require.NoError(t, err)
	require.Len(t, resp.CancelledTaskIds, 2)
}

func TestGoHubGRPCListWorkers(t *testing.T) {
	h, dialer := testutil.StartTestHubGRPC(t)
	h.Registry().Announce(context.Background(), registry.AnnounceInput{
		WorkerID:      "worker-a",
		SessionModes:  []string{"A", "C"},
		MaxConcurrent: 2,
		Toolsets:      []string{"terminal", "file"},
		Capabilities:  map[string]any{"os": "linux", "region": "ap-southeast-1"},
	})

	ctx, cancel := testutil.TestContext(t, 5*time.Second)
	defer cancel()

	master := testutil.NewMasterClient(t, ctx, dialer, testutil.IssueMasterJWT(t, h, "master-1"))

	list, err := master.ListWorkers(ctx, &pb.ListWorkersRequest{
		RequireToolsets: []string{"terminal"},
	})
	require.NoError(t, err)
	require.Len(t, list.Workers, 1)
	require.Equal(t, "worker-a", list.Workers[0].WorkerId)
	require.Equal(t, "ap-southeast-1", list.Workers[0].Region)
}

func TestGoHubGRPCGetTaskResultWithCheckpoint(t *testing.T) {
	h, dialer := testutil.StartTestHubGRPC(t)
	ctx, cancel := testutil.TestContext(t, 5*time.Second)
	defer cancel()

	master := testutil.NewMasterClient(t, ctx, dialer, testutil.IssueMasterJWT(t, h, "master-1"))

	taskID := "go-hub-checkpoint-1"
	_, err := master.DispatchTask(ctx, &pb.TaskSpec{
		TaskId: taskID,
		Goal:   "checkpoint task",
	}, "sess", false)
	require.NoError(t, err)

	h.Registry().Announce(ctx, registry.AnnounceInput{
		WorkerID:      "worker-cp",
		SessionModes:  []string{"A"},
		MaxConcurrent: 1,
	})
	claimed, err := h.Router().ClaimForWorker(ctx, taskID, "worker-cp", nil)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	require.NoError(t, h.Router().OnCheckpoint(ctx, taskID, "ckpt-go-1", "saved", "", []byte("blob")))

	result, err := master.GetTaskResult(ctx, taskID, true)
	require.NoError(t, err)
	require.Equal(t, "ckpt-go-1", result.LatestCheckpointId)
	require.Equal(t, pb.TaskStatus_TASK_STATUS_RUNNING, result.Status)
}
