package router

import (
	"context"
	"time"
)

// Store abstracts task persistence for the Go router port.
type Store interface {
	GetTask(ctx context.Context, taskID string) (*Task, error)
	InsertTask(ctx context.Context, task *Task) error
	UpdateTask(ctx context.Context, task *Task) error
}

// Router implements the core M1 dispatch/complete state machine (Go port scaffold).
type Router struct {
	store Store
	now   func() time.Time
}

// NewRouter constructs a Router backed by store.
func NewRouter(store Store) *Router {
	return &Router{
		store: store,
		now:   time.Now,
	}
}

// DispatchTask creates a pending task or returns the existing row (idempotent).
func (r *Router) DispatchTask(ctx context.Context, spec TaskSpec) (*DispatchResponse, error) {
	if spec.TaskID == "" {
		return nil, &Error{Msg: "task_id is required"}
	}
	if spec.Goal == "" {
		return nil, &Error{Msg: "goal is required"}
	}
	topic := spec.CallbackTopic
	if topic == "" {
		topic = "default"
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

	now := r.now()
	task := &Task{
		TaskID:        spec.TaskID,
		Goal:          spec.Goal,
		CallbackTopic: topic,
		Status:        StatusPending,
		Attempt:       0,
		CreatedAt:     now,
	}
	if err := r.store.InsertTask(ctx, task); err != nil {
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
	return &DispatchResponse{
		TaskID:        task.TaskID,
		CallbackTopic: task.CallbackTopic,
		Status:        task.Status,
		IdempotentHit: false,
		Attempt:       task.Attempt,
	}, nil
}
