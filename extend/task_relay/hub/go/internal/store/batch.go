package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/infa/task_relay/hub/internal/router"
)

// Open opens SQLite or Postgres based on the db path/URL.
func Open(pathOrURL string) (router.Store, func() error, error) {
	if isPostgresURL(pathOrURL) {
		pg, err := OpenPostgres(pathOrURL)
		if err != nil {
			return nil, nil, err
		}
		return pg, pg.Close, nil
	}
	sqlite, err := OpenSQLite(pathOrURL)
	if err != nil {
		return nil, nil, err
	}
	return sqlite, sqlite.Close, nil
}

func isPostgresURL(value string) bool {
	return strings.HasPrefix(value, "postgres://") || strings.HasPrefix(value, "postgresql://")
}

func (s *SQLite) UpdateBatch(_ context.Context, batch *router.Batch) error {
	_, err := s.db.Exec(
		`UPDATE batches SET callback_topic = ?, batch_spec_hash = ?, policy_json = ?, batch_deadline_at = ?
		 WHERE batch_id = ?`,
		batch.CallbackTopic,
		batch.BatchSpecHash,
		nullString(batch.PolicyJSON),
		nullTime(batch.BatchDeadlineAt),
		batch.BatchID,
	)
	return err
}

func (s *SQLite) ListExpiredBatches(_ context.Context, now time.Time) ([]*router.Batch, error) {
	rows, err := s.db.Query(
		`SELECT batch_id, callback_topic, batch_spec_hash, policy_json, created_at, batch_deadline_at
		 FROM batches WHERE batch_deadline_at IS NOT NULL AND batch_deadline_at <= ?`,
		float64(now.Unix()),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBatchRows(rows)
}

func (m *Memory) UpdateBatch(_ context.Context, batch *router.Batch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.batches[batch.BatchID]; !ok {
		return fmt.Errorf("batch %s not found", batch.BatchID)
	}
	copy := *batch
	m.batches[batch.BatchID] = &copy
	return nil
}

func (m *Memory) ListExpiredBatches(_ context.Context, now time.Time) ([]*router.Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*router.Batch, 0)
	for _, batch := range m.batches {
		if batch.BatchDeadlineAt.IsZero() || batch.BatchDeadlineAt.After(now) {
			continue
		}
		copy := *batch
		out = append(out, &copy)
	}
	return out, nil
}

func scanBatchRow(scanner interface{ Scan(dest ...any) error }) (*router.Batch, error) {
	var policy sqlNullString
	var created sqlNullFloat64
	var deadline sqlNullFloat64
	batch := &router.Batch{}
	if err := scanner.Scan(
		&batch.BatchID, &batch.CallbackTopic, &batch.BatchSpecHash, &policy, &created, &deadline,
	); err != nil {
		return nil, err
	}
	batch.PolicyJSON = policy.String
	if created.Valid {
		batch.CreatedAt = time.Unix(int64(created.Float64), 0)
	}
	if deadline.Valid {
		batch.BatchDeadlineAt = time.Unix(int64(deadline.Float64), 0)
	}
	return batch, nil
}

type sqlNullString struct {
	String string
	Valid  bool
}

func (n *sqlNullString) Scan(value any) error {
	if value == nil {
		n.String, n.Valid = "", false
		return nil
	}
	switch v := value.(type) {
	case string:
		n.String, n.Valid = v, true
	case []byte:
		n.String, n.Valid = string(v), true
	default:
		return fmt.Errorf("unsupported string type %T", value)
	}
	return nil
}

type sqlNullFloat64 struct {
	Float64 float64
	Valid   bool
}

func (n *sqlNullFloat64) Scan(value any) error {
	if value == nil {
		n.Valid = false
		return nil
	}
	switch v := value.(type) {
	case float64:
		n.Float64, n.Valid = v, true
	case int64:
		n.Float64, n.Valid = float64(v), true
	default:
		return fmt.Errorf("unsupported float type %T", value)
	}
	return nil
}

func scanBatchRows(rows interface {
	Next() bool
	Scan(dest ...any) error
}) ([]*router.Batch, error) {
	out := make([]*router.Batch, 0)
	for rows.Next() {
		batch, err := scanBatchRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, batch)
	}
	return out, nil
}
