package eventbus

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/store"
)

func TestSlowConsumerOverflow(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	bus := New(st, 2)

	_, errCh, cancel, err := bus.Subscribe(ctx, Filter{Topic: "a"}, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()

	publish := func(n int) *router.TaskEvent {
		ev, err := bus.Publish(ctx, &router.TaskEvent{
			CallbackTopic: "a",
			TaskID:        "t1",
			Kind:          router.EventKindStatus,
			PayloadJSON:   fmt.Sprintf(`{"n":%d}`, n),
		})
		if err != nil {
			t.Fatalf("publish %d: %v", n, err)
		}
		return ev
	}

	publish(1)
	publish(2)
	third := publish(3)

	select {
	case err := <-errCh:
		slow, ok := err.(*SlowConsumerError)
		if !ok {
			t.Fatalf("expected SlowConsumerError, got %T: %v", err, err)
		}
		if slow.Delivered != 0 {
			t.Fatalf("delivered = %d, want 0", slow.Delivered)
		}
		if slow.Newest != third.EventID {
			t.Fatalf("newest = %d, want %d", slow.Newest, third.EventID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for slow consumer error")
	}
}
