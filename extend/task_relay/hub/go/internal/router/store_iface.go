package router

import (
	"context"
	"time"
)

// Store abstracts task, batch, checkpoint, event, and worker persistence.
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

	AppendEvent(ctx context.Context, event *TaskEvent) (*TaskEvent, error)
	ListEventsForFilter(ctx context.Context, filter EventFilter) ([]*TaskEvent, error)
	OldestEventIDForFilter(ctx context.Context, topic, batchID, taskID string) (*int64, error)
	OldestEventID(ctx context.Context) (*int64, error)
	NewestEventID(ctx context.Context) (*int64, error)
	PruneEventsBefore(ctx context.Context, cutoff time.Time) (int64, error)

	UpsertWorker(ctx context.Context, worker *Worker) error
	GetWorker(ctx context.Context, workerID string) (*Worker, error)
	ListWorkers(ctx context.Context, onlySchedulable bool) ([]*Worker, error)
	DeleteWorker(ctx context.Context, workerID string) error
}

// TaskOrchestrator implements M3 DAG and batch policy hooks.
type TaskOrchestrator interface {
	IsTaskReady(ctx context.Context, task *Task) (bool, error)
	OnTaskTerminal(ctx context.Context, task *Task, status string) ([]string, error)
	EnforceBatchDeadlines(ctx context.Context) error
}
