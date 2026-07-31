package router

import (
	"context"
	"time"
)

// OnProgress clears first-progress deadline and extends execution lease.
func (r *Router) OnProgress(ctx context.Context, taskID, summary string) error {
	task, err := r.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return &Error{Msg: "task not found: " + taskID}
	}
	if task.Status != StatusRunning && task.Status != StatusCancelling {
		return nil
	}
	now := r.now()
	task.FirstProgressDeadlineAt = time.Time{}
	if task.Status == StatusRunning {
		task.ClaimExpiresAt = now.Add(time.Duration(r.cfg.executionTimeout(task)) * time.Second)
	}
	if summary != "" {
		task.Summary = summary
	}
	if err := r.store.UpdateTask(ctx, task); err != nil {
		return err
	}
	if r.emitter != nil && summary != "" {
		return r.emitter.EmitProgress(ctx, task, summary)
	}
	return nil
}

// OnCheckpoint persists an L1/L2 checkpoint and extends lease like progress.
func (r *Router) OnCheckpoint(
	ctx context.Context,
	taskID, checkpointID, summary, fieldsJSON string,
	resumeBlob []byte,
) error {
	task, err := r.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return &Error{Msg: "task not found: " + taskID}
	}
	if err := r.OnProgress(ctx, taskID, summary); err != nil {
		return err
	}
	now := r.now()
	var event *TaskEvent
	if r.emitter != nil {
		var err error
		event, err = r.emitter.EmitCheckpoint(ctx, task, checkpointID, summary, fieldsJSON)
		if err != nil {
			return err
		}
	}
	if err := r.store.InsertCheckpoint(ctx, &Checkpoint{
		CheckpointID: checkpointID,
		TaskID:       taskID,
		EventID:      eventIDOrZero(event),
		Summary:      summary,
		FieldsJSON:   fieldsJSON,
		ResumeBlob:   resumeBlob,
		CheckpointAt: now,
	}); err != nil {
		return err
	}
	task, err = r.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return &Error{Msg: "task not found: " + taskID}
	}
	task.ResumeFromCheckpoint = checkpointID
	return r.store.UpdateTask(ctx, task)
}

func eventIDOrZero(event *TaskEvent) int64 {
	if event == nil {
		return 0
	}
	return event.EventID
}
