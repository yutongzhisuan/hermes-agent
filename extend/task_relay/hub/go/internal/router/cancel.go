package router

import (
	"context"
	"time"
)

// Cancel requests cancellation for a non-terminal task.
func (r *Router) Cancel(ctx context.Context, taskID, reason string, graceSeconds int) (*DispatchResponse, error) {
	if taskID == "" {
		return nil, &Error{Msg: "task_id is required"}
	}
	if reason == "" {
		reason = "cancelled by master"
	}
	task, err := r.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, &Error{Msg: "task not found: " + taskID}
	}
	if IsTerminal(task.Status) {
		return &DispatchResponse{
			TaskID:        task.TaskID,
			CallbackTopic: task.CallbackTopic,
			Status:        task.Status,
			IdempotentHit: true,
			Attempt:       task.Attempt,
		}, nil
	}
	switch task.Status {
	case StatusPending:
		task.Status = StatusCancelled
		task.Summary = reason
		task.CompletedAt = r.now()
	case StatusRunning, StatusCancelling:
		grace := r.cfg.cancelGrace(graceSeconds)
		task.Status = StatusCancelling
		task.CancelReason = reason
		task.Summary = reason
		task.ClaimExpiresAt = r.now().Add(time.Duration(grace) * time.Second)
		task.FirstProgressDeadlineAt = time.Time{}
	default:
		return nil, &Error{Msg: "invalid transition " + task.Status + " -> cancelled"}
	}
	if err := r.store.UpdateTask(ctx, task); err != nil {
		return nil, err
	}
	if r.emitter != nil {
		switch task.Status {
		case StatusCancelled:
			_ = r.emitter.EmitTerminal(ctx, task, StatusCancelled, reason, task.Error)
		case StatusCancelling:
			_ = r.emitter.EmitProgress(ctx, task, "cancel requested: "+reason)
		}
	}
	return &DispatchResponse{
		TaskID:        task.TaskID,
		CallbackTopic: task.CallbackTopic,
		Status:        task.Status,
		IdempotentHit: false,
		Attempt:       task.Attempt,
	}, nil
}
