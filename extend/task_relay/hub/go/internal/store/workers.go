package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/infa/task_relay/hub/internal/router"
)

const workerSelectSQL = `
SELECT worker_id, wake_url, session_modes, capabilities_json, resources_json, load_json,
       max_concurrent, credit_available, running_tasks, last_announce_at, last_heartbeat_at,
       last_seen_at, status, online_session_id, drain_requested
FROM workers`

func scanWorkerRow(scanner interface {
	Scan(dest ...any) error
}) (*router.Worker, error) {
	var wakeURL sql.NullString
	var sessionModes sql.NullString
	var capabilities sql.NullString
	var resources sql.NullString
	var load sql.NullString
	var lastAnnounce sql.NullFloat64
	var lastHeartbeat sql.NullFloat64
	var lastSeen sql.NullFloat64
	var status sql.NullString
	var onlineSession sql.NullString
	var drainRequested sql.NullInt64

	worker := &router.Worker{}
	if err := scanner.Scan(
		&worker.WorkerID,
		&wakeURL,
		&sessionModes,
		&capabilities,
		&resources,
		&load,
		&worker.MaxConcurrent,
		&worker.CreditAvailable,
		&worker.RunningTasks,
		&lastAnnounce,
		&lastHeartbeat,
		&lastSeen,
		&status,
		&onlineSession,
		&drainRequested,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	worker.WakeURL = wakeURL.String
	if sessionModes.Valid {
		worker.SessionModes = sessionModes.String
	} else {
		worker.SessionModes = "A"
	}
	worker.CapabilitiesJSON = capabilities.String
	worker.ResourcesJSON = resources.String
	worker.LoadJSON = load.String
	if lastAnnounce.Valid {
		worker.LastAnnounceAt = time.Unix(int64(lastAnnounce.Float64), 0)
	}
	if lastHeartbeat.Valid {
		worker.LastHeartbeatAt = time.Unix(int64(lastHeartbeat.Float64), 0)
	}
	if lastSeen.Valid {
		worker.LastSeenAt = time.Unix(int64(lastSeen.Float64), 0)
	}
	if status.Valid {
		worker.Status = status.String
	} else {
		worker.Status = "offline"
	}
	worker.OnlineSessionID = onlineSession.String
	worker.DrainRequested = drainRequested.Int64 != 0
	return worker, nil
}

func workerInsertValues(worker *router.Worker) []any {
	sessionModes := worker.SessionModes
	if sessionModes == "" {
		sessionModes = "A"
	}
	status := worker.Status
	if status == "" {
		status = "offline"
	}
	return []any{
		worker.WorkerID,
		nullString(worker.WakeURL),
		sessionModes,
		nullString(worker.CapabilitiesJSON),
		nullString(worker.ResourcesJSON),
		nullString(worker.LoadJSON),
		worker.MaxConcurrent,
		worker.CreditAvailable,
		worker.RunningTasks,
		nullTime(worker.LastAnnounceAt),
		nullTime(worker.LastHeartbeatAt),
		nullTime(worker.LastSeenAt),
		status,
		nullString(worker.OnlineSessionID),
		boolInt(worker.DrainRequested),
	}
}

func (m *Memory) UpsertWorker(_ context.Context, worker *router.Worker) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.workers == nil {
		m.workers = make(map[string]*router.Worker)
	}
	copy := *worker
	m.workers[worker.WorkerID] = &copy
	return nil
}

func (m *Memory) GetWorker(_ context.Context, workerID string) (*router.Worker, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	worker, ok := m.workers[workerID]
	if !ok {
		return nil, nil
	}
	copy := *worker
	return &copy, nil
}

func (m *Memory) ListWorkers(_ context.Context, onlySchedulable bool) ([]*router.Worker, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*router.Worker, 0, len(m.workers))
	for _, worker := range m.workers {
		if onlySchedulable && isWorkerUnschedulable(worker.Status) {
			continue
		}
		copy := *worker
		out = append(out, &copy)
	}
	return out, nil
}

func (m *Memory) DeleteWorker(_ context.Context, workerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.workers, workerID)
	return nil
}

func isWorkerUnschedulable(status string) bool {
	switch status {
	case "offline", "stale", "draining":
		return true
	default:
		return false
	}
}

func (s *SQLite) UpsertWorker(_ context.Context, worker *router.Worker) error {
	values := workerInsertValues(worker)
	_, err := s.db.Exec(
		`INSERT INTO workers (
		 worker_id, wake_url, session_modes, capabilities_json, resources_json, load_json,
		 max_concurrent, credit_available, running_tasks, last_announce_at, last_heartbeat_at,
		 last_seen_at, status, online_session_id, drain_requested
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(worker_id) DO UPDATE SET
		 wake_url = excluded.wake_url,
		 session_modes = excluded.session_modes,
		 capabilities_json = excluded.capabilities_json,
		 resources_json = excluded.resources_json,
		 load_json = excluded.load_json,
		 max_concurrent = excluded.max_concurrent,
		 credit_available = excluded.credit_available,
		 running_tasks = excluded.running_tasks,
		 last_announce_at = excluded.last_announce_at,
		 last_heartbeat_at = excluded.last_heartbeat_at,
		 last_seen_at = excluded.last_seen_at,
		 status = excluded.status,
		 online_session_id = excluded.online_session_id,
		 drain_requested = excluded.drain_requested`,
		values...,
	)
	return err
}

func (s *SQLite) GetWorker(_ context.Context, workerID string) (*router.Worker, error) {
	row := s.db.QueryRow(workerSelectSQL+` WHERE worker_id = ?`, workerID)
	return scanWorkerRow(row)
}

func (s *SQLite) ListWorkers(_ context.Context, onlySchedulable bool) ([]*router.Worker, error) {
	query := workerSelectSQL
	if onlySchedulable {
		query += ` WHERE status NOT IN ('offline', 'stale', 'draining')`
	}
	query += ` ORDER BY worker_id`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWorkerRows(rows)
}

func (s *SQLite) DeleteWorker(_ context.Context, workerID string) error {
	_, err := s.db.Exec(`DELETE FROM workers WHERE worker_id = ?`, workerID)
	return err
}

func scanWorkerRows(rows *sql.Rows) ([]*router.Worker, error) {
	out := make([]*router.Worker, 0)
	for rows.Next() {
		worker, err := scanWorkerRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, worker)
	}
	return out, rows.Err()
}

func (p *Postgres) UpsertWorker(ctx context.Context, worker *router.Worker) error {
	values := workerInsertValues(worker)
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO workers (
		 worker_id, wake_url, session_modes, capabilities_json, resources_json, load_json,
		 max_concurrent, credit_available, running_tasks, last_announce_at, last_heartbeat_at,
		 last_seen_at, status, online_session_id, drain_requested
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT(worker_id) DO UPDATE SET
		 wake_url = EXCLUDED.wake_url,
		 session_modes = EXCLUDED.session_modes,
		 capabilities_json = EXCLUDED.capabilities_json,
		 resources_json = EXCLUDED.resources_json,
		 load_json = EXCLUDED.load_json,
		 max_concurrent = EXCLUDED.max_concurrent,
		 credit_available = EXCLUDED.credit_available,
		 running_tasks = EXCLUDED.running_tasks,
		 last_announce_at = EXCLUDED.last_announce_at,
		 last_heartbeat_at = EXCLUDED.last_heartbeat_at,
		 last_seen_at = EXCLUDED.last_seen_at,
		 status = EXCLUDED.status,
		 online_session_id = EXCLUDED.online_session_id,
		 drain_requested = EXCLUDED.drain_requested`,
		values...,
	)
	return err
}

func (p *Postgres) GetWorker(ctx context.Context, workerID string) (*router.Worker, error) {
	row := p.db.QueryRowContext(ctx, workerSelectSQL+` WHERE worker_id = $1`, workerID)
	return scanWorkerRow(row)
}

func (p *Postgres) ListWorkers(ctx context.Context, onlySchedulable bool) ([]*router.Worker, error) {
	query := workerSelectSQL
	if onlySchedulable {
		query += ` WHERE status NOT IN ('offline', 'stale', 'draining')`
	}
	query += ` ORDER BY worker_id`
	rows, err := p.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWorkerRows(rows)
}

func (p *Postgres) DeleteWorker(ctx context.Context, workerID string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM workers WHERE worker_id = $1`, workerID)
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
