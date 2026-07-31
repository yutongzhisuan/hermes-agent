package router

import (
	"context"
	"time"
)

func (r *Router) notifyTerminal(ctx context.Context, task *Task, status string) error {
	if r.orch == nil {
		return nil
	}
	ready, err := r.orch.OnTaskTerminal(ctx, task, status)
	if err != nil {
		return err
	}
	if r.onReady != nil {
		for _, taskID := range ready {
			r.onReady(ctx, taskID)
		}
	}
	return nil
}

// OnCancel cancels a task for orchestrator-driven reasons.
func (r *Router) OnCancel(ctx context.Context, taskID, reason string) error {
	task, err := r.store.GetTask(ctx, taskID)
	if err != nil || task == nil {
		return err
	}
	if IsTerminal(task.Status) {
		return nil
	}
	switch task.Status {
	case StatusPending:
		task.Status = StatusCancelled
		task.Summary = reason
		task.CompletedAt = r.now()
	case StatusRunning, StatusCancelling:
		task.Status = StatusCancelling
		task.CancelReason = reason
		task.Summary = reason
		task.ClaimExpiresAt = r.now().Add(time.Duration(r.cfg.CancelGraceSeconds) * time.Second)
	default:
		return &Error{Msg: "invalid transition " + task.Status + " -> cancelled"}
	}
	return r.store.UpdateTask(ctx, task)
}
