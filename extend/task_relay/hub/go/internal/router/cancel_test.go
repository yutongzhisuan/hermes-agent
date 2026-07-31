package router_test

import (
	"context"
	"testing"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/store"
)

func TestCancelPendingTask(t *testing.T) {
	mem := store.NewMemory()
	r := router.NewRouter(mem, nil, router.DefaultRouterConfig())
	ctx := context.Background()

	_, err := r.DispatchTask(ctx, router.TaskSpec{
		TaskID: "cancel-1",
		Goal:   "cancel me",
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := r.Cancel(ctx, "cancel-1", "test cancel")
	if err != nil || resp.IdempotentHit || resp.Status != router.StatusCancelled {
		t.Fatalf("cancel: %+v err=%v", resp, err)
	}

	stored, _ := mem.GetTask(ctx, "cancel-1")
	if stored.Status != router.StatusCancelled || stored.Summary != "test cancel" {
		t.Fatalf("stored: %+v", stored)
	}
}

func TestCancelRunningTaskEntersCancelling(t *testing.T) {
	mem := store.NewMemory()
	ctx := context.Background()
	task := &router.Task{
		TaskID: "cancel-2", Goal: "run", CallbackTopic: "topic",
		Status: router.StatusRunning, Attempt: 1, CreatedAt: time.Unix(1_700_000_000, 0),
	}
	if err := mem.InsertTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	resp, err := router.NewRouter(mem, nil, router.DefaultRouterConfig()).Cancel(ctx, "cancel-2", "stop")
	if err != nil || resp.Status != router.StatusCancelling {
		t.Fatalf("cancel running: %+v err=%v", resp, err)
	}
}
