package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Postgres persists tasks using the portable Hub schema on PostgreSQL.
type Postgres struct {
	db *sql.DB
}

// OpenPostgres opens a postgres URL and ensures schema exists.
func OpenPostgres(url string) (*Postgres, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if _, err := db.Exec(postgresSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate postgres: %w", err)
	}
	return &Postgres{db: db}, nil
}

// Close releases the database handle.
func (p *Postgres) Close() error { return p.db.Close() }

func (p *Postgres) GetTask(ctx context.Context, taskID string) (*router.Task, error) {
	row := p.db.QueryRowContext(ctx, taskSelectSQL+` WHERE task_id = $1`, taskID)
	return scanTaskRow(row)
}

func (p *Postgres) InsertTask(ctx context.Context, task *router.Task) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO tasks (
		 task_id, batch_id, goal, callback_topic, status, attempt, max_attempts,
		 target_worker, toolsets_json, depends_on_json, aggregate_key, min_resources_json,
		 priority, queue_timeout_seconds, first_progress_seconds, timeout_seconds,
		 queue_deadline_at, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		task.TaskID, nullString(task.BatchID), task.Goal, task.CallbackTopic, task.Status,
		task.Attempt, task.MaxAttempts, nullString(task.TargetWorker), nullString(task.ToolsetsJSON),
		nullString(task.DependsOnJSON), nullString(task.AggregateKey), nullString(task.MinResourcesJSON),
		task.Priority, nullInt(task.QueueTimeoutSeconds), nullInt(task.FirstProgressSeconds),
		nullInt(task.TimeoutSeconds), nullTime(task.QueueDeadlineAt), float64(task.CreatedAt.Unix()),
	)
	return err
}

func (p *Postgres) UpdateTask(ctx context.Context, task *router.Task) error {
	res, err := p.db.ExecContext(ctx,
		`UPDATE tasks SET status = $1, summary = $2, error = $3, attempt = $4, worker_id = $5,
		 claim_token = $6, queue_deadline_at = $7, first_progress_deadline_at = $8,
		 claim_expires_at = $9, started_at = $10, completed_at = $11, cancel_reason = $12
		 WHERE task_id = $13`,
		task.Status, task.Summary, nullString(task.Error), task.Attempt, nullString(task.WorkerID),
		nullString(task.ClaimToken), nullTime(task.QueueDeadlineAt), nullTime(task.FirstProgressDeadlineAt),
		nullTime(task.ClaimExpiresAt), nullTime(task.StartedAt), nullTime(task.CompletedAt),
		nullString(task.CancelReason), task.TaskID,
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

func (p *Postgres) ListTasks(ctx context.Context, query router.ListTasksQuery) ([]*router.Task, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	clauses := make([]string, 0, 3)
	args := make([]any, 0, 5)
	argN := 1
	if query.BatchID != "" {
		clauses = append(clauses, fmt.Sprintf("batch_id = $%d", argN))
		args = append(args, query.BatchID)
		argN++
	}
	if query.CallbackTopic != "" {
		clauses = append(clauses, fmt.Sprintf("callback_topic = $%d", argN))
		args = append(args, query.CallbackTopic)
		argN++
	}
	if len(query.Statuses) > 0 {
		holders := make([]string, len(query.Statuses))
		for i, status := range query.Statuses {
			holders[i] = fmt.Sprintf("$%d", argN)
			args = append(args, status)
			argN++
		}
		clauses = append(clauses, fmt.Sprintf("status IN (%s)", strings.Join(holders, ",")))
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)
	sqlText := taskSelectSQL + where + fmt.Sprintf(" ORDER BY priority DESC, created_at ASC, task_id LIMIT $%d", argN)
	rows, err := p.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := make([]*router.Task, 0)
	for rows.Next() {
		task, err := scanTaskRow(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (p *Postgres) GetBatch(ctx context.Context, batchID string) (*router.Batch, error) {
	row := p.db.QueryRowContext(ctx,
		`SELECT batch_id, callback_topic, batch_spec_hash, policy_json, created_at, batch_deadline_at
		 FROM batches WHERE batch_id = $1`, batchID)
	batch, err := scanBatchRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return batch, err
}

func (p *Postgres) InsertBatch(ctx context.Context, batch *router.Batch) error {
	created := batch.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO batches (batch_id, callback_topic, batch_spec_hash, policy_json, created_at, batch_deadline_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		batch.BatchID, batch.CallbackTopic, batch.BatchSpecHash, nullString(batch.PolicyJSON),
		float64(created.Unix()), nullTime(batch.BatchDeadlineAt),
	)
	return err
}

func (p *Postgres) UpdateBatch(ctx context.Context, batch *router.Batch) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE batches SET callback_topic = $1, batch_spec_hash = $2, policy_json = $3, batch_deadline_at = $4
		 WHERE batch_id = $5`,
		batch.CallbackTopic, batch.BatchSpecHash, nullString(batch.PolicyJSON),
		nullTime(batch.BatchDeadlineAt), batch.BatchID,
	)
	return err
}

func (p *Postgres) ListExpiredBatches(ctx context.Context, now time.Time) ([]*router.Batch, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT batch_id, callback_topic, batch_spec_hash, policy_json, created_at, batch_deadline_at
		 FROM batches WHERE batch_deadline_at IS NOT NULL AND batch_deadline_at <= $1`,
		float64(now.Unix()),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBatchRows(rows)
}

func (p *Postgres) InsertCheckpoint(ctx context.Context, checkpoint *router.Checkpoint) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO checkpoints (checkpoint_id, task_id, summary, resume_blob, checkpoint_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		checkpoint.CheckpointID, checkpoint.TaskID, checkpoint.Summary,
		checkpoint.ResumeBlob, float64(checkpoint.CheckpointAt.Unix()),
	)
	return err
}

func (p *Postgres) GetLatestCheckpoint(ctx context.Context, taskID string) (*router.Checkpoint, error) {
	row := p.db.QueryRowContext(ctx,
		`SELECT checkpoint_id, task_id, summary, resume_blob, checkpoint_at
		 FROM checkpoints WHERE task_id = $1 ORDER BY checkpoint_at DESC LIMIT 1`, taskID)
	var summary sql.NullString
	var blob []byte
	var at float64
	checkpoint := &router.Checkpoint{}
	if err := row.Scan(&checkpoint.CheckpointID, &checkpoint.TaskID, &summary, &blob, &at); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	checkpoint.Summary = summary.String
	checkpoint.ResumeBlob = blob
	checkpoint.CheckpointAt = time.Unix(int64(at), 0)
	return checkpoint, nil
}
