package eventbus

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/hub/internal/router"
	"github.com/infa/task_relay/hub/internal/store"
)

func TestSlowConsumerOverflow(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	bus := New(st, 2)

	_, errCh, cancel, err := bus.Subscribe(ctx, Filter{Topic: "a"}, 0)
	require.NoError(t, err)
	defer cancel()

	publish := func(n int) *router.TaskEvent {
		ev, err := bus.Publish(ctx, &router.TaskEvent{
			CallbackTopic: "a",
			TaskID:        "t1",
			Kind:          router.EventKindStatus,
			PayloadJSON:   fmt.Sprintf(`{"n":%d}`, n),
		})
		require.NoError(t, err)
		return ev
	}

	publish(1)
	publish(2)
	third := publish(3)

	select {
	case err := <-errCh:
		slow, ok := err.(*SlowConsumerError)
		require.True(t, ok, "expected SlowConsumerError, got %T: %v", err, err)
		require.Zero(t, slow.Delivered)
		require.Equal(t, third.EventID, slow.Newest)
	case <-time.After(5 * time.Second):
		require.Fail(t, "timeout waiting for slow consumer error")
	}
}
