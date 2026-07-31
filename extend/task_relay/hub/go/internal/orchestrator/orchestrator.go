package orchestrator

import (
	"context"
	"encoding/json"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/eventbus"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

var (
	terminalStatuses         = map[string]struct{}{router.StatusCompleted: {}, router.StatusFailed: {}, router.StatusLost: {}, router.StatusCancelled: {}}
	failedDependencyStatuses = map[string]struct{}{router.StatusFailed: {}, router.StatusLost: {}, router.StatusCancelled: {}}
)

// TerminalPublisher emits watch events for orchestrator-driven transitions.
type TerminalPublisher interface {
	PublishTerminal(task *router.Task)
	PublishAggregate(event eventbus.Event)
}

// Orchestrator implements M3 DAG, BatchPolicy, and AGGREGATE behavior.
type Orchestrator struct {
	store      router.Store
	publisher  TerminalPublisher
	now        func() time.Time
	aggregates map[string]struct{}
}

// New constructs a batch orchestrator wired to store and event publisher.
func New(store router.Store, publisher TerminalPublisher) *Orchestrator {
	return &Orchestrator{
		store: store, publisher: publisher,
		now: time.Now, aggregates: make(map[string]struct{}),
	}
}

// IsTaskReady reports whether all depends_on tasks are completed.
func (o *Orchestrator) IsTaskReady(ctx context.Context, task *router.Task) (bool, error) {
	deps, err := decodeStringList(task.DependsOnJSON)
	if err != nil || len(deps) == 0 {
		return true, err
	}
	for _, depID := range deps {
		dep, err := o.store.GetTask(ctx, depID)
		if err != nil {
			return false, err
		}
		if dep == nil || dep.Status != router.StatusCompleted {
			return false, nil
		}
	}
	return true, nil
}

// OnTaskTerminal propagates dependency failures, applies batch policy, emits AGGREGATE.
func (o *Orchestrator) OnTaskTerminal(ctx context.Context, task *router.Task, status string) ([]string, error) {
	ready := make([]string, 0)
	if status != router.StatusCompleted {
		if err := o.cancelDependents(ctx, task.TaskID, status); err != nil {
			return nil, err
		}
	} else {
		items, err := o.collectNewlyReady(ctx, task.TaskID)
		if err != nil {
			return nil, err
		}
		ready = append(ready, items...)
	}
	if task.BatchID != "" {
		if err := o.applyBatchPolicy(ctx, task, status); err != nil {
			return ready, err
		}
		if task.AggregateKey != "" {
			if err := o.maybeEmitAggregate(ctx, task); err != nil {
				return ready, err
			}
		}
	}
	return ready, nil
}

// EnforceBatchDeadlines cancels non-terminal tasks when batch deadlines expire.
func (o *Orchestrator) EnforceBatchDeadlines(ctx context.Context) error {
	now := o.now()
	batches, err := o.store.ListExpiredBatches(ctx, now)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		members, err := o.store.ListTasks(ctx, router.ListTasksQuery{BatchID: batch.BatchID, Limit: 1000})
		if err != nil {
			return err
		}
		for _, member := range members {
			if _, terminal := terminalStatuses[member.Status]; terminal {
				continue
			}
			if err := o.onCancel(ctx, member.TaskID, "batch timeout"); err != nil {
				return err
			}
		}
		batch.BatchDeadlineAt = time.Time{}
		if err := o.store.UpdateBatch(ctx, batch); err != nil {
			return err
		}
	}
	return nil
}

func decodeStringList(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, err
	}
	return items, nil
}
