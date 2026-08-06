package orchestrator_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/orchestrator"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/registry"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/store"
)

type stubPublisher struct {
	terminals []string
}

func (s *stubPublisher) PublishTerminal(task *router.Task) {
	s.terminals = append(s.terminals, task.TaskID)
}

func (s *stubPublisher) PublishAggregate(task *router.Task, payload map[string]any) {}

func TestDAGBlocksClaimUntilDependencyCompletes(t *testing.T) {
	mem := store.NewMemory()
	reg := registry.New(nil)
	rt := router.NewRouter(mem, registry.NewRouterAdapter(reg), router.DefaultRouterConfig())
	pub := &stubPublisher{}
	orch := orchestrator.New(mem, pub)
	rt.SetOrchestrator(orch)

	ctx := context.Background()
	_, err := rt.DispatchTaskBatch(ctx, "dag-1", "topic", "", "sess", []router.TaskSpec{
		{TaskID: "a1", Goal: "first"},
		{TaskID: "a2", Goal: "second", DependsOn: []string{"a1"}},
	}, false)
	require.NoError(t, err)
	reg.Announce(ctx, registry.AnnounceInput{WorkerID: "w1", SessionModes: []string{"A"}, MaxConcurrent: 2})

	claimed, err := rt.ClaimForPoll(ctx, "w1", 2, nil)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, "a1", claimed[0].TaskID)

	_, err = rt.Complete(ctx, "a1", router.StatusCompleted, "done", router.CompleteInput{})
	require.NoError(t, err)

	claimed2, err := rt.ClaimForPoll(ctx, "w1", 1, nil)
	require.NoError(t, err)
	require.Len(t, claimed2, 1)
	require.Equal(t, "a2", claimed2[0].TaskID)
}

func TestFailFastCancelsBatchSibling(t *testing.T) {
	mem := store.NewMemory()
	reg := registry.New(nil)
	rt := router.NewRouter(mem, registry.NewRouterAdapter(reg), router.DefaultRouterConfig())
	orch := orchestrator.New(mem, &stubPublisher{})
	rt.SetOrchestrator(orch)
	ctx := context.Background()

	policy := `{"fail_fast": true}`
	_, err := rt.DispatchTaskBatch(ctx, "ff-1", "topic", policy, "sess", []router.TaskSpec{
		{TaskID: "f1", Goal: "one"},
		{TaskID: "f2", Goal: "two"},
	}, false)
	require.NoError(t, err)
	reg.Announce(ctx, registry.AnnounceInput{WorkerID: "w1", SessionModes: []string{"A"}, MaxConcurrent: 1})

	_, err = rt.ClaimForPoll(ctx, "w1", 1, nil)
	require.NoError(t, err)
	_, err = rt.Complete(ctx, "f1", router.StatusFailed, "fail", router.CompleteInput{})
	require.NoError(t, err)

	sibling, err := mem.GetTask(ctx, "f2")
	require.NoError(t, err)
	require.Equal(t, router.StatusCancelled, sibling.Status)
}
