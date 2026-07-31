package delivery_test

import (
	"context"
	"testing"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/delivery"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/registry"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/store"
)

type mockPusher struct {
	payloads []map[string]any
}

func (m *mockPusher) PushTaskRun(payload map[string]any) bool {
	m.payloads = append(m.payloads, payload)
	return true
}

func testRunBuilder(ctx context.Context, claimed router.ClaimedTask) (map[string]any, error) {
	return map[string]any{
		"run": map[string]any{"task_id": claimed.TaskID, "goal": claimed.Goal},
	}, nil
}

func TestCoordinatorPushesPendingTaskOnCredit(t *testing.T) {
	mem := store.NewMemory()
	reg := registry.New(nil)
	rt := router.NewRouter(mem, registry.NewRouterAdapter(reg), router.DefaultRouterConfig())
	coord := delivery.New(rt, reg, nil, testRunBuilder)
	ctx := context.Background()

	credit := 1
	pusher := &mockPusher{}
	reg.Announce(ctx, registry.AnnounceInput{
		WorkerID: "wc1", SessionModes: []string{"A", "C"}, MaxConcurrent: 1,
		InitialCredit: &credit, OnlineSessionID: "sess-1", Pusher: pusher,
	})

	if _, err := rt.DispatchTask(ctx, router.TaskSpec{
		TaskID: "mode-c-1", Goal: "push me", CallbackTopic: "topic-1",
	}, "sess", false); err != nil {
		t.Fatal(err)
	}

	coord.OnTaskPending(ctx, "mode-c-1")
	if len(pusher.payloads) != 1 {
		t.Fatalf("expected one push, got %d", len(pusher.payloads))
	}
	run, _ := pusher.payloads[0]["run"].(map[string]any)
	if run["task_id"] != "mode-c-1" {
		t.Fatalf("unexpected payload: %+v", pusher.payloads[0])
	}

	task, _ := mem.GetTask(ctx, "mode-c-1")
	if task.Status != router.StatusRunning || task.WorkerID != "wc1" {
		t.Fatalf("expected claimed running task: %+v", task)
	}
}

func TestCoordinatorSkipsDrainingWorker(t *testing.T) {
	mem := store.NewMemory()
	reg := registry.New(nil)
	rt := router.NewRouter(mem, registry.NewRouterAdapter(reg), router.DefaultRouterConfig())
	coord := delivery.New(rt, reg, nil, testRunBuilder)
	ctx := context.Background()

	credit := 1
	pusher := &mockPusher{}
	reg.Announce(ctx, registry.AnnounceInput{
		WorkerID: "wc2", SessionModes: []string{"A", "C"}, MaxConcurrent: 1,
		InitialCredit: &credit, OnlineSessionID: "sess-2", Pusher: pusher,
	})
	reg.Drain(ctx, "wc2")

	if _, err := rt.DispatchTask(ctx, router.TaskSpec{
		TaskID: "mode-c-2", Goal: "skip me", CallbackTopic: "topic-1",
	}, "sess", false); err != nil {
		t.Fatal(err)
	}

	coord.OnTaskPending(ctx, "mode-c-2")
	if len(pusher.payloads) != 0 {
		t.Fatalf("expected no push to draining worker, got %+v", pusher.payloads)
	}
	task, _ := mem.GetTask(ctx, "mode-c-2")
	if task.Status != router.StatusPending {
		t.Fatalf("expected pending task: %+v", task)
	}
}
