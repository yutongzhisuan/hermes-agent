package router

import (
	"context"
	"time"
)

const cancelReasonTimeout = "execution timeout"

// TickTimeouts evaluates queue, first-progress, lease, and cancel-grace deadlines.
func (r *Router) TickTimeouts(ctx context.Context) error {
	now := r.now()
	tasks, err := r.store.ListTasks(ctx, ListTasksQuery{
		Statuses: []string{StatusPending, StatusRunning, StatusCancelling},
		Limit:    1000,
	})
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if err := r.timeoutTask(ctx, task, now); err != nil {
			return err
		}
	}
	if r.orch != nil {
		if err := r.orch.EnforceBatchDeadlines(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *Router) timeoutTask(ctx context.Context, task *Task, now time.Time) error {
	switch task.Status {
	case StatusPending:
		if task.ClaimToken != "" && !task.ClaimExpiresAt.IsZero() && !now.Before(task.ClaimExpiresAt) {
			task.ClaimToken = ""
			task.ClaimExpiresAt = time.Time{}
			if err := r.store.UpdateTask(ctx, task); err != nil {
				return err
			}
		}
		if !task.QueueDeadlineAt.IsZero() && !now.Before(task.QueueDeadlineAt) {
			return r.settleLost(ctx, task, now, "queue timeout")
		}
	case StatusRunning:
		if !task.FirstProgressDeadlineAt.IsZero() && !now.Before(task.FirstProgressDeadlineAt) {
			return r.settleLost(ctx, task, now, "first progress timeout")
		}
		if !task.ClaimExpiresAt.IsZero() && !now.Before(task.ClaimExpiresAt) {
			return r.enterCancelling(ctx, task, now, cancelReasonTimeout)
		}
	case StatusCancelling:
		if !task.ClaimExpiresAt.IsZero() && !now.Before(task.ClaimExpiresAt) {
			if task.CancelReason == cancelReasonTimeout {
				return r.settleFailed(ctx, task, now, "execution/lease timeout")
			}
			return r.settleCancelled(ctx, task, now)
		}
	}
	return nil
}

func (r *Router) settleLost(ctx context.Context, task *Task, now time.Time, reason string) error {
	workerID := task.WorkerID
	if task.AllowRedispatch && task.Attempt < task.MaxAttempts {
		if err := r.requeueTask(ctx, task, now, reason); err != nil {
			return err
		}
		if r.reg != nil && workerID != "" {
			r.reg.DecRunning(workerID)
		}
		return nil
	}
	task.Status = StatusLost
	task.Summary = reason
	task.CompletedAt = now
	task.ClaimExpiresAt = time.Time{}
	task.FirstProgressDeadlineAt = time.Time{}
	if err := r.store.UpdateTask(ctx, task); err != nil {
		return err
	}
	if r.emitter != nil {
		_ = r.emitter.EmitTerminal(ctx, task, StatusLost, reason, "")
	}
	if r.reg != nil && workerID != "" {
		r.reg.DecRunning(workerID)
	}
	return nil
}

func (r *Router) settleFailed(ctx context.Context, task *Task, now time.Time, reason string) error {
	workerID := task.WorkerID
	if task.AllowRedispatch && task.Attempt < task.MaxAttempts {
		if err := r.requeueTask(ctx, task, now, reason); err != nil {
			return err
		}
		if r.reg != nil && workerID != "" {
			r.reg.DecRunning(workerID)
			r.reg.ReleaseCredit(workerID)
		}
		return nil
	}
	task.Status = StatusFailed
	task.Summary = reason
	task.CompletedAt = now
	task.ClaimExpiresAt = time.Time{}
	task.FirstProgressDeadlineAt = time.Time{}
	if err := r.store.UpdateTask(ctx, task); err != nil {
		return err
	}
	if r.emitter != nil {
		_ = r.emitter.EmitTerminal(ctx, task, StatusFailed, reason, "")
	}
	if r.reg != nil && workerID != "" {
		r.reg.DecRunning(workerID)
		r.reg.ReleaseCredit(workerID)
	}
	return nil
}

func (r *Router) settleCancelled(ctx context.Context, task *Task, now time.Time) error {
	workerID := task.WorkerID
	task.Status = StatusCancelled
	if task.Summary == "" {
		task.Summary = "cancel grace expired"
	}
	task.CompletedAt = now
	task.ClaimExpiresAt = time.Time{}
	task.FirstProgressDeadlineAt = time.Time{}
	if err := r.store.UpdateTask(ctx, task); err != nil {
		return err
	}
	if r.emitter != nil {
		_ = r.emitter.EmitTerminal(ctx, task, StatusCancelled, task.Summary, task.Error)
	}
	if r.reg != nil && workerID != "" {
		r.reg.DecRunning(workerID)
		r.reg.ReleaseCredit(workerID)
	}
	return nil
}

func (r *Router) enterCancelling(ctx context.Context, task *Task, now time.Time, reason string) error {
	task.Status = StatusCancelling
	task.CancelReason = reason
	task.Summary = reason
	task.ClaimExpiresAt = now.Add(time.Duration(r.cfg.CancelGraceSeconds) * time.Second)
	task.FirstProgressDeadlineAt = time.Time{}
	if err := r.store.UpdateTask(ctx, task); err != nil {
		return err
	}
	if r.emitter != nil {
		_ = r.emitter.EmitProgress(ctx, task, "cancel requested: "+reason)
	}
	return nil
}
