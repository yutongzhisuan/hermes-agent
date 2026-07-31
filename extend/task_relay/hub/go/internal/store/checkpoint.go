package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

const checkpointSelectSQL = `
SELECT checkpoint_id, task_id, event_id, summary, fields_json, resume_blob, checkpoint_at, lease_until
FROM checkpoints`

func scanCheckpointRow(scanner interface {
	Scan(dest ...any) error
}) (*router.Checkpoint, error) {
	var summary sql.NullString
	var fieldsJSON sql.NullString
	var blob []byte
	var at float64
	var leaseUntil sql.NullFloat64
	checkpoint := &router.Checkpoint{}
	if err := scanner.Scan(
		&checkpoint.CheckpointID,
		&checkpoint.TaskID,
		&checkpoint.EventID,
		&summary,
		&fieldsJSON,
		&blob,
		&at,
		&leaseUntil,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	checkpoint.Summary = summary.String
	checkpoint.FieldsJSON = fieldsJSON.String
	checkpoint.ResumeBlob = blob
	checkpoint.CheckpointAt = time.Unix(int64(at), 0)
	if leaseUntil.Valid {
		checkpoint.LeaseUntil = time.Unix(int64(leaseUntil.Float64), 0)
	}
	return checkpoint, nil
}

func (s *SQLite) InsertCheckpoint(_ context.Context, checkpoint *router.Checkpoint) error {
	_, err := s.db.Exec(
		`INSERT INTO checkpoints (
		 checkpoint_id, task_id, event_id, summary, fields_json, resume_blob, checkpoint_at, lease_until
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		checkpoint.CheckpointID,
		checkpoint.TaskID,
		checkpoint.EventID,
		checkpoint.Summary,
		nullString(checkpoint.FieldsJSON),
		checkpoint.ResumeBlob,
		float64(checkpoint.CheckpointAt.Unix()),
		nullTime(checkpoint.LeaseUntil),
	)
	return err
}

func (s *SQLite) GetLatestCheckpoint(_ context.Context, taskID string) (*router.Checkpoint, error) {
	row := s.db.QueryRow(
		checkpointSelectSQL+` WHERE task_id = ? ORDER BY checkpoint_at DESC LIMIT 1`,
		taskID,
	)
	return scanCheckpointRow(row)
}

func (m *Memory) InsertCheckpoint(_ context.Context, checkpoint *router.Checkpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.checkpoints == nil {
		m.checkpoints = make(map[string][]router.Checkpoint)
	}
	m.checkpoints[checkpoint.TaskID] = append(m.checkpoints[checkpoint.TaskID], *checkpoint)
	return nil
}

func (m *Memory) GetLatestCheckpoint(_ context.Context, taskID string) (*router.Checkpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := m.checkpoints[taskID]
	if len(items) == 0 {
		return nil, nil
	}
	latest := items[len(items)-1]
	return &latest, nil
}

func (p *Postgres) InsertCheckpoint(ctx context.Context, checkpoint *router.Checkpoint) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO checkpoints (
		 checkpoint_id, task_id, event_id, summary, fields_json, resume_blob, checkpoint_at, lease_until
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		checkpoint.CheckpointID, checkpoint.TaskID, checkpoint.EventID, checkpoint.Summary,
		nullString(checkpoint.FieldsJSON), checkpoint.ResumeBlob,
		float64(checkpoint.CheckpointAt.Unix()), nullTime(checkpoint.LeaseUntil),
	)
	return err
}

func (p *Postgres) GetLatestCheckpoint(ctx context.Context, taskID string) (*router.Checkpoint, error) {
	row := p.db.QueryRowContext(ctx,
		checkpointSelectSQL+` WHERE task_id = $1 ORDER BY checkpoint_at DESC LIMIT 1`, taskID)
	return scanCheckpointRow(row)
}
