package eventbus

import (
	"context"
	"fmt"
	"sync"

	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/router"
)

const defaultBufferSize = 1024
const replayPage = 256

// DefaultBufferSize returns the design-spec watch stream buffer size.
func DefaultBufferSize() int {
	return defaultBufferSize
}

// Filter mirrors WatchTask oneof filter fields.
type Filter struct {
	Topic   string
	BatchID string
	TaskID  string
}

// CursorOutOfRangeError is returned when since_event_id predates retained events.
type CursorOutOfRangeError struct {
	Requested int64
	Oldest    int64
	Newest    int64
}

func (e *CursorOutOfRangeError) Error() string {
	return fmt.Sprintf(
		"since_event_id %d is older than the oldest retained event %d (newest: %d); "+
			"reconcile via GetTaskResult / ListTasks, then resubscribe with since_event_id=%d",
		e.Requested, e.Oldest, e.Newest, e.Newest,
	)
}

type subscription struct {
	bus          *Bus
	filter       Filter
	sinceEventID int64
	queue        chan *router.TaskEvent
	overflowCh   chan struct{}
	overflow     int64
	delivered    int64
	queuedCount  int
	replaying    bool
	closed       bool
	mu           sync.Mutex
}

// Bus is a persist-first pub/sub backbone over the global event log.
type Bus struct {
	mu         sync.Mutex
	store      router.Store
	bufferSize int
	subs       map[*subscription]struct{}
	newestID   int64
}

// New returns a DB-backed event bus.
func New(store router.Store, bufferSize int) *Bus {
	if bufferSize <= 0 {
		bufferSize = defaultBufferSize
	}
	return &Bus{
		store:      store,
		bufferSize: bufferSize,
		subs:       make(map[*subscription]struct{}),
	}
}

// Publish persists the event first, then fans out to live subscribers.
func (b *Bus) Publish(ctx context.Context, event *router.TaskEvent) (*router.TaskEvent, error) {
	stored, err := b.store.AppendEvent(ctx, event)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	if stored.EventID > b.newestID {
		b.newestID = stored.EventID
	}
	subs := make([]*subscription, 0, len(b.subs))
	for sub := range b.subs {
		subs = append(subs, sub)
	}
	b.mu.Unlock()
	for _, sub := range subs {
		sub.offer(stored)
	}
	return stored, nil
}

// Subscribe replays events with event_id > sinceEventID, then streams live updates.
func (b *Bus) Subscribe(
	ctx context.Context,
	filter Filter,
	sinceEventID int64,
) (<-chan *router.TaskEvent, <-chan error, func(), error) {
	if filter.Topic == "" && filter.BatchID == "" && filter.TaskID == "" {
		return nil, nil, nil, fmt.Errorf("filter requires topic, batch_id, or task_id")
	}
	if err := b.validateCursor(ctx, filter, sinceEventID); err != nil {
		return nil, nil, nil, err
	}

	sub := &subscription{
		bus:          b,
		filter:       filter,
		sinceEventID: sinceEventID,
		queue:        make(chan *router.TaskEvent, b.bufferSize),
		overflowCh:   make(chan struct{}, 1),
		delivered:    sinceEventID,
		replaying:    true,
	}
	b.mu.Lock()
	b.subs[sub] = struct{}{}
	b.mu.Unlock()

	// Unbuffered: the internal queue is the only bounded buffer (Python parity).
	out := make(chan *router.TaskEvent)
	errCh := make(chan error, 1)
	go sub.run(ctx, out, errCh)

	cancel := func() { sub.close() }
	return out, errCh, cancel, nil
}

func (b *Bus) validateCursor(ctx context.Context, filter Filter, sinceEventID int64) error {
	if sinceEventID <= 0 {
		return nil
	}
	oldest, err := b.store.OldestEventIDForFilter(ctx, filter.Topic, filter.BatchID, filter.TaskID)
	if err != nil {
		return err
	}
	floor := oldest
	if floor == nil {
		floor, err = b.store.OldestEventID(ctx)
		if err != nil {
			return err
		}
	}
	if floor == nil || sinceEventID >= *floor {
		return nil
	}
	storedNewest, err := b.store.NewestEventID(ctx)
	if err != nil {
		return err
	}
	newest := *floor
	if storedNewest != nil && *storedNewest > newest {
		newest = *storedNewest
	}
	b.mu.Lock()
	if b.newestID > newest {
		newest = b.newestID
	}
	b.mu.Unlock()
	return &CursorOutOfRangeError{Requested: sinceEventID, Oldest: *floor, Newest: newest}
}

func (b *Bus) drop(sub *subscription) {
	b.mu.Lock()
	delete(b.subs, sub)
	b.mu.Unlock()
}

func (s *subscription) offer(event *router.TaskEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if s.overflow > 0 {
		if event.EventID > s.overflow {
			s.overflow = event.EventID
		}
		return
	}
	if !matches(s.filter, event) {
		return
	}
	if s.queuedCount >= s.bus.bufferSize {
		s.markOverflow(event.EventID)
		return
	}
	select {
	case s.queue <- event:
		s.queuedCount++
	default:
		s.markOverflow(event.EventID)
	}
}

func (s *subscription) markOverflow(eventID int64) {
	s.overflow = eventID
	select {
	case s.overflowCh <- struct{}{}:
	default:
	}
}

func (s *subscription) run(ctx context.Context, out chan<- *router.TaskEvent, errCh chan<- error) {
	defer close(out)
	defer close(errCh)
	defer s.bus.drop(s)

	for {
		if err := s.raiseIfOverflow(errCh); err != nil {
			return
		}
		if s.replaying {
			if s.isClosed() {
				return
			}
			delivered := s.deliveredCursor()
			events, err := s.bus.store.ListEventsForFilter(ctx, router.EventFilter{
				Topic:        s.filter.Topic,
				BatchID:      s.filter.BatchID,
				TaskID:       s.filter.TaskID,
				AfterEventID: delivered,
				Limit:        replayPage,
			})
			if err != nil {
				errCh <- err
				return
			}
			if len(events) == 0 {
				s.setReplaying(false)
				continue
			}
			for _, event := range events {
				if err := s.raiseIfOverflow(errCh); err != nil {
					return
				}
				if s.isClosed() {
					return
				}
				if !s.sendEvent(ctx, out, errCh, event, false) {
					return
				}
			}
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-s.overflowCh:
			if err := s.raiseIfOverflow(errCh); err != nil {
				return
			}
		case event, ok := <-s.queue:
			if !ok {
				return
			}
			if event.EventID <= s.deliveredCursor() {
				continue
			}
			if err := s.raiseIfOverflow(errCh); err != nil {
				return
			}
			if !s.sendEvent(ctx, out, errCh, event, true) {
				return
			}
		}
	}
}

func (s *subscription) sendEvent(
	ctx context.Context,
	out chan<- *router.TaskEvent,
	errCh chan<- error,
	event *router.TaskEvent,
	fromQueue bool,
) bool {
	for {
		if err := s.raiseIfOverflow(errCh); err != nil {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case out <- event:
			s.setDelivered(event.EventID)
			if fromQueue {
				s.mu.Lock()
				if s.queuedCount > 0 {
					s.queuedCount--
				}
				s.mu.Unlock()
			}
			return true
		case <-s.overflowCh:
		}
	}
}

func (s *subscription) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	s.bus.drop(s)
	close(s.queue)
}

func (s *subscription) raiseIfOverflow(errCh chan<- error) error {
	s.mu.Lock()
	overflow := s.overflow
	delivered := s.delivered
	if overflow > 0 {
		s.overflow = 0
		s.closed = true
	}
	s.mu.Unlock()
	if overflow == 0 {
		return nil
	}
	newest := overflow
	s.bus.mu.Lock()
	if s.bus.newestID > newest {
		newest = s.bus.newestID
	}
	s.bus.mu.Unlock()
	s.bus.drop(s)
	errCh <- &SlowConsumerError{Delivered: delivered, Newest: newest}
	return fmt.Errorf("slow consumer")
}

func (s *subscription) deliveredCursor() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delivered
}

func (s *subscription) setDelivered(id int64) {
	s.mu.Lock()
	s.delivered = id
	s.mu.Unlock()
}

func (s *subscription) setReplaying(replaying bool) {
	s.mu.Lock()
	s.replaying = replaying
	s.mu.Unlock()
}

func (s *subscription) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func matches(filter Filter, event *router.TaskEvent) bool {
	if filter.Topic != "" && event.CallbackTopic != filter.Topic {
		return false
	}
	if filter.BatchID != "" && event.BatchID != filter.BatchID {
		return false
	}
	if filter.TaskID != "" && event.TaskID != filter.TaskID {
		return false
	}
	return true
}
