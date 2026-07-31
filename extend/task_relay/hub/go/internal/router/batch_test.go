package router_test

import (
	"context"
	"testing"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/store"
)

func TestDispatchTaskBatchIdempotent(t *testing.T) {
	mem := store.NewMemory()
	r := router.NewRouter(mem, nil, router.DefaultRouterConfig())
	ctx := context.Background()
	specs := []router.TaskSpec{
		{TaskID: "b1-t1", Goal: "one", CallbackTopic: "batch-topic"},
		{TaskID: "b1-t2", Goal: "two", CallbackTopic: "batch-topic"},
	}

	first, err := r.DispatchTaskBatch(ctx, "batch-1", "batch-topic", "", "sess", specs)
	if err != nil || first.IdempotentHit || len(first.Tasks) != 2 {
		t.Fatalf("first batch: %+v err=%v", first, err)
	}

	second, err := r.DispatchTaskBatch(ctx, "batch-1", "batch-topic", "", "sess", specs)
	if err != nil || !second.IdempotentHit || len(second.Tasks) != 2 {
		t.Fatalf("second batch: %+v err=%v", second, err)
	}

	conflict, err := r.DispatchTaskBatch(ctx, "batch-1", "batch-topic", "", "sess", []router.TaskSpec{
		{TaskID: "b1-t1", Goal: "changed", CallbackTopic: "batch-topic"},
	})
	if err == nil {
		t.Fatalf("expected spec hash conflict, got %+v", conflict)
	}
}

func TestDispatchTaskBatchSetsBatchIDOnTasks(t *testing.T) {
	mem := store.NewMemory()
	r := router.NewRouter(mem, nil, router.DefaultRouterConfig())
	ctx := context.Background()
	_, err := r.DispatchTaskBatch(ctx, "batch-2", "topic", "", "sess", []router.TaskSpec{
		{TaskID: "b2-t1", Goal: "goal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := r.GetTask(ctx, "b2-t1")
	if err != nil || task.BatchID != "batch-2" {
		t.Fatalf("task batch_id: %+v err=%v", task, err)
	}
}
