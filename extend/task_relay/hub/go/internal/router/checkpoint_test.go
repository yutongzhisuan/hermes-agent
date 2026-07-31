package router_test

import (
	"context"
	"testing"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/store"
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
	if err := mem.InsertTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	if err := r.OnCheckpoint(ctx, "cp1", "ckpt-1", "half done", "", []byte("blob")); err != nil {
		t.Fatal(err)
	}

	stored, _ := mem.GetTask(ctx, "cp1")
	if !stored.FirstProgressDeadlineAt.IsZero() {
		t.Fatalf("expected first progress deadline cleared: %+v", stored)
	}
	if !stored.ClaimExpiresAt.After(now.Add(30 * time.Second)) {
		t.Fatalf("expected lease extension: %+v", stored.ClaimExpiresAt)
	}

	checkpoint, err := r.GetLatestCheckpoint(ctx, "cp1")
	if err != nil || checkpoint == nil {
		t.Fatalf("checkpoint: %+v err=%v", checkpoint, err)
	}
	if checkpoint.CheckpointID != "ckpt-1" || checkpoint.Summary != "half done" {
		t.Fatalf("unexpected checkpoint: %+v", checkpoint)
	}
}
