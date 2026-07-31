package eventbus

import "fmt"

// SlowConsumerError is returned when a subscriber's bounded buffer overflows.
type SlowConsumerError struct {
	Delivered int64
	Newest    int64
}

func (e *SlowConsumerError) Error() string {
	return fmt.Sprintf(
		"watch buffer overflowed after delivering event %d (newest: %d); "+
			"reconcile, then resubscribe with since_event_id=%d",
		e.Delivered, e.Newest, e.Delivered,
	)
}
