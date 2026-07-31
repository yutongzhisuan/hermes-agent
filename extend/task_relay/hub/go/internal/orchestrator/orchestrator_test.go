package orchestrator_test

import (
	"context"
	"testing"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/eventbus"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/orchestrator"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/registry"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/store"
)

type stubPublisher struct {
	terminals []string
}

func (s *stubPublisher) PublishTerminal(task *router.Task) {
	s.terminals = append(s.terminals, task.TaskID)
}

func (s *stubPublisher) PublishAggregate(event eventbus.Event) {}

func TestDAGBlocksClaimUntilDependencyCompletes(t *testing.T) {
	mem := store.NewMemory()
	reg := registry.New()
	rt := router.NewRouter(mem, registry.NewRouterAdapter(reg), router.DefaultRouterConfig())
	pub := &stubPublisher{}
	orch := orchestrator.New(mem, pub)
	rt.SetOrchestrator(orch)

	ctx := context.Background()
	_, err := rt.DispatchTaskBatch(ctx, "dag-1", "topic", "", []router.TaskSpec{
		{TaskID: "a1", Goal: "first"},
		{TaskID: "a2", Goal: "second", DependsOn: []string{"a1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reg.Announce(ctx, registry.AnnounceInput{WorkerID: "w1", SessionModes: []string{"A"}, MaxConcurrent: 2})

	claimed, err := rt.ClaimForPoll(ctx, "w1", 2, nil)
	if err != nil || len(claimed) != 1 || claimed[0].TaskID != "a1" {
		t.Fatalf("expected only a1 claimed: %+v err=%v", claimed, err)
	}
	if _, err := rt.Complete(ctx, "a1", router.StatusCompleted, "done"); err != nil {
		t.Fatal(err)
	}
	claimed2, err := rt.ClaimForPoll(ctx, "w1", 1, nil)
	if err != nil || len(claimed2) != 1 || claimed2[0].TaskID != "a2" {
		t.Fatalf("expected a2 claimed: %+v err=%v", claimed2, err)
	}
}

func TestFailFastCancelsBatchSibling(t *testing.T) {
	mem := store.NewMemory()
	reg := registry.New()
	rt := router.NewRouter(mem, registry.NewRouterAdapter(reg), router.DefaultRouterConfig())
	orch := orchestrator.New(mem, &stubPublisher{})
	rt.SetOrchestrator(orch)
	ctx := context.Background()

	policy := `{"fail_fast": true}`
	_, err := rt.DispatchTaskBatch(ctx, "ff-1", "topic", policy, []router.TaskSpec{
		{TaskID: "f1", Goal: "one"},
		{TaskID: "f2", Goal: "two"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reg.Announce(ctx, registry.AnnounceInput{WorkerID: "w1", SessionModes: []string{"A"}, MaxConcurrent: 1})
	if _, err := rt.ClaimForPoll(ctx, "w1", 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Complete(ctx, "f1", router.StatusFailed, "fail"); err != nil {
		t.Fatal(err)
	}
	sibling, _ := mem.GetTask(ctx, "f2")
	if sibling.Status != router.StatusCancelled {
		t.Fatalf("expected cancelled sibling: %+v", sibling)
	}
}
