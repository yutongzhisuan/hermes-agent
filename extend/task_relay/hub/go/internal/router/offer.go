package router

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// OfferedTask is a metadata-only two-step poll reservation.
type OfferedTask struct {
	TaskID         string
	ClaimToken     string
	ClaimExpiresAt time.Time
	Attempt        int
	TimeoutSeconds int
}

// OfferTasksForPoll reserves pending tasks without moving them to running.
func (r *Router) OfferTasksForPoll(
	ctx context.Context,
	workerID string,
	maxTasks int,
	claims *WorkerClaims,
) ([]OfferedTask, error) {
	if workerID == "" || maxTasks <= 0 {
		return nil, nil
	}
	candidates, err := r.store.ListTasks(ctx, ListTasksQuery{
		Statuses: []string{StatusPending},
		Limit:    maxTasks * 20,
	})
	if err != nil {
		return nil, err
	}
	offered := make([]OfferedTask, 0, maxTasks)
	now := r.now()
	for _, candidate := range candidates {
		if len(offered) >= maxTasks {
			break
		}
		item, err := r.offerOne(ctx, workerID, candidate, claims, now)
		if err != nil || item == nil {
			continue
		}
		offered = append(offered, *item)
	}
	return offered, nil
}

// ClaimOfferedTask confirms a two-step offer and moves the task to running.
func (r *Router) ClaimOfferedTask(
	ctx context.Context,
	taskID, workerID, claimToken string,
	claims *WorkerClaims,
) (*ClaimedTask, error) {
	fresh, err := r.store.GetTask(ctx, taskID)
	if err != nil || fresh == nil || fresh.Status != StatusPending {
		return nil, err
	}
	if fresh.ClaimToken != claimToken || !hasActiveOffer(fresh, r.now()) {
		return nil, nil
	}
	return r.confirmOfferedClaim(ctx, fresh, workerID, claims)
}

// ReleaseOffer releases a pending two-step offer back to the queue.
func (r *Router) ReleaseOffer(ctx context.Context, taskID, claimToken string) (bool, error) {
	fresh, err := r.store.GetTask(ctx, taskID)
	if err != nil || fresh == nil || fresh.Status != StatusPending {
		return false, err
	}
	if claimToken != "" && fresh.ClaimToken != claimToken {
		return false, nil
	}
	if fresh.ClaimToken == "" {
		return false, nil
	}
	fresh.ClaimToken = ""
	fresh.ClaimExpiresAt = time.Time{}
	return true, r.store.UpdateTask(ctx, fresh)
}

func (r *Router) offerOne(
	ctx context.Context,
	workerID string,
	candidate *Task,
	claims *WorkerClaims,
	now time.Time,
) (*OfferedTask, error) {
	if hasActiveOffer(candidate, now) {
		return nil, nil
	}
	if !r.canClaim(ctx, workerID, candidate, claims) {
		return nil, nil
	}
	fresh, err := r.store.GetTask(ctx, candidate.TaskID)
	if err != nil || fresh == nil || fresh.Status != StatusPending {
		return nil, err
	}
	token := uuid.NewString()
	fresh.ClaimToken = token
	fresh.ClaimExpiresAt = now.Add(time.Duration(r.cfg.PollOfferSeconds) * time.Second)
	if err := r.store.UpdateTask(ctx, fresh); err != nil {
		return nil, err
	}
	return &OfferedTask{
		TaskID:         fresh.TaskID,
		ClaimToken:     token,
		ClaimExpiresAt: fresh.ClaimExpiresAt,
		Attempt:        fresh.Attempt + 1,
		TimeoutSeconds: r.cfg.executionTimeout(fresh),
	}, nil
}

func (r *Router) confirmOfferedClaim(
	ctx context.Context,
	task *Task,
	workerID string,
	claims *WorkerClaims,
) (*ClaimedTask, error) {
	if !r.canClaim(ctx, workerID, task, claims) {
		return nil, nil
	}
	now := r.now()
	token := task.ClaimToken
	task.Status = StatusRunning
	task.WorkerID = workerID
	task.Attempt++
	task.StartedAt = now
	task.FirstProgressDeadlineAt = now.Add(time.Duration(r.cfg.firstProgress(task)) * time.Second)
	task.ClaimExpiresAt = now.Add(time.Duration(r.cfg.executionTimeout(task)) * time.Second)
	task.QueueDeadlineAt = time.Time{}
	if err := r.store.UpdateTask(ctx, task); err != nil {
		return nil, err
	}
	if r.reg != nil {
		r.reg.IncRunning(workerID)
	}
	recordClaimed()
	return &ClaimedTask{
		TaskID:         task.TaskID,
		Attempt:        task.Attempt,
		ClaimToken:     token,
		Goal:           task.Goal,
		TimeoutSeconds: r.cfg.executionTimeout(task),
	}, nil
}

func hasActiveOffer(task *Task, now time.Time) bool {
	return task != nil &&
		task.Status == StatusPending &&
		task.ClaimToken != "" &&
		!task.ClaimExpiresAt.IsZero() &&
		now.Before(task.ClaimExpiresAt)
}
