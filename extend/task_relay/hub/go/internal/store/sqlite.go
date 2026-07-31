package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	_ "modernc.org/sqlite"
)

// SQLite persists tasks using the portable Hub schema subset.
type SQLite struct {
	db *sql.DB
}

// OpenSQLite opens path and ensures schema exists.
func OpenSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(tasksSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	applySQLiteMigrations(db)
	return &SQLite{db: db}, nil
}

// Close releases the database handle.
func (s *SQLite) Close() error {
	return s.db.Close()
}

func (s *SQLite) GetTask(ctx context.Context, taskID string) (*router.Task, error) {
	row := s.db.QueryRow(taskSelectSQL+` WHERE task_id = ?`, taskID)
	return scanTaskRow(row)
}

func (s *SQLite) InsertTask(_ context.Context, task *router.Task) error {
	_, err := s.db.Exec(
		`INSERT INTO tasks (
		 task_id, batch_id, goal, callback_topic, status, attempt, max_attempts,
		 target_worker, toolsets_json, depends_on_json, aggregate_key, min_resources_json,
		 priority, queue_timeout_seconds, first_progress_seconds, timeout_seconds,
		 queue_deadline_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.TaskID,
		nullString(task.BatchID),
		task.Goal,
		task.CallbackTopic,
		task.Status,
		task.Attempt,
		task.MaxAttempts,
		nullString(task.TargetWorker),
		nullString(task.ToolsetsJSON),
		nullString(task.DependsOnJSON),
		nullString(task.AggregateKey),
		nullString(task.MinResourcesJSON),
		task.Priority,
		nullInt(task.QueueTimeoutSeconds),
		nullInt(task.FirstProgressSeconds),
		nullInt(task.TimeoutSeconds),
		nullTime(task.QueueDeadlineAt),
		float64(task.CreatedAt.Unix()),
	)
	return err
}

func (s *SQLite) UpdateTask(_ context.Context, task *router.Task) error {
	res, err := s.db.Exec(
		`UPDATE tasks SET
		 status = ?, summary = ?, error = ?, attempt = ?, worker_id = ?, claim_token = ?,
		 queue_deadline_at = ?, first_progress_deadline_at = ?, claim_expires_at = ?,
		 started_at = ?, completed_at = ?, cancel_reason = ?
		 WHERE task_id = ?`,
		task.Status,
		task.Summary,
		nullString(task.Error),
		task.Attempt,
		nullString(task.WorkerID),
		nullString(task.ClaimToken),
		nullTime(task.QueueDeadlineAt),
		nullTime(task.FirstProgressDeadlineAt),
		nullTime(task.ClaimExpiresAt),
		nullTime(task.StartedAt),
		nullTime(task.CompletedAt),
		nullString(task.CancelReason),
		task.TaskID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("task %s not found", task.TaskID)
	}
	return nil
}

func (s *SQLite) GetBatch(_ context.Context, batchID string) (*router.Batch, error) {
	row := s.db.QueryRow(
		`SELECT batch_id, callback_topic, batch_spec_hash, policy_json, created_at, batch_deadline_at
		 FROM batches WHERE batch_id = ?`,
		batchID,
	)
	batch, err := scanBatchRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return batch, err
}

func (s *SQLite) InsertBatch(_ context.Context, batch *router.Batch) error {
	created := batch.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	_, err := s.db.Exec(
		`INSERT INTO batches (batch_id, callback_topic, batch_spec_hash, policy_json, created_at, batch_deadline_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		batch.BatchID,
		batch.CallbackTopic,
		batch.BatchSpecHash,
		nullString(batch.PolicyJSON),
		float64(created.Unix()),
		nullTime(batch.BatchDeadlineAt),
	)
	return err
}

const taskSelectSQL = `
SELECT task_id, batch_id, goal, callback_topic, status, attempt, max_attempts,
       worker_id, claim_token, target_worker, toolsets_json, depends_on_json,
       aggregate_key, min_resources_json, error, priority,
       queue_timeout_seconds, first_progress_seconds, timeout_seconds,
       queue_deadline_at, first_progress_deadline_at, claim_expires_at,
       started_at, summary, cancel_reason, created_at, completed_at
FROM tasks`

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return float64(value.Unix())
}
