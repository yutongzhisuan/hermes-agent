package router_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/hub/internal/router"
	"github.com/infa/task_relay/hub/internal/store"
)

func TestTickTimeoutsQueueTimeout(t *testing.T) {
	mem := store.NewMemory()
	r := router.NewRouter(mem, nil, router.DefaultRouterConfig())
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)

	task := &router.Task{
		TaskID: "q1", Goal: "wait", CallbackTopic: "topic-1",
		Status: router.StatusPending, CreatedAt: past, QueueDeadlineAt: past,
	}
	require.NoError(t, mem.InsertTask(ctx, task))

	require.NoError(t, r.TickTimeouts(ctx))

	stored, err := mem.GetTask(ctx, "q1")
	require.NoError(t, err)
	require.Equal(t, router.StatusLost, stored.Status)
	require.Equal(t, "queue timeout", stored.Summary)
}

func TestTickTimeoutsFirstProgressTimeout(t *testing.T) {
	mem := store.NewMemory()
	r := router.NewRouter(mem, nil, router.DefaultRouterConfig())
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)

	task := &router.Task{
		TaskID: "fp1", Goal: "run", CallbackTopic: "topic-1",
		Status: router.StatusRunning, Attempt: 1, CreatedAt: past,
		FirstProgressDeadlineAt: past,
	}
	require.NoError(t, mem.InsertTask(ctx, task))

	require.NoError(t, r.TickTimeouts(ctx))

	stored, err := mem.GetTask(ctx, "fp1")
	require.NoError(t, err)
	require.Equal(t, router.StatusLost, stored.Status)
	require.Equal(t, "first progress timeout", stored.Summary)
}

func TestTickTimeoutsLeaseEntersCancelling(t *testing.T) {
	mem := store.NewMemory()
	cfg := router.DefaultRouterConfig()
	cfg.CancelGraceSeconds = 30
	r := router.NewRouter(mem, nil, cfg)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)

	task := &router.Task{
		TaskID: "lease1", Goal: "run", CallbackTopic: "topic-1",
		Status: router.StatusRunning, Attempt: 1, CreatedAt: past,
		ClaimExpiresAt: past,
	}
	require.NoError(t, mem.InsertTask(ctx, task))

	require.NoError(t, r.TickTimeouts(ctx))

	stored, err := mem.GetTask(ctx, "lease1")
	require.NoError(t, err)
	require.Equal(t, router.StatusCancelling, stored.Status)
	require.Equal(t, "execution timeout", stored.CancelReason)
}
