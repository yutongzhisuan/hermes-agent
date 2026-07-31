package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/batchpolicy"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/eventbus"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

func (o *Orchestrator) collectNewlyReady(ctx context.Context, completedTaskID string) ([]string, error) {
	pending, err := o.store.ListTasks(ctx, router.ListTasksQuery{Statuses: []string{router.StatusPending}, Limit: 1000})
	if err != nil {
		return nil, err
	}
	ready := make([]string, 0)
	for _, task := range pending {
		deps, err := decodeStringList(task.DependsOnJSON)
		if err != nil {
			return nil, err
		}
		if !contains(deps, completedTaskID) {
			continue
		}
		ok, err := o.IsTaskReady(ctx, task)
		if err != nil {
			return nil, err
		}
		if ok {
			ready = append(ready, task.TaskID)
		}
	}
	return ready, nil
}

func (o *Orchestrator) cancelDependents(ctx context.Context, failedTaskID, failedStatus string) error {
	queue := []string{failedTaskID}
	seen := map[string]struct{}{failedTaskID: {}}
	for len(queue) > 0 {
		depID := queue[0]
		queue = queue[1:]
		pending, err := o.store.ListTasks(ctx, router.ListTasksQuery{Statuses: []string{router.StatusPending}, Limit: 1000})
		if err != nil {
			return err
		}
		for _, task := range pending {
			if _, ok := seen[task.TaskID]; ok {
				continue
			}
			deps, err := decodeStringList(task.DependsOnJSON)
			if err != nil {
				return err
			}
			if !contains(deps, depID) {
				continue
			}
			if !o.dependsOnFailed(ctx, task) {
				continue
			}
			if err := o.cancelAsDependency(ctx, task.TaskID, depID, failedStatus); err != nil {
				return err
			}
			seen[task.TaskID] = struct{}{}
			queue = append(queue, task.TaskID)
		}
	}
	return nil
}

func (o *Orchestrator) dependsOnFailed(ctx context.Context, task *router.Task) bool {
	deps, err := decodeStringList(task.DependsOnJSON)
	if err != nil {
		return false
	}
	for _, depID := range deps {
		dep, err := o.store.GetTask(ctx, depID)
		if err != nil || dep == nil {
			continue
		}
		if _, failed := failedDependencyStatuses[dep.Status]; failed {
			return true
		}
		if dep.Status != router.StatusCompleted {
			return false
		}
	}
	return false
}

func (o *Orchestrator) cancelAsDependency(ctx context.Context, taskID, depID, depStatus string) error {
	task, err := o.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil || task.Status != router.StatusPending {
		return nil
	}
	msg := fmt.Sprintf("dependency %s ended %s", depID, depStatus)
	task.Status = router.StatusCancelled
	task.Summary = msg
	task.Error = msg
	task.CompletedAt = o.now()
	if err := o.store.UpdateTask(ctx, task); err != nil {
		return err
	}
	if o.publisher != nil {
		o.publisher.PublishTerminal(task)
	}
	return nil
}

func (o *Orchestrator) applyBatchPolicy(ctx context.Context, task *router.Task, status string) error {
	batch, err := o.store.GetBatch(ctx, task.BatchID)
	if err != nil || batch == nil || batch.PolicyJSON == "" {
		return err
	}
	var policy map[string]any
	if err := json.Unmarshal([]byte(batch.PolicyJSON), &policy); err != nil {
		return nil
	}
	if failFast, _ := policy["fail_fast"].(bool); failFast {
		if _, failed := failedDependencyStatuses[status]; failed {
			if err := o.cancelBatchSiblings(ctx, task.BatchID, task.TaskID, "fail_fast"); err != nil {
				return err
			}
		}
	}
	if status == router.StatusCompleted {
		members, err := o.store.ListTasks(ctx, router.ListTasksQuery{BatchID: task.BatchID, Limit: 1000})
		if err != nil {
			return err
		}
		if batchpolicy.CompletionThresholdMet(countCompleted(members), len(members), policy) {
			mode := batchpolicy.NormalizeCompletionMode(policy)
			if err := o.cancelBatchSiblings(ctx, task.BatchID, task.TaskID, "batch "+mode+" threshold met"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (o *Orchestrator) cancelBatchSiblings(ctx context.Context, batchID, exclude, reason string) error {
	members, err := o.store.ListTasks(ctx, router.ListTasksQuery{BatchID: batchID, Limit: 1000})
	if err != nil {
		return err
	}
	for _, member := range members {
		if member.TaskID == exclude {
			continue
		}
		if _, terminal := terminalStatuses[member.Status]; terminal {
			continue
		}
		if err := o.onCancel(ctx, member.TaskID, reason); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) onCancel(ctx context.Context, taskID, reason string) error {
	task, err := o.store.GetTask(ctx, taskID)
	if err != nil || task == nil {
		return err
	}
	if _, terminal := terminalStatuses[task.Status]; terminal {
		return nil
	}
	if task.Status == router.StatusPending {
		task.Status = router.StatusCancelled
		task.Summary = reason
		task.CompletedAt = o.now()
	} else {
		task.Status = router.StatusCancelling
		task.CancelReason = reason
		task.Summary = reason
	}
	if err := o.store.UpdateTask(ctx, task); err != nil {
		return err
	}
	if o.publisher != nil && task.Status == router.StatusCancelled {
		o.publisher.PublishTerminal(task)
	}
	return nil
}

func (o *Orchestrator) maybeEmitAggregate(ctx context.Context, task *router.Task) error {
	key := task.BatchID + "\x00" + task.AggregateKey
	if _, seen := o.aggregates[key]; seen {
		return nil
	}
	members, err := o.store.ListTasks(ctx, router.ListTasksQuery{BatchID: task.BatchID, Limit: 1000})
	if err != nil {
		return err
	}
	group := make([]*router.Task, 0)
	for _, member := range members {
		if member.AggregateKey == task.AggregateKey {
			group = append(group, member)
		}
	}
	if len(group) == 0 {
		return nil
	}
	for _, member := range group {
		if _, terminal := terminalStatuses[member.Status]; !terminal {
			return nil
		}
	}
	statusCounts := map[string]int{}
	summaries := make([]string, 0)
	taskIDs := make([]string, 0, len(group))
	for _, member := range group {
		statusCounts[member.Status]++
		if member.Summary != "" {
			summaries = append(summaries, member.Summary)
		}
		taskIDs = append(taskIDs, member.TaskID)
	}
	payload := map[string]any{
		"batch_id": task.BatchID, "aggregate_key": task.AggregateKey,
		"task_ids": taskIDs, "status_counts": statusCounts,
		"summary": joinSummaries(summaries), "metrics": []any{}, "schema_version": 1,
	}
	raw, _ := json.Marshal(payload)
	o.aggregates[key] = struct{}{}
	if o.publisher != nil {
		o.publisher.PublishAggregate(eventbus.Event{
			BatchID: task.BatchID, CallbackTopic: task.CallbackTopic,
			Kind: eventbus.KindAggregate, AggregateJSON: string(raw),
		})
	}
	return nil
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func joinSummaries(items []string) string {
	if len(items) == 0 {
		return ""
	}
	out := items[0]
	for i := 1; i < len(items); i++ {
		out += " | " + items[i]
	}
	return out
}

func countCompleted(members []*router.Task) int {
	n := 0
	for _, member := range members {
		if member.Status == router.StatusCompleted {
			n++
		}
	}
	return n
}
