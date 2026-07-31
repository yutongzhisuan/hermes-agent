package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

func (s *SQLite) ListTasks(_ context.Context, query router.ListTasksQuery) ([]*router.Task, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	clauses := make([]string, 0, 3)
	args := make([]any, 0, 5)
	if query.BatchID != "" {
		clauses = append(clauses, "batch_id = ?")
		args = append(args, query.BatchID)
	}
	if query.CallbackTopic != "" {
		clauses = append(clauses, "callback_topic = ?")
		args = append(args, query.CallbackTopic)
	}
	if query.MasterSessionID != "" {
		clauses = append(clauses, "master_session_id = ?")
		args = append(args, query.MasterSessionID)
	}
	if query.WorkerID != "" {
		clauses = append(clauses, "worker_id = ?")
		args = append(args, query.WorkerID)
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
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)
	rows, err := s.db.Query(
		taskSelectSQL+where+` ORDER BY priority DESC, created_at ASC, task_id LIMIT ?`,
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
