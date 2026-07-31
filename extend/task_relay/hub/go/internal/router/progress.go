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
	task.ClaimExpiresAt = now.Add(time.Duration(r.cfg.executionTimeout(task)) * time.Second)
	if summary != "" {
		task.Summary = summary
	}
	return r.store.UpdateTask(ctx, task)
}

// OnCheckpoint persists an L1/L2 checkpoint and extends lease like progress.
func (r *Router) OnCheckpoint(
	ctx context.Context,
	taskID, checkpointID, summary string,
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
	return r.store.InsertCheckpoint(ctx, &Checkpoint{
		CheckpointID: checkpointID,
		TaskID:       taskID,
		Summary:      summary,
		ResumeBlob:   resumeBlob,
		CheckpointAt: r.now(),
	})
}
