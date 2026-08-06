package router_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/store"
)

func TestDispatchTaskIdempotent(t *testing.T) {
	mem := store.NewMemory()
	r := router.NewRouter(mem, nil, router.DefaultRouterConfig())
	ctx := context.Background()
	spec := router.TaskSpec{TaskID: "t1", Goal: "hello", CallbackTopic: "topic-1"}

	first, err := r.DispatchTask(ctx, spec, "test-session", false)
	require.NoError(t, err)
	require.False(t, first.IdempotentHit)

	second, err := r.DispatchTask(ctx, spec, "test-session", false)
	require.NoError(t, err)
	require.True(t, second.IdempotentHit)
	require.Equal(t, router.StatusPending, second.Status)
}

func TestCompleteTerminalTransition(t *testing.T) {
	mem := store.NewMemory()
	ctx := context.Background()

	task := &router.Task{
		TaskID: "t2", Goal: "run", CallbackTopic: "topic-1",
		Status: router.StatusRunning, Attempt: 1, CreatedAt: time.Unix(1_700_000_000, 0),
	}
	require.NoError(t, mem.InsertTask(ctx, task))

	resp, err := router.NewRouter(mem, nil, router.DefaultRouterConfig()).Complete(ctx, "t2", router.StatusCompleted, "done", router.CompleteInput{})
	require.NoError(t, err)
	require.False(t, resp.IdempotentHit)
	require.Equal(t, router.StatusCompleted, resp.Status)

	stored, err := mem.GetTask(ctx, "t2")
	require.NoError(t, err)
	require.Equal(t, "done", stored.Summary)
	require.Equal(t, router.StatusCompleted, stored.Status)
}

func TestValidateTransitionRejectsIllegal(t *testing.T) {
	require.Error(t, router.ValidateTransition(router.StatusPending, router.StatusCompleted))
}
