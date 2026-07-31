package eventbus

import (
	"fmt"
	"sync"
	"time"
)

const defaultBufferSize = 64

// Kind identifies a TaskEvent category for WatchTask streams.
type Kind int

const (
	KindStatus Kind = iota + 1
	KindProgress
	KindTerminal
)

// Filter mirrors WatchTask oneof filter fields.
type Filter struct {
	Topic   string
	BatchID string
	TaskID  string
}

// Event is an in-memory watch event (Go Hub scaffold; no SQLite log yet).
type Event struct {
	EventID         int64
	EventAt         time.Time
	TaskID          string
	BatchID         string
	CallbackTopic   string
	Kind            Kind
	ProgressSummary string
	Status          string
	Summary         string
}

// CursorOutOfRangeError is returned when since_event_id predates retained events.
type CursorOutOfRangeError struct {
	Requested int64
	Oldest    int64
	Newest    int64
}

func (e *CursorOutOfRangeError) Error() string {
	return fmt.Sprintf(
		"since_event_id %d is older than oldest retained %d (newest %d)",
		e.Requested, e.Oldest, e.Newest,
	)
}

type subscription struct {
	filter Filter
	ch     chan Event
}

// Bus is a minimal in-process event backbone for WatchTask (Go port scaffold).
type Bus struct {
	mu     sync.RWMutex
	nextID int64
	events []Event
	subs   map[*subscription]struct{}
}

// New returns an empty event bus.
func New() *Bus {
	return &Bus{subs: make(map[*subscription]struct{})}
}

// Publish assigns a monotonic event_id, retains the event, and fans out live.
func (b *Bus) Publish(event Event) Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	event.EventID = b.nextID
	if event.EventAt.IsZero() {
		event.EventAt = time.Now()
	}
	b.events = append(b.events, event)
	for sub := range b.subs {
		if !matches(sub.filter, event) {
			continue
		}
		select {
		case sub.ch <- event:
		default:
		}
	}
	return event
}

// Subscribe returns replayed then live events matching filter after sinceEventID.
func (b *Bus) Subscribe(filter Filter, sinceEventID int64) (<-chan Event, func(), error) {
	if filter.Topic == "" && filter.BatchID == "" && filter.TaskID == "" {
		return nil, nil, fmt.Errorf("filter requires topic, batch_id, or task_id")
	}

	b.mu.Lock()
	oldest, newest := b.cursorBoundsLocked(filter)
	if sinceEventID > 0 && oldest > 0 && sinceEventID < oldest {
		b.mu.Unlock()
		return nil, nil, &CursorOutOfRangeError{
			Requested: sinceEventID,
			Oldest:    oldest,
			Newest:    newest,
		}
	}
	replay := b.replayLocked(filter, sinceEventID)
	sub := &subscription{filter: filter, ch: make(chan Event, defaultBufferSize)}
	b.subs[sub] = struct{}{}
	b.mu.Unlock()

	out := make(chan Event, defaultBufferSize)
	go func() {
		defer close(out)
		for _, event := range replay {
			out <- event
		}
		for event := range sub.ch {
			out <- event
		}
	}()
	cancel := func() {
		b.mu.Lock()
		delete(b.subs, sub)
		close(sub.ch)
		b.mu.Unlock()
	}
	return out, cancel, nil
}

func (b *Bus) cursorBoundsLocked(filter Filter) (oldest, newest int64) {
	for _, event := range b.events {
		if !matches(filter, event) {
			continue
		}
		if oldest == 0 || event.EventID < oldest {
			oldest = event.EventID
		}
		if event.EventID > newest {
			newest = event.EventID
		}
	}
	return oldest, newest
}

func (b *Bus) replayLocked(filter Filter, sinceEventID int64) []Event {
	replay := make([]Event, 0)
	for _, event := range b.events {
		if event.EventID <= sinceEventID || !matches(filter, event) {
			continue
		}
		replay = append(replay, event)
	}
	return replay
}

func matches(filter Filter, event Event) bool {
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
