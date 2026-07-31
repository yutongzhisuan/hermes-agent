package router

import (
	"context"
	"strconv"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/audit"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/contextcrypto"
)

// DispatchTask creates a pending task or returns the existing row (idempotent).
func (r *Router) DispatchTask(
	ctx context.Context,
	spec TaskSpec,
	masterSessionID string,
	allowRedispatch bool,
) (*DispatchResponse, error) {
	if spec.TaskID == "" {
		return nil, &Error{Msg: "task_id is required"}
	}
	if spec.Goal == "" {
		return nil, &Error{Msg: "goal is required"}
	}
	return r.dispatchSingle(ctx, spec, masterSessionID, allowRedispatch, spec.BatchID)
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
func (r *Router) Complete(
	ctx context.Context,
	taskID, status, summary string,
	input CompleteInput,
) (*DispatchResponse, error) {
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
	previousStatus := task.Status
	workerID := task.WorkerID
	task.Status = status
	task.Summary = summary
	task.ResultJSON = input.ResultJSON
	task.FieldsJSON = input.FieldsJSON
	task.UsageJSON = input.UsageJSON
	task.Error = input.Error
	task.CompletedAt = r.now()
	task.FirstProgressDeadlineAt = time.Time{}
	task.ClaimExpiresAt = time.Time{}
	if err := r.store.UpdateTask(ctx, task); err != nil {
		return nil, err
	}
	if r.emitter != nil {
		_ = r.emitter.EmitTerminal(ctx, task, status, summary, task.Error)
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
	if r.onTerminal != nil && (previousStatus == StatusRunning || previousStatus == StatusCancelling) {
		r.onTerminal(ctx, taskID, workerID)
	}
	return &DispatchResponse{
		TaskID:        task.TaskID,
		CallbackTopic: task.CallbackTopic,
		Status:        task.Status,
		IdempotentHit: false,
		Attempt:       task.Attempt,
	}, nil
}

func (r *Router) dispatchSingle(
	ctx context.Context,
	spec TaskSpec,
	masterSessionID string,
	allowRedispatch bool,
	batchID string,
) (*DispatchResponse, error) {
	existing, err := r.store.GetTask(ctx, spec.TaskID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		resp, err := r.handleExisting(ctx, existing, allowRedispatch)
		if err != nil {
			return nil, err
		}
		if batchID != "" && existing.BatchID == "" {
			existing.BatchID = batchID
			if err := r.store.UpdateTask(ctx, existing); err != nil {
				return nil, err
			}
		}
		return resp, nil
	}
	if batchID != "" {
		spec.BatchID = batchID
	}
	return r.dispatchNewTask(ctx, spec, masterSessionID, allowRedispatch)
}

func (r *Router) handleExisting(
	ctx context.Context,
	task *Task,
	allowRedispatch bool,
) (*DispatchResponse, error) {
	if !IsTerminal(task.Status) {
		return r.responseFromTask(task, true, nil), nil
	}
	if task.AllowRedispatch != allowRedispatch {
		task.AllowRedispatch = allowRedispatch
		if err := r.store.UpdateTask(ctx, task); err != nil {
			return nil, err
		}
	}
	if allowRedispatch && isRedispatchable(task.Status) && task.Attempt < task.MaxAttempts {
		if err := r.reopenForRedispatch(ctx, task); err != nil {
			return nil, err
		}
		return r.responseFromTask(task, false, nil), nil
	}
	return r.responseFromTask(task, true, r.existingResult(task)), nil
}

func (r *Router) reopenForRedispatch(ctx context.Context, task *Task) error {
	now := r.now()
	queueTimeout := r.cfg.queueTimeout(task)
	task.Status = StatusPending
	task.WorkerID = ""
	task.ClaimToken = ""
	task.ClaimExpiresAt = time.Time{}
	task.FirstProgressDeadlineAt = time.Time{}
	task.QueueDeadlineAt = now.Add(time.Duration(queueTimeout) * time.Second)
	task.StartedAt = time.Time{}
	task.CompletedAt = time.Time{}
	task.Summary = ""
	task.CancelReason = ""
	task.ResultJSON = ""
	task.Error = ""
	if err := r.store.UpdateTask(ctx, task); err != nil {
		return err
	}
	if r.emitter != nil {
		_ = r.emitter.EmitStatus(ctx, task, StatusPending)
	}
	return nil
}

func (r *Router) existingResult(task *Task) *ExistingResult {
	return &ExistingResult{
		TaskID:             task.TaskID,
		Status:             task.Status,
		Summary:            task.Summary,
		ResultText:         task.ResultJSON,
		Error:              task.Error,
		WorkerID:           task.WorkerID,
		Attempt:            task.Attempt,
		MaxAttempts:        task.MaxAttempts,
		BatchID:            task.BatchID,
		LatestCheckpointID: task.ResumeFromCheckpoint,
		StartedAt:          task.StartedAt,
		CompletedAt:        task.CompletedAt,
		FieldsJSON:         task.FieldsJSON,
		UsageJSON:          task.UsageJSON,
	}
}

func (r *Router) responseFromTask(task *Task, idempotentHit bool, existing *ExistingResult) *DispatchResponse {
	return &DispatchResponse{
		TaskID:         task.TaskID,
		CallbackTopic:  task.CallbackTopic,
		Status:         task.Status,
		IdempotentHit:  idempotentHit,
		Attempt:        task.Attempt,
		ExistingResult: existing,
	}
}

func (r *Router) dispatchNewTask(
	ctx context.Context,
	spec TaskSpec,
	masterSessionID string,
	allowRedispatch bool,
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
		AllowRedispatch:      allowRedispatch,
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
	if r.emitter != nil {
		_ = r.emitter.EmitStatus(ctx, task, StatusPending)
	}
	return r.responseFromTask(task, false, nil), nil
}

func (r *Router) requeueTask(ctx context.Context, task *Task, now time.Time, reason string) error {
	queueTimeout := r.cfg.queueTimeout(task)
	task.Status = StatusPending
	task.WorkerID = ""
	task.ClaimToken = ""
	task.ClaimExpiresAt = time.Time{}
	task.FirstProgressDeadlineAt = time.Time{}
	task.QueueDeadlineAt = now.Add(time.Duration(queueTimeout) * time.Second)
	task.Summary = reason + "; requeuing for attempt " + strconv.Itoa(task.Attempt+1)
	if err := r.store.UpdateTask(ctx, task); err != nil {
		return err
	}
	if r.emitter != nil {
		_ = r.emitter.EmitProgress(ctx, task, task.Summary)
		_ = r.emitter.EmitStatus(ctx, task, StatusPending)
	}
	return nil
}
