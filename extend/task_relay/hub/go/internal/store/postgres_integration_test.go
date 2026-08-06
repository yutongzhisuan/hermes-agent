//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/hub/internal/registry"
	"github.com/infa/task_relay/hub/internal/router"
	"github.com/infa/task_relay/hub/internal/testutil"
)

func TestPostgresInsertAndGetTask(t *testing.T) {
	st := testutil.OpenTestPostgres(t)
	ctx := context.Background()
	now := time.Unix(100, 0).UTC()
	task := &router.Task{
		TaskID: "pg-task-1", Goal: "goal", CallbackTopic: "topic", Status: router.StatusPending,
		CreatedAt: now, Attempt: 1, MaxAttempts: 3,
	}
	require.NoError(t, st.InsertTask(ctx, task))

	loaded, err := st.GetTask(ctx, "pg-task-1")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, "goal", loaded.Goal)
}

func TestPostgresDispatchClaimComplete(t *testing.T) {
	st := testutil.OpenTestPostgres(t)
	ctx := context.Background()
	reg := registry.New(nil)
	rt := router.NewRouter(st, registry.NewRouterAdapter(reg), router.DefaultRouterConfig())
	reg.Announce(ctx, registry.AnnounceInput{
		WorkerID: "pg-w1", SessionModes: []string{"A"}, MaxConcurrent: 1,
		OnlineSessionID: "sess-1",
	})

	resp, err := rt.DispatchTask(ctx, router.TaskSpec{
		TaskID: "pg-dispatch-1", Goal: "execute", CallbackTopic: "pg-topic",
	}, "m1", false)
	require.NoError(t, err)
	require.Equal(t, "pg-dispatch-1", resp.TaskID)

	claimed, err := rt.ClaimForPoll(ctx, "pg-w1", 1, &router.WorkerClaims{MaxConcurrent: 1})
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	require.NoError(t, rt.OnProgress(ctx, "pg-dispatch-1", "started"))
	_, err = rt.Complete(ctx, "pg-dispatch-1", router.StatusCompleted, "ok", router.CompleteInput{})
	require.NoError(t, err)

	task, err := st.GetTask(ctx, "pg-dispatch-1")
	require.NoError(t, err)
	require.Equal(t, router.StatusCompleted, task.Status)
	require.Equal(t, "ok", task.Summary)
}

func TestPostgresListTasksByWorkerAndStatus(t *testing.T) {
	st := testutil.OpenTestPostgres(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, st.InsertTask(ctx, &router.Task{
		TaskID: "pg-cancel-1", Goal: "g", CallbackTopic: "t", Status: router.StatusCancelling,
		WorkerID: "pg-w1", CancelReason: "stop", ClaimExpiresAt: now.Add(time.Minute), CreatedAt: now,
	}))

	tasks, err := st.ListTasks(ctx, router.ListTasksQuery{
		WorkerID: "pg-w1", Statuses: []string{router.StatusCancelling}, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "pg-cancel-1", tasks[0].TaskID)
}
