package delivery_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/hub/internal/delivery"
	"github.com/infa/task_relay/hub/internal/registry"
	"github.com/infa/task_relay/hub/internal/router"
	"github.com/infa/task_relay/hub/internal/store"
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

	_, err := rt.DispatchTask(ctx, router.TaskSpec{
		TaskID: "mode-c-1", Goal: "push me", CallbackTopic: "topic-1",
	}, "sess", false)
	require.NoError(t, err)

	coord.OnTaskPending(ctx, "mode-c-1")
	require.Len(t, pusher.payloads, 1)
	run, ok := pusher.payloads[0]["run"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "mode-c-1", run["task_id"])

	task, err := mem.GetTask(ctx, "mode-c-1")
	require.NoError(t, err)
	require.Equal(t, router.StatusRunning, task.Status)
	require.Equal(t, "wc1", task.WorkerID)
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

	_, err := rt.DispatchTask(ctx, router.TaskSpec{
		TaskID: "mode-c-2", Goal: "skip me", CallbackTopic: "topic-1",
	}, "sess", false)
	require.NoError(t, err)

	coord.OnTaskPending(ctx, "mode-c-2")
	require.Empty(t, pusher.payloads)

	task, err := mem.GetTask(ctx, "mode-c-2")
	require.NoError(t, err)
	require.Equal(t, router.StatusPending, task.Status)
}
