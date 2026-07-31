package router

import (
	"context"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/audit"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/contextcrypto"
)

// DispatchTask creates a pending task or returns the existing row (idempotent).
func (r *Router) DispatchTask(
	ctx context.Context,
	spec TaskSpec,
	masterSessionID string,
) (*DispatchResponse, error) {
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
	return r.dispatchNewTask(ctx, spec, masterSessionID)
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
	startedAt := task.StartedAt
	workerID := task.WorkerID
	task.Status = status
	task.Summary = summary
	task.CompletedAt = r.now()
	if err := r.store.UpdateTask(ctx, task); err != nil {
		return nil, err
	}
	if r.reg != nil && workerID != "" {
		r.reg.DecRunning(workerID)
		r.reg.ReleaseCredit(workerID)
	}
	recordTerminal(status)
	if !startedAt.IsZero() {
		observeTaskLatency(status, workerID, task.CompletedAt.Sub(startedAt).Seconds())
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

func (r *Router) dispatchNewTask(
	ctx context.Context,
	spec TaskSpec,
	masterSessionID string,
) (*DispatchResponse, error) {
	topic := spec.CallbackTopic
	if topic == "" {
		topic = "default"
	}
	now := r.now()
	maxAttempts := spec.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = r.cfg.MaxAttempts
	}
	queueTimeout := spec.QueueTimeoutSeconds
	if queueTimeout <= 0 {
		queueTimeout = r.cfg.QueueTimeoutSeconds
	}
	contextJSON := spec.ContextJSON
	if contextJSON != "" && r.cfg.EncryptInlineContextAtRest && r.cfg.JWTSecret != "" {
		encrypted, err := contextcrypto.EncryptContextJSON(contextJSON, r.cfg.JWTSecret)
		if err != nil {
			return nil, &Error{Msg: err.Error()}
		}
		contextJSON = encrypted
	}
	task := &Task{
		TaskID:               spec.TaskID,
		BatchID:              spec.BatchID,
		MasterSessionID:      masterSessionID,
		Goal:                 spec.Goal,
		ParamsJSON:           spec.ParamsJSON,
		ContextJSON:          contextJSON,
		CallbackTopic:        topic,
		Status:               StatusPending,
		Attempt:              0,
		MaxAttempts:          maxAttempts,
		TargetWorker:         spec.TargetWorker,
		ToolsetsJSON:         encodeToolsets(spec.Toolsets),
		DependsOnJSON:        encodeStringList(spec.DependsOn),
		AggregateKey:         spec.AggregateKey,
		MinResourcesJSON:     spec.MinResourcesJSON,
		TraceContextJSON:     spec.TraceContextJSON,
		AllowedWorkerIDsJSON: spec.AllowedWorkerIDsJSON,
		DenyWorkerIDsJSON:    spec.DenyWorkerIDsJSON,
		ResumeFromCheckpoint: spec.ResumeFromCheckpoint,
		Priority:             spec.Priority,
		QueueTimeoutSeconds:  spec.QueueTimeoutSeconds,
		FirstProgressSeconds: spec.FirstProgressSeconds,
		TimeoutSeconds:       spec.TimeoutSeconds,
		CreatedAt:            now,
		QueueDeadlineAt:      now.Add(time.Duration(queueTimeout) * time.Second),
	}
	if err := r.store.InsertTask(ctx, task); err != nil {
		return nil, err
	}
	if err := audit.RecordDispatchACL(ctx, r.store, audit.DispatchACLInput{
		TaskID: task.TaskID, TargetWorker: task.TargetWorker,
		AllowedWorkerIDsJSON: task.AllowedWorkerIDsJSON, DenyWorkerIDsJSON: task.DenyWorkerIDsJSON,
	}, masterSessionID); err != nil {
		return nil, err
	}
	recordDispatched(spec.BatchID != "")
	return &DispatchResponse{
		TaskID:        task.TaskID,
		CallbackTopic: task.CallbackTopic,
		Status:        task.Status,
		IdempotentHit: false,
		Attempt:       task.Attempt,
	}, nil
}
