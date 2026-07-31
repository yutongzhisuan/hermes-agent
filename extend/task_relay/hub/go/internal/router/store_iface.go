package router

import (
	"context"
	"time"
)

// Store abstracts task, batch, and checkpoint persistence.
type Store interface {
	GetTask(ctx context.Context, taskID string) (*Task, error)
	InsertTask(ctx context.Context, task *Task) error
	UpdateTask(ctx context.Context, task *Task) error
	ListTasks(ctx context.Context, query ListTasksQuery) ([]*Task, error)
	GetBatch(ctx context.Context, batchID string) (*Batch, error)
	InsertBatch(ctx context.Context, batch *Batch) error
	UpdateBatch(ctx context.Context, batch *Batch) error
	ListExpiredBatches(ctx context.Context, now time.Time) ([]*Batch, error)
	InsertCheckpoint(ctx context.Context, checkpoint *Checkpoint) error
	GetLatestCheckpoint(ctx context.Context, taskID string) (*Checkpoint, error)
	InsertAuditLog(ctx context.Context, action, taskID, masterSessionID, payloadJSON string) error
	CountAuditLogs(ctx context.Context, taskID string) (int, error)
}

// TaskOrchestrator implements M3 DAG and batch policy hooks.
type TaskOrchestrator interface {
	IsTaskReady(ctx context.Context, task *Task) (bool, error)
	OnTaskTerminal(ctx context.Context, task *Task, status string) ([]string, error)
	EnforceBatchDeadlines(ctx context.Context) error
}
