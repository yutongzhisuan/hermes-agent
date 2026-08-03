package router_test

import (
	"context"
	"testing"
	"time"

	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/store"
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
	if err := mem.InsertTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	if err := r.TickTimeouts(ctx); err != nil {
		t.Fatal(err)
	}
	stored, _ := mem.GetTask(ctx, "q1")
	if stored.Status != router.StatusLost || stored.Summary != "queue timeout" {
		t.Fatalf("unexpected task after queue timeout: %+v", stored)
	}
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
	if err := mem.InsertTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	if err := r.TickTimeouts(ctx); err != nil {
		t.Fatal(err)
	}
	stored, _ := mem.GetTask(ctx, "fp1")
	if stored.Status != router.StatusLost || stored.Summary != "first progress timeout" {
		t.Fatalf("unexpected task after first progress timeout: %+v", stored)
	}
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
	if err := mem.InsertTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	if err := r.TickTimeouts(ctx); err != nil {
		t.Fatal(err)
	}
	stored, _ := mem.GetTask(ctx, "lease1")
	if stored.Status != router.StatusCancelling || stored.CancelReason != "execution timeout" {
		t.Fatalf("unexpected task after lease timeout: %+v", stored)
	}
}
