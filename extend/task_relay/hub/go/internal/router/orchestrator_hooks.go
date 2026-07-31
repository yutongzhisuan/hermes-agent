package router

import (
	"context"
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
	_, err := r.Cancel(ctx, taskID, reason, 0)
	return err
}
