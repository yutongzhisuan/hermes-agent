package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

func (s *SQLite) InsertCheckpoint(_ context.Context, checkpoint *router.Checkpoint) error {
	_, err := s.db.Exec(
		`INSERT INTO checkpoints (checkpoint_id, task_id, summary, resume_blob, checkpoint_at)
		 VALUES (?, ?, ?, ?, ?)`,
		checkpoint.CheckpointID,
		checkpoint.TaskID,
		checkpoint.Summary,
		checkpoint.ResumeBlob,
		float64(checkpoint.CheckpointAt.Unix()),
	)
	return err
}

func (s *SQLite) GetLatestCheckpoint(_ context.Context, taskID string) (*router.Checkpoint, error) {
	row := s.db.QueryRow(
		`SELECT checkpoint_id, task_id, summary, resume_blob, checkpoint_at
		 FROM checkpoints WHERE task_id = ? ORDER BY checkpoint_at DESC LIMIT 1`,
		taskID,
	)
	var summary sql.NullString
	var blob []byte
	var at float64
	checkpoint := &router.Checkpoint{}
	if err := row.Scan(
		&checkpoint.CheckpointID,
		&checkpoint.TaskID,
		&summary,
		&blob,
		&at,
	); err != nil {
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
