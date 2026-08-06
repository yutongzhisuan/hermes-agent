package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/infa/task_relay/hub/internal/router"
)

const eventSelectSQL = `
SELECT event_id, callback_topic, task_id, batch_id, kind, payload_json, event_at
FROM task_events`

func scanEventRow(scanner interface {
	Scan(dest ...any) error
}) (*router.TaskEvent, error) {
	var taskID sql.NullString
	var batchID sql.NullString
	var payload sql.NullString
	var eventAt float64
	event := &router.TaskEvent{}
	if err := scanner.Scan(
		&event.EventID,
		&event.CallbackTopic,
		&taskID,
		&batchID,
		&event.Kind,
		&payload,
		&eventAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	event.TaskID = taskID.String
	event.BatchID = batchID.String
	event.PayloadJSON = payload.String
	event.EventAt = time.Unix(int64(eventAt), 0)
	return event, nil
}

func sqlPlaceholder(postgres bool, index int) string {
	if postgres {
		return fmt.Sprintf("$%d", index)
	}
	return "?"
}

func buildEventFilterSQL(filter router.EventFilter, postgres bool) (string, []any) {
	clauses := make([]string, 0, 4)
	args := make([]any, 0, 4)

	clauses = append(clauses, "event_id > "+sqlPlaceholder(postgres, len(args)+1))
	args = append(args, filter.AfterEventID)
	if filter.Topic != "" {
		clauses = append(clauses, "callback_topic = "+sqlPlaceholder(postgres, len(args)+1))
		args = append(args, filter.Topic)
	}
	if filter.BatchID != "" {
		clauses = append(clauses, "batch_id = "+sqlPlaceholder(postgres, len(args)+1))
		args = append(args, filter.BatchID)
	}
	if filter.TaskID != "" {
		clauses = append(clauses, "task_id = "+sqlPlaceholder(postgres, len(args)+1))
		args = append(args, filter.TaskID)
	}
	return strings.Join(clauses, " AND "), args
}

func buildEventMatchSQL(topic, batchID, taskID string, postgres bool) (string, []any) {
	clauses := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if topic != "" {
		clauses = append(clauses, "callback_topic = "+sqlPlaceholder(postgres, len(args)+1))
		args = append(args, topic)
	}
	if batchID != "" {
		clauses = append(clauses, "batch_id = "+sqlPlaceholder(postgres, len(args)+1))
		args = append(args, batchID)
	}
	if taskID != "" {
		clauses = append(clauses, "task_id = "+sqlPlaceholder(postgres, len(args)+1))
		args = append(args, taskID)
	}
	if len(clauses) == 0 {
		return "1=1", args
	}
	return strings.Join(clauses, " AND "), args
}

func (m *Memory) AppendEvent(_ context.Context, event *router.TaskEvent) (*router.TaskEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextEventID++
	copy := *event
	copy.EventID = m.nextEventID
	if copy.EventAt.IsZero() {
		copy.EventAt = time.Now()
	}
	m.events = append(m.events, copy)
	return &copy, nil
}

func (m *Memory) ListEventsForFilter(_ context.Context, filter router.EventFilter) ([]*router.TaskEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	limit := filter.Limit
	if limit <= 0 {
		limit = 1000
	}
	out := make([]*router.TaskEvent, 0)
	for _, event := range m.events {
		if event.EventID <= filter.AfterEventID {
			continue
		}
		if filter.Topic != "" && event.CallbackTopic != filter.Topic {
			continue
		}
		if filter.BatchID != "" && event.BatchID != filter.BatchID {
			continue
		}
		if filter.TaskID != "" && event.TaskID != filter.TaskID {
			continue
		}
		copy := event
		out = append(out, &copy)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *Memory) OldestEventIDForFilter(_ context.Context, topic, batchID, taskID string) (*int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var oldest *int64
	for _, event := range m.events {
		if topic != "" && event.CallbackTopic != topic {
			continue
		}
		if batchID != "" && event.BatchID != batchID {
			continue
		}
		if taskID != "" && event.TaskID != taskID {
			continue
		}
		id := event.EventID
		if oldest == nil || id < *oldest {
			oldest = &id
		}
	}
	return oldest, nil
}

func (m *Memory) OldestEventID(_ context.Context) (*int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events) == 0 {
		return nil, nil
	}
	min := m.events[0].EventID
	for _, event := range m.events[1:] {
		if event.EventID < min {
			min = event.EventID
		}
	}
	return &min, nil
}

func (m *Memory) NewestEventID(_ context.Context) (*int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events) == 0 {
		return nil, nil
	}
	id := m.events[len(m.events)-1].EventID
	return &id, nil
}

func (m *Memory) PruneEventsBefore(_ context.Context, cutoff time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := make([]router.TaskEvent, 0, len(m.events))
	var removed int64
	for _, event := range m.events {
		if event.EventAt.Before(cutoff) {
			removed++
			continue
		}
		kept = append(kept, event)
	}
	m.events = kept
	return removed, nil
}

func (s *SQLite) AppendEvent(_ context.Context, event *router.TaskEvent) (*router.TaskEvent, error) {
	at := event.EventAt
	if at.IsZero() {
		at = time.Now()
	}
	res, err := s.db.Exec(
		`INSERT INTO task_events (callback_topic, task_id, batch_id, kind, payload_json, event_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		event.CallbackTopic,
		nullString(event.TaskID),
		nullString(event.BatchID),
		event.Kind,
		nullString(event.PayloadJSON),
		float64(at.Unix()),
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	copy := *event
	copy.EventID = id
	copy.EventAt = at
	return &copy, nil
}

func (s *SQLite) ListEventsForFilter(_ context.Context, filter router.EventFilter) ([]*router.TaskEvent, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 1000
	}
	where, args := buildEventFilterSQL(filter, false)
	args = append(args, limit)
	rows, err := s.db.Query(
		eventSelectSQL+" WHERE "+where+" ORDER BY event_id LIMIT ?",
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEventRows(rows)
}

func (s *SQLite) OldestEventIDForFilter(_ context.Context, topic, batchID, taskID string) (*int64, error) {
	where, args := buildEventMatchSQL(topic, batchID, taskID, false)
	return s.queryOptionalEventID("SELECT MIN(event_id) FROM task_events WHERE "+where, args...)
}

func (s *SQLite) OldestEventID(_ context.Context) (*int64, error) {
	return s.queryOptionalEventID("SELECT MIN(event_id) FROM task_events")
}

func (s *SQLite) NewestEventID(_ context.Context) (*int64, error) {
	return s.queryOptionalEventID("SELECT MAX(event_id) FROM task_events")
}

func (s *SQLite) PruneEventsBefore(_ context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM task_events WHERE event_at < ?`, float64(cutoff.Unix()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *SQLite) queryOptionalEventID(query string, args ...any) (*int64, error) {
	row := s.db.QueryRow(query, args...)
	var id sql.NullInt64
	if err := row.Scan(&id); err != nil {
		return nil, err
	}
	if !id.Valid {
		return nil, nil
	}
	v := id.Int64
	return &v, nil
}

func scanEventRows(rows *sql.Rows) ([]*router.TaskEvent, error) {
	out := make([]*router.TaskEvent, 0)
	for rows.Next() {
		event, err := scanEventRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (p *Postgres) AppendEvent(ctx context.Context, event *router.TaskEvent) (*router.TaskEvent, error) {
	at := event.EventAt
	if at.IsZero() {
		at = time.Now()
	}
	row := p.db.QueryRowContext(ctx,
		`INSERT INTO task_events (callback_topic, task_id, batch_id, kind, payload_json, event_at)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING event_id`,
		event.CallbackTopic, nullString(event.TaskID), nullString(event.BatchID),
		event.Kind, nullString(event.PayloadJSON), float64(at.Unix()),
	)
	copy := *event
	copy.EventAt = at
	if err := row.Scan(&copy.EventID); err != nil {
		return nil, err
	}
	return &copy, nil
}

func (p *Postgres) ListEventsForFilter(ctx context.Context, filter router.EventFilter) ([]*router.TaskEvent, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 1000
	}
	where, args := buildEventFilterSQL(filter, true)
	argN := len(args) + 1
	args = append(args, limit)
	rows, err := p.db.QueryContext(ctx,
		eventSelectSQL+" WHERE "+where+fmt.Sprintf(" ORDER BY event_id LIMIT $%d", argN),
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEventRows(rows)
}

func (p *Postgres) OldestEventIDForFilter(ctx context.Context, topic, batchID, taskID string) (*int64, error) {
	where, args := buildEventMatchSQL(topic, batchID, taskID, true)
	return p.queryOptionalEventID(ctx, "SELECT MIN(event_id) FROM task_events WHERE "+where, args...)
}

func (p *Postgres) OldestEventID(ctx context.Context) (*int64, error) {
	return p.queryOptionalEventID(ctx, "SELECT MIN(event_id) FROM task_events")
}

func (p *Postgres) NewestEventID(ctx context.Context) (*int64, error) {
	return p.queryOptionalEventID(ctx, "SELECT MAX(event_id) FROM task_events")
}

func (p *Postgres) PruneEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := p.db.ExecContext(ctx,
		`DELETE FROM task_events WHERE event_at < $1`, float64(cutoff.Unix()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (p *Postgres) queryOptionalEventID(ctx context.Context, query string, args ...any) (*int64, error) {
	row := p.db.QueryRowContext(ctx, query, args...)
	var id sql.NullInt64
	if err := row.Scan(&id); err != nil {
		return nil, err
	}
	if !id.Valid {
		return nil, nil
	}
	v := id.Int64
	return &v, nil
}
