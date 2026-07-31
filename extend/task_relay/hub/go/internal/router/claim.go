package router

import (
	"context"

	"github.com/google/uuid"
)

// ClaimForPoll atomically claims up to maxTasks pending tasks for a worker.
func (r *Router) ClaimForPoll(ctx context.Context, workerID string, maxTasks int) ([]ClaimedTask, error) {
	if workerID == "" {
		return nil, &Error{Msg: "worker_id is required"}
	}
	if maxTasks <= 0 {
		return nil, &Error{Msg: "max_tasks must be > 0"}
	}
	candidates, err := r.store.ListTasks(ctx, ListTasksQuery{
		Statuses: []string{StatusPending},
		Limit:    maxTasks * 10,
	})
	if err != nil {
		return nil, err
	}

	claimed := make([]ClaimedTask, 0, maxTasks)
	for _, candidate := range candidates {
		if len(claimed) >= maxTasks {
			break
		}
		fresh, err := r.store.GetTask(ctx, candidate.TaskID)
		if err != nil || fresh == nil || fresh.Status != StatusPending {
			continue
		}
		fresh.Status = StatusRunning
		fresh.Attempt++
		fresh.WorkerID = workerID
		fresh.ClaimToken = uuid.NewString()
		if err := r.store.UpdateTask(ctx, fresh); err != nil {
			continue
		}
		claimed = append(claimed, ClaimedTask{
			TaskID:     fresh.TaskID,
			Attempt:    fresh.Attempt,
			ClaimToken: fresh.ClaimToken,
			Goal:       fresh.Goal,
		})
	}
	return claimed, nil
}

// CompleteOwned marks a terminal status for a task owned by workerID.
func (r *Router) CompleteOwned(
	ctx context.Context,
	workerID, taskID, status, summary string,
) (*DispatchResponse, error) {
	task, err := r.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, &Error{Msg: "task not found: " + taskID}
	}
	if task.WorkerID != "" && task.WorkerID != workerID {
		return nil, &Error{Msg: "task not owned by worker " + workerID}
	}
	return r.Complete(ctx, taskID, status, summary)
}
