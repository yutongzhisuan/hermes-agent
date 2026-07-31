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

// OpenSQLite opens path and ensures the tasks schema exists.
func OpenSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(tasksSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	return &SQLite{db: db}, nil
}

// Close releases the database handle.
func (s *SQLite) Close() error {
	return s.db.Close()
}

func (s *SQLite) GetTask(_ context.Context, taskID string) (*router.Task, error) {
	row := s.db.QueryRow(
		`SELECT task_id, goal, callback_topic, status, attempt, summary, created_at, completed_at
		 FROM tasks WHERE task_id = ?`,
		taskID,
	)
	var summary sql.NullString
	var completedAt sql.NullFloat64
	var createdUnix float64
	task := &router.Task{}
	if err := row.Scan(
		&task.TaskID,
		&task.Goal,
		&task.CallbackTopic,
		&task.Status,
		&task.Attempt,
		&summary,
		&createdUnix,
		&completedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	task.Summary = summary.String
	task.CreatedAt = time.Unix(int64(createdUnix), 0)
	if completedAt.Valid {
		task.CompletedAt = time.Unix(int64(completedAt.Float64), 0)
	}
	return task, nil
}

func (s *SQLite) InsertTask(_ context.Context, task *router.Task) error {
	_, err := s.db.Exec(
		`INSERT INTO tasks (task_id, goal, callback_topic, status, attempt, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		task.TaskID,
		task.Goal,
		task.CallbackTopic,
		task.Status,
		task.Attempt,
		float64(task.CreatedAt.Unix()),
	)
	return err
}

func (s *SQLite) UpdateTask(_ context.Context, task *router.Task) error {
	var completed any
	if !task.CompletedAt.IsZero() {
		completed = float64(task.CompletedAt.Unix())
	}
	res, err := s.db.Exec(
		`UPDATE tasks SET status = ?, summary = ?, completed_at = ? WHERE task_id = ?`,
		task.Status,
		task.Summary,
		completed,
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
