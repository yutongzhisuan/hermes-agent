package router_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/hub/internal/registry"
	"github.com/infa/task_relay/hub/internal/router"
	"github.com/infa/task_relay/hub/internal/store"
)

func TestTwoStepOfferClaimRelease(t *testing.T) {
	mem := store.NewMemory()
	reg := registry.New(nil)
	cfg := router.DefaultRouterConfig()
	cfg.PollOfferSeconds = 30
	rt := router.NewRouter(mem, registry.NewRouterAdapter(reg), cfg)
	ctx := context.Background()

	reg.Announce(ctx, registry.AnnounceInput{
		WorkerID: "w1", SessionModes: []string{"A"}, MaxConcurrent: 2, Toolsets: []string{"terminal"},
	})
	_, err := rt.DispatchTask(ctx, router.TaskSpec{
		TaskID: "ts1", Goal: "work", Toolsets: []string{"terminal"},
	}, "m1", false)
	require.NoError(t, err)

	offered, err := rt.OfferTasksForPoll(ctx, "w1", 1, nil)
	require.NoError(t, err)
	require.Len(t, offered, 1)

	task, err := mem.GetTask(ctx, "ts1")
	require.NoError(t, err)
	require.Equal(t, router.StatusPending, task.Status)
	require.NotEmpty(t, task.ClaimToken)

	claimed, err := rt.ClaimOfferedTask(ctx, "ts1", "w1", offered[0].ClaimToken, nil)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	task, err = mem.GetTask(ctx, "ts1")
	require.NoError(t, err)
	require.Equal(t, router.StatusRunning, task.Status)

	ok, err := rt.ReleaseOffer(ctx, "ts2", "nope")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestActiveOfferBlocksAtomicClaim(t *testing.T) {
	mem := store.NewMemory()
	reg := registry.New(nil)
	rt := router.NewRouter(mem, registry.NewRouterAdapter(reg), router.DefaultRouterConfig())
	ctx := context.Background()
	reg.Announce(ctx, registry.AnnounceInput{WorkerID: "w1", SessionModes: []string{"A"}, MaxConcurrent: 2})
	_, err := rt.DispatchTask(ctx, router.TaskSpec{TaskID: "ts2", Goal: "work"}, "m1", false)
	require.NoError(t, err)
	_, err = rt.OfferTasksForPoll(ctx, "w1", 1, nil)
	require.NoError(t, err)

	claimed, err := rt.ClaimForPoll(ctx, "w1", 1, nil)
	require.NoError(t, err)
	require.Empty(t, claimed)
}

func TestExpiredOfferClearedByTimeoutTick(t *testing.T) {
	mem := store.NewMemory()
	reg := registry.New(nil)
	cfg := router.DefaultRouterConfig()
	cfg.PollOfferSeconds = 1
	now := time.Unix(1_700_000_000, 0)
	rt := router.NewRouter(mem, registry.NewRouterAdapter(reg), cfg)
	rt.SetNow(func() time.Time { return now })
	ctx := context.Background()
	reg.Announce(ctx, registry.AnnounceInput{WorkerID: "w1", SessionModes: []string{"A"}, MaxConcurrent: 2})
	_, err := rt.DispatchTask(ctx, router.TaskSpec{TaskID: "ts3", Goal: "work"}, "m1", false)
	require.NoError(t, err)
	_, err = rt.OfferTasksForPoll(ctx, "w1", 1, nil)
	require.NoError(t, err)

	now = now.Add(2 * time.Second)
	require.NoError(t, rt.TickTimeouts(ctx))

	task, err := mem.GetTask(ctx, "ts3")
	require.NoError(t, err)
	require.Empty(t, task.ClaimToken)

	claimed, err := rt.ClaimForPoll(ctx, "w1", 1, nil)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
}
