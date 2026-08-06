package router_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/store"
)

func TestDispatchTaskBatchIdempotent(t *testing.T) {
	mem := store.NewMemory()
	r := router.NewRouter(mem, nil, router.DefaultRouterConfig())
	ctx := context.Background()
	specs := []router.TaskSpec{
		{TaskID: "b1-t1", Goal: "one", CallbackTopic: "batch-topic"},
		{TaskID: "b1-t2", Goal: "two", CallbackTopic: "batch-topic"},
	}

	first, err := r.DispatchTaskBatch(ctx, "batch-1", "batch-topic", "", "sess", specs, false)
	require.NoError(t, err)
	require.False(t, first.IdempotentHit)
	require.Len(t, first.Tasks, 2)

	second, err := r.DispatchTaskBatch(ctx, "batch-1", "batch-topic", "", "sess", specs, false)
	require.NoError(t, err)
	require.True(t, second.IdempotentHit)
	require.Len(t, second.Tasks, 2)

	_, err = r.DispatchTaskBatch(ctx, "batch-1", "batch-topic", "", "sess", []router.TaskSpec{
		{TaskID: "b1-t1", Goal: "changed", CallbackTopic: "batch-topic"},
	}, false)
	require.Error(t, err)
}

func TestDispatchTaskBatchSetsBatchIDOnTasks(t *testing.T) {
	mem := store.NewMemory()
	r := router.NewRouter(mem, nil, router.DefaultRouterConfig())
	ctx := context.Background()
	_, err := r.DispatchTaskBatch(ctx, "batch-2", "topic", "", "sess", []router.TaskSpec{
		{TaskID: "b2-t1", Goal: "goal"},
	}, false)
	require.NoError(t, err)

	task, err := r.GetTask(ctx, "b2-t1")
	require.NoError(t, err)
	require.Equal(t, "batch-2", task.BatchID)
}
