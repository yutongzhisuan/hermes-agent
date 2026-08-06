package router_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/hub/internal/router"
	"github.com/infa/task_relay/hub/internal/store"
)

func TestCancelPendingTask(t *testing.T) {
	mem := store.NewMemory()
	r := router.NewRouter(mem, nil, router.DefaultRouterConfig())
	ctx := context.Background()

	_, err := r.DispatchTask(ctx, router.TaskSpec{
		TaskID: "cancel-1",
		Goal:   "cancel me",
	}, "test-session", false)
	require.NoError(t, err)

	resp, err := r.Cancel(ctx, "cancel-1", "test cancel", 0)
	require.NoError(t, err)
	require.False(t, resp.IdempotentHit)
	require.Equal(t, router.StatusCancelled, resp.Status)

	stored, err := mem.GetTask(ctx, "cancel-1")
	require.NoError(t, err)
	require.Equal(t, router.StatusCancelled, stored.Status)
	require.Equal(t, "test cancel", stored.Summary)
}

func TestCancelRunningTaskEntersCancelling(t *testing.T) {
	mem := store.NewMemory()
	ctx := context.Background()
	task := &router.Task{
		TaskID: "cancel-2", Goal: "run", CallbackTopic: "topic",
		Status: router.StatusRunning, Attempt: 1, CreatedAt: time.Unix(1_700_000_000, 0),
	}
	require.NoError(t, mem.InsertTask(ctx, task))

	resp, err := router.NewRouter(mem, nil, router.DefaultRouterConfig()).Cancel(ctx, "cancel-2", "stop", 0)
	require.NoError(t, err)
	require.Equal(t, router.StatusCancelling, resp.Status)
}
