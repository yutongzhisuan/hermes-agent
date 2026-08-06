package router_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/hub/internal/router"
	"github.com/infa/task_relay/hub/internal/store"
)

func TestOnCheckpointPersistsAndExtendsLease(t *testing.T) {
	mem := store.NewMemory()
	r := router.NewRouter(mem, nil, router.DefaultRouterConfig())
	ctx := context.Background()
	now := time.Now()

	task := &router.Task{
		TaskID: "cp1", Goal: "run", CallbackTopic: "topic-1",
		Status: router.StatusRunning, Attempt: 1, CreatedAt: now,
		FirstProgressDeadlineAt: now.Add(time.Minute),
		ClaimExpiresAt:          now.Add(time.Minute),
	}
	require.NoError(t, mem.InsertTask(ctx, task))

	require.NoError(t, r.OnCheckpoint(ctx, "cp1", "ckpt-1", "half done", "", []byte("blob")))

	stored, err := mem.GetTask(ctx, "cp1")
	require.NoError(t, err)
	require.True(t, stored.FirstProgressDeadlineAt.IsZero())
	require.True(t, stored.ClaimExpiresAt.After(now.Add(30*time.Second)))

	checkpoint, err := r.GetLatestCheckpoint(ctx, "cp1")
	require.NoError(t, err)
	require.NotNil(t, checkpoint)
	require.Equal(t, "ckpt-1", checkpoint.CheckpointID)
	require.Equal(t, "half done", checkpoint.Summary)
}
