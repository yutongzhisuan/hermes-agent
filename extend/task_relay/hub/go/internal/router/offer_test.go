package router_test

import (
	"context"
	"testing"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/registry"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/store"
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
	if _, err := rt.DispatchTask(ctx, router.TaskSpec{
		TaskID: "ts1", Goal: "work", Toolsets: []string{"terminal"},
	}, "m1", false); err != nil {
		t.Fatal(err)
	}

	offered, err := rt.OfferTasksForPoll(ctx, "w1", 1, nil)
	if err != nil || len(offered) != 1 {
		t.Fatalf("offer: %+v err=%v", offered, err)
	}
	task, _ := mem.GetTask(ctx, "ts1")
	if task.Status != router.StatusPending || task.ClaimToken == "" {
		t.Fatalf("expected pending offer token: %+v", task)
	}

	claimed, err := rt.ClaimOfferedTask(ctx, "ts1", "w1", offered[0].ClaimToken, nil)
	if err != nil || claimed == nil {
		t.Fatalf("claim offered: %+v err=%v", claimed, err)
	}
	task, _ = mem.GetTask(ctx, "ts1")
	if task.Status != router.StatusRunning {
		t.Fatalf("expected running after claim: %+v", task)
	}

	if ok, err := rt.ReleaseOffer(ctx, "ts2", "nope"); err != nil || ok {
		t.Fatalf("release unknown should be false: ok=%v err=%v", ok, err)
	}
}

func TestActiveOfferBlocksAtomicClaim(t *testing.T) {
	mem := store.NewMemory()
	reg := registry.New(nil)
	rt := router.NewRouter(mem, registry.NewRouterAdapter(reg), router.DefaultRouterConfig())
	ctx := context.Background()
	reg.Announce(ctx, registry.AnnounceInput{WorkerID: "w1", SessionModes: []string{"A"}, MaxConcurrent: 2})
	if _, err := rt.DispatchTask(ctx, router.TaskSpec{TaskID: "ts2", Goal: "work"}, "m1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.OfferTasksForPoll(ctx, "w1", 1, nil); err != nil {
		t.Fatal(err)
	}
	claimed, err := rt.ClaimForPoll(ctx, "w1", 1, nil)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("expected atomic claim blocked: %+v err=%v", claimed, err)
	}
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
	if _, err := rt.DispatchTask(ctx, router.TaskSpec{TaskID: "ts3", Goal: "work"}, "m1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.OfferTasksForPoll(ctx, "w1", 1, nil); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if err := rt.TickTimeouts(ctx); err != nil {
		t.Fatal(err)
	}
	task, _ := mem.GetTask(ctx, "ts3")
	if task.ClaimToken != "" {
		t.Fatalf("expected expired offer cleared: %+v", task)
	}
	claimed, err := rt.ClaimForPoll(ctx, "w1", 1, nil)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("expected reclaim after expiry: %+v err=%v", claimed, err)
	}
}
