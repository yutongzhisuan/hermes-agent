package store

const tasksSchema = `
CREATE TABLE IF NOT EXISTS tasks (
    task_id TEXT PRIMARY KEY,
    batch_id TEXT,
    master_session_id TEXT,
    goal TEXT NOT NULL,
    params_json TEXT,
    context_json TEXT,
    callback_topic TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempt INTEGER DEFAULT 0,
    max_attempts INTEGER DEFAULT 1,
    worker_id TEXT,
    claim_token TEXT,
    target_worker TEXT,
    toolsets_json TEXT,
    depends_on_json TEXT,
    aggregate_key TEXT,
    min_resources_json TEXT,
    trace_context_json TEXT,
    allowed_worker_ids_json TEXT,
    deny_worker_ids_json TEXT,
    resume_from_checkpoint TEXT,
    error TEXT,
    priority INTEGER DEFAULT 0,
    queue_timeout_seconds INTEGER,
    first_progress_seconds INTEGER,
    timeout_seconds INTEGER,
    queue_deadline_at REAL,
    first_progress_deadline_at REAL,
    claim_expires_at REAL,
    started_at REAL,
    summary TEXT,
    cancel_reason TEXT,
    created_at REAL NOT NULL,
    completed_at REAL
);

CREATE TABLE IF NOT EXISTS audit_log (
    audit_id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_at REAL NOT NULL,
    action TEXT NOT NULL,
    task_id TEXT,
    master_session_id TEXT,
    payload_json TEXT
);

CREATE INDEX IF NOT EXISTS idx_audit_task ON audit_log(task_id, event_at DESC);

CREATE TABLE IF NOT EXISTS batches (
    batch_id TEXT PRIMARY KEY,
    callback_topic TEXT NOT NULL,
    batch_spec_hash TEXT NOT NULL,
    policy_json TEXT,
    created_at REAL NOT NULL,
    batch_deadline_at REAL
);

CREATE TABLE IF NOT EXISTS checkpoints (
    checkpoint_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    summary TEXT,
    resume_blob BLOB,
    checkpoint_at REAL NOT NULL,
    PRIMARY KEY (task_id, checkpoint_id)
);
`

var sqliteMigrations = []string{
	`ALTER TABLE tasks ADD COLUMN depends_on_json TEXT`,
	`ALTER TABLE tasks ADD COLUMN aggregate_key TEXT`,
	`ALTER TABLE tasks ADD COLUMN min_resources_json TEXT`,
	`ALTER TABLE tasks ADD COLUMN error TEXT`,
	`ALTER TABLE batches ADD COLUMN policy_json TEXT`,
	`ALTER TABLE batches ADD COLUMN batch_deadline_at REAL`,
	`ALTER TABLE tasks ADD COLUMN master_session_id TEXT`,
	`ALTER TABLE tasks ADD COLUMN params_json TEXT`,
	`ALTER TABLE tasks ADD COLUMN context_json TEXT`,
	`ALTER TABLE tasks ADD COLUMN trace_context_json TEXT`,
	`ALTER TABLE tasks ADD COLUMN allowed_worker_ids_json TEXT`,
	`ALTER TABLE tasks ADD COLUMN deny_worker_ids_json TEXT`,
	`ALTER TABLE tasks ADD COLUMN resume_from_checkpoint TEXT`,
	`CREATE TABLE IF NOT EXISTS audit_log (
		audit_id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_at REAL NOT NULL,
		action TEXT NOT NULL,
		task_id TEXT,
		master_session_id TEXT,
		payload_json TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_task ON audit_log(task_id, event_at DESC)`,
}

const postgresSchema = `
CREATE TABLE IF NOT EXISTS tasks (
    task_id TEXT PRIMARY KEY,
    batch_id TEXT,
    master_session_id TEXT,
    goal TEXT NOT NULL,
    params_json TEXT,
    context_json TEXT,
    callback_topic TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempt INTEGER DEFAULT 0,
    max_attempts INTEGER DEFAULT 1,
    worker_id TEXT,
    claim_token TEXT,
    target_worker TEXT,
    toolsets_json TEXT,
    depends_on_json TEXT,
    aggregate_key TEXT,
    min_resources_json TEXT,
    trace_context_json TEXT,
    allowed_worker_ids_json TEXT,
    deny_worker_ids_json TEXT,
    resume_from_checkpoint TEXT,
    error TEXT,
    priority INTEGER DEFAULT 0,
    queue_timeout_seconds INTEGER,
    first_progress_seconds INTEGER,
    timeout_seconds INTEGER,
    queue_deadline_at DOUBLE PRECISION,
    first_progress_deadline_at DOUBLE PRECISION,
    claim_expires_at DOUBLE PRECISION,
    started_at DOUBLE PRECISION,
    summary TEXT,
    cancel_reason TEXT,
    created_at DOUBLE PRECISION NOT NULL,
    completed_at DOUBLE PRECISION
);

CREATE TABLE IF NOT EXISTS audit_log (
    audit_id BIGSERIAL PRIMARY KEY,
    event_at DOUBLE PRECISION NOT NULL,
    action TEXT NOT NULL,
    task_id TEXT,
    master_session_id TEXT,
    payload_json TEXT
);

CREATE INDEX IF NOT EXISTS idx_audit_task ON audit_log(task_id, event_at DESC);

CREATE TABLE IF NOT EXISTS batches (
    batch_id TEXT PRIMARY KEY,
    callback_topic TEXT NOT NULL,
    batch_spec_hash TEXT NOT NULL,
    policy_json TEXT,
    created_at DOUBLE PRECISION NOT NULL,
    batch_deadline_at DOUBLE PRECISION
);

CREATE TABLE IF NOT EXISTS checkpoints (
    checkpoint_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    summary TEXT,
    resume_blob BYTEA,
    checkpoint_at DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (task_id, checkpoint_id)
);
`
