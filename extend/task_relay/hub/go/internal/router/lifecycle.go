package router

import "context"

// DispatchTask creates a pending task or returns the existing row (idempotent).
func (r *Router) DispatchTask(ctx context.Context, spec TaskSpec) (*DispatchResponse, error) {
	if spec.TaskID == "" {
		return nil, &Error{Msg: "task_id is required"}
	}
	if spec.Goal == "" {
		return nil, &Error{Msg: "goal is required"}
	}
	existing, err := r.store.GetTask(ctx, spec.TaskID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return &DispatchResponse{
			TaskID:        existing.TaskID,
			CallbackTopic: existing.CallbackTopic,
			Status:        existing.Status,
			IdempotentHit: true,
			Attempt:       existing.Attempt,
		}, nil
	}
	return r.dispatchNewTask(ctx, spec)
}

// GetTask returns a persisted task row.
func (r *Router) GetTask(ctx context.Context, taskID string) (*Task, error) {
	if taskID == "" {
		return nil, &Error{Msg: "task_id is required"}
	}
	task, err := r.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, &Error{Msg: "task not found: " + taskID}
	}
	return task, nil
}

// ListTasks returns filtered persisted tasks.
func (r *Router) ListTasks(ctx context.Context, query ListTasksQuery) ([]*Task, error) {
	return r.store.ListTasks(ctx, query)
}

// Complete marks a task terminal with idempotent semantics.
func (r *Router) Complete(ctx context.Context, taskID, status, summary string) (*DispatchResponse, error) {
	if !IsTerminal(status) {
		return nil, &Error{Msg: "complete requires a terminal status, got " + status}
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
	if err := ValidateTransition(task.Status, status); err != nil {
		return nil, err
	}
	task.Status = status
	task.Summary = summary
	task.CompletedAt = r.now()
	if err := r.store.UpdateTask(ctx, task); err != nil {
		return nil, err
	}
	if r.reg != nil && task.WorkerID != "" {
		r.reg.DecRunning(task.WorkerID)
		r.reg.ReleaseCredit(task.WorkerID)
	}
	if err := r.notifyTerminal(ctx, task, status); err != nil {
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
