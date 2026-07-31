//go:build integration

package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/registry"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/store"
)

func openTestPostgres(t *testing.T) router.Store {
	t.Helper()
	url := os.Getenv("TASK_RELAY_TEST_PG")
	if url == "" {
		t.Skip("TASK_RELAY_TEST_PG not set")
	}
	pg, err := store.OpenPostgres(url)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	if err := store.TruncatePostgresTables(context.Background(), pg); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pg
}

func TestPostgresInsertAndGetTask(t *testing.T) {
	st := openTestPostgres(t)
	ctx := context.Background()
	now := time.Unix(100, 0).UTC()
	task := &router.Task{
		TaskID: "pg-task-1", Goal: "goal", CallbackTopic: "topic", Status: router.StatusPending,
		CreatedAt: now, Attempt: 1, MaxAttempts: 3,
	}
	if err := st.InsertTask(ctx, task); err != nil {
		t.Fatalf("insert: %v", err)
	}
	loaded, err := st.GetTask(ctx, "pg-task-1")
	if err != nil || loaded == nil || loaded.Goal != "goal" {
		t.Fatalf("get task: %+v err=%v", loaded, err)
	}
}

func TestPostgresDispatchClaimComplete(t *testing.T) {
	st := openTestPostgres(t)
	ctx := context.Background()
	reg := registry.New()
	rt := router.NewRouter(st, registry.NewRouterAdapter(reg), router.DefaultRouterConfig())
	reg.Announce(ctx, registry.AnnounceInput{
		WorkerID: "pg-w1", SessionModes: []string{"A"}, MaxConcurrent: 1,
		OnlineSessionID: "sess-1",
	})
	resp, err := rt.DispatchTask(ctx, router.TaskSpec{
		TaskID: "pg-dispatch-1", Goal: "execute", CallbackTopic: "pg-topic",
	}, "m1")
	if err != nil || resp.TaskID != "pg-dispatch-1" {
		t.Fatalf("dispatch: %+v err=%v", resp, err)
	}
	claimed, err := rt.ClaimForPoll(ctx, "pg-w1", 1, &router.WorkerClaims{MaxConcurrent: 1})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v err=%v", claimed, err)
	}
	if err := rt.OnProgress(ctx, "pg-dispatch-1", "started"); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if _, err := rt.Complete(ctx, "pg-dispatch-1", router.StatusCompleted, "ok"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	task, err := st.GetTask(ctx, "pg-dispatch-1")
	if err != nil || task.Status != router.StatusCompleted || task.Summary != "ok" {
		t.Fatalf("terminal task: %+v err=%v", task, err)
	}
}

func TestPostgresListTasksByWorkerAndStatus(t *testing.T) {
	st := openTestPostgres(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := st.InsertTask(ctx, &router.Task{
		TaskID: "pg-cancel-1", Goal: "g", CallbackTopic: "t", Status: router.StatusCancelling,
		WorkerID: "pg-w1", CancelReason: "stop", ClaimExpiresAt: now.Add(time.Minute), CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	tasks, err := st.ListTasks(ctx, router.ListTasksQuery{
		WorkerID: "pg-w1", Statuses: []string{router.StatusCancelling}, Limit: 10,
	})
	if err != nil || len(tasks) != 1 || tasks[0].TaskID != "pg-cancel-1" {
		t.Fatalf("list: %+v err=%v", tasks, err)
	}
}
