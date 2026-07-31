package router

import "context"

// Cancel requests cancellation for a non-terminal task.
func (r *Router) Cancel(ctx context.Context, taskID, reason string) (*DispatchResponse, error) {
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
		task.Status = StatusCancelling
		task.Summary = reason
	default:
		return nil, &Error{Msg: "invalid transition " + task.Status + " -> cancelled"}
	}
	if err := r.store.UpdateTask(ctx, task); err != nil {
		return nil, err
	}
	return &DispatchResponse{
		TaskID:        task.TaskID,
		CallbackTopic: task.CallbackTopic,
		Status:        task.Status,
		IdempotentHit: false,
		Attempt:       task.Attempt,
	}, nil
}
