package router

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/resources"
)

// ClaimedTask is returned to a worker after a successful poll claim.
type ClaimedTask struct {
	TaskID     string
	Attempt    int
	ClaimToken string
	Goal       string
}

// ClaimForPoll atomically claims up to maxTasks pending tasks for a worker.
func (r *Router) ClaimForPoll(
	ctx context.Context,
	workerID string,
	maxTasks int,
	claims *WorkerClaims,
) ([]ClaimedTask, error) {
	if workerID == "" {
		return nil, &Error{Msg: "worker_id is required"}
	}
	if maxTasks <= 0 {
		return nil, &Error{Msg: "max_tasks must be > 0"}
	}
	candidates, err := r.store.ListTasks(ctx, ListTasksQuery{
		Statuses: []string{StatusPending},
		Limit:    maxTasks * 20,
	})
	if err != nil {
		return nil, err
	}

	claimed := make([]ClaimedTask, 0, maxTasks)
	for _, candidate := range candidates {
		if len(claimed) >= maxTasks {
			break
		}
		item, err := r.claimOne(ctx, workerID, candidate.TaskID, claims)
		if err != nil || item == nil {
			continue
		}
		claimed = append(claimed, *item)
	}
	return claimed, nil
}

// ClaimForWorker atomically claims one pending task for Mode C push.
func (r *Router) ClaimForWorker(
	ctx context.Context,
	taskID, workerID string,
	claims *WorkerClaims,
) (*ClaimedTask, error) {
	return r.claimOne(ctx, workerID, taskID, claims)
}

func (r *Router) claimOne(
	ctx context.Context,
	workerID, taskID string,
	claims *WorkerClaims,
) (*ClaimedTask, error) {
	fresh, err := r.store.GetTask(ctx, taskID)
	if err != nil || fresh == nil || fresh.Status != StatusPending {
		return nil, err
	}
	ready, err := r.isClaimable(ctx, fresh)
	if err != nil || !ready {
		return nil, err
	}
	if r.reg != nil {
		worker := r.reg.Get(workerID)
		if worker.WorkerID == "" || !r.reg.IsEligible(&worker, fresh, claims) {
			return nil, nil
		}
		if worker.RunningTasks >= worker.MaxConcurrent {
			return nil, nil
		}
		if fresh.MinResourcesJSON != "" {
			req := resources.ParseMinResources(fresh.MinResourcesJSON)
			if !resources.WorkerMeetsResources(resources.WorkerView{ResourcesJSON: worker.ResourcesJSON}, req) {
				return nil, nil
			}
		}
	}
	now := r.now()
	fresh.Status = StatusRunning
	fresh.Attempt++
	fresh.WorkerID = workerID
	fresh.ClaimToken = uuid.NewString()
	fresh.StartedAt = now
	fresh.FirstProgressDeadlineAt = now.Add(time.Duration(r.cfg.firstProgress(fresh)) * time.Second)
	fresh.ClaimExpiresAt = now.Add(time.Duration(r.cfg.executionTimeout(fresh)) * time.Second)
	fresh.QueueDeadlineAt = time.Time{}
	if err := r.store.UpdateTask(ctx, fresh); err != nil {
		return nil, err
	}
	if r.reg != nil {
		r.reg.IncRunning(workerID)
	}
	return &ClaimedTask{
		TaskID:     fresh.TaskID,
		Attempt:    fresh.Attempt,
		ClaimToken: fresh.ClaimToken,
		Goal:       fresh.Goal,
	}, nil
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

func (r *Router) isClaimable(ctx context.Context, task *Task) (bool, error) {
	if r.orch == nil {
		return true, nil
	}
	return r.orch.IsTaskReady(ctx, task)
}
