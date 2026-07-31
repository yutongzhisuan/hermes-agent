package router_test

import (
	"context"
	"testing"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/store"
)

func TestDispatchTaskIdempotent(t *testing.T) {
	mem := store.NewMemory()
	r := router.NewRouter(mem, nil, router.DefaultRouterConfig())
	ctx := context.Background()
	spec := router.TaskSpec{TaskID: "t1", Goal: "hello", CallbackTopic: "topic-1"}

	first, err := r.DispatchTask(ctx, spec, "test-session")
	if err != nil || first.IdempotentHit {
		t.Fatalf("first dispatch: %+v err=%v", first, err)
	}
	second, err := r.DispatchTask(ctx, spec, "test-session")
	if err != nil || !second.IdempotentHit || second.Status != router.StatusPending {
		t.Fatalf("second dispatch: %+v err=%v", second, err)
	}
}

func TestCompleteTerminalTransition(t *testing.T) {
	mem := store.NewMemory()
	ctx := context.Background()

	task := &router.Task{
		TaskID: "t2", Goal: "run", CallbackTopic: "topic-1",
		Status: router.StatusRunning, Attempt: 1, CreatedAt: time.Unix(1_700_000_000, 0),
	}
	if err := mem.InsertTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	resp, err := router.NewRouter(mem, nil, router.DefaultRouterConfig()).Complete(ctx, "t2", router.StatusCompleted, "done")
	if err != nil || resp.IdempotentHit || resp.Status != router.StatusCompleted {
		t.Fatalf("complete: %+v err=%v", resp, err)
	}
	stored, _ := mem.GetTask(ctx, "t2")
	if stored.Summary != "done" || stored.Status != router.StatusCompleted {
		t.Fatalf("stored task: %+v", stored)
	}
}

func TestValidateTransitionRejectsIllegal(t *testing.T) {
	if err := router.ValidateTransition(router.StatusPending, router.StatusCompleted); err == nil {
		t.Fatal("expected illegal pending->completed transition")
	}
}
