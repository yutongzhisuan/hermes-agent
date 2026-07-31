package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

func (s *SQLite) ListTasks(_ context.Context, query router.ListTasksQuery) ([]*router.Task, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 4)
	if query.CallbackTopic != "" {
		clauses = append(clauses, "callback_topic = ?")
		args = append(args, query.CallbackTopic)
	}
	if len(query.Statuses) > 0 {
		holders := make([]string, len(query.Statuses))
		for i, status := range query.Statuses {
			holders[i] = "?"
			args = append(args, status)
		}
		clauses = append(clauses, fmt.Sprintf("status IN (%s)", strings.Join(holders, ",")))
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)
	rows, err := s.db.Query(
		`SELECT task_id, goal, callback_topic, status, attempt, worker_id, claim_token, summary, created_at, completed_at
		 FROM tasks `+where+` ORDER BY created_at ASC, task_id LIMIT ?`,
		args...,
	)
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

func scanTaskRow(scanner interface {
	Scan(dest ...any) error
}) (*router.Task, error) {
	var summary sql.NullString
	var workerID sql.NullString
	var claimToken sql.NullString
	var completedAt sql.NullFloat64
	var createdUnix float64
	task := &router.Task{}
	if err := scanner.Scan(
		&task.TaskID,
		&task.Goal,
		&task.CallbackTopic,
		&task.Status,
		&task.Attempt,
		&workerID,
		&claimToken,
		&summary,
		&createdUnix,
		&completedAt,
	); err != nil {
		return nil, err
	}
	task.Summary = summary.String
	task.WorkerID = workerID.String
	task.ClaimToken = claimToken.String
	task.CreatedAt = time.Unix(int64(createdUnix), 0)
	if completedAt.Valid {
		task.CompletedAt = time.Unix(int64(completedAt.Float64), 0)
	}
	return task, nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
