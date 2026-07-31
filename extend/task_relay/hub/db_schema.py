"""Portable Hub store schema definitions for SQLite and Postgres."""

SCHEMA_SQLITE = """
CREATE TABLE IF NOT EXISTS tasks (
    task_id TEXT PRIMARY KEY,
    batch_id TEXT,
    master_session_id TEXT,
    goal TEXT NOT NULL,
    params_json TEXT,
    context_json TEXT,
    toolsets_json TEXT,
    target_worker TEXT,
    worker_id TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    result_json TEXT,
    summary TEXT,
    cancel_reason TEXT,
    fields_json TEXT,
    usage_json TEXT,
    error TEXT,
    callback_topic TEXT NOT NULL,
    allow_redispatch INTEGER DEFAULT 0,
    claim_token TEXT,
    claim_expires_at REAL,
    first_progress_deadline_at REAL,
    queue_deadline_at REAL,
    attempt INTEGER DEFAULT 0,
    max_attempts INTEGER DEFAULT 1,
    priority INTEGER DEFAULT 0,
    depends_on_json TEXT,
    aggregate_key TEXT,
    min_resources_json TEXT,
    trace_context_json TEXT,
    allowed_worker_ids_json TEXT,
    deny_worker_ids_json TEXT,
    resume_from_checkpoint TEXT,
    timeout_seconds INTEGER,
    queue_timeout_seconds INTEGER,
    first_progress_seconds INTEGER,
    created_at REAL NOT NULL,
    started_at REAL,
    completed_at REAL
);

CREATE INDEX IF NOT EXISTS idx_tasks_pending ON tasks(status, priority DESC, created_at);
CREATE INDEX IF NOT EXISTS idx_tasks_batch ON tasks(batch_id, status);
CREATE INDEX IF NOT EXISTS idx_tasks_aggregate ON tasks(batch_id, aggregate_key, status);

CREATE TABLE IF NOT EXISTS batches (
    batch_id TEXT PRIMARY KEY,
    master_session_id TEXT,
    callback_topic TEXT NOT NULL,
    batch_spec_hash TEXT NOT NULL,
    policy_json TEXT,
    created_at REAL NOT NULL,
    batch_deadline_at REAL
);

CREATE TABLE IF NOT EXISTS workers (
    worker_id TEXT PRIMARY KEY,
    wake_url TEXT,
    session_modes TEXT NOT NULL DEFAULT 'A',
    capabilities_json TEXT,
    resources_json TEXT,
    load_json TEXT,
    max_concurrent INTEGER DEFAULT 1,
    credit_available INTEGER DEFAULT 0,
    running_tasks INTEGER DEFAULT 0,
    last_announce_at REAL,
    last_heartbeat_at REAL,
    last_seen_at REAL,
    status TEXT DEFAULT 'offline',
    online_session_id TEXT,
    drain_requested INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS task_events (
    event_id INTEGER PRIMARY KEY AUTOINCREMENT,
    callback_topic TEXT NOT NULL,
    task_id TEXT,
    batch_id TEXT,
    kind TEXT NOT NULL,
    payload_json TEXT,
    event_at REAL NOT NULL,
    CHECK (kind = 'AGGREGATE' OR task_id IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_events_topic ON task_events(callback_topic, event_id);

CREATE TABLE IF NOT EXISTS checkpoints (
    checkpoint_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    event_id INTEGER NOT NULL,
    checkpoint_at REAL NOT NULL,
    summary TEXT,
    fields_json TEXT,
    resume_blob BLOB,
    lease_until REAL,
    PRIMARY KEY (task_id, checkpoint_id)
);

CREATE INDEX IF NOT EXISTS idx_checkpoints_task ON checkpoints(task_id, checkpoint_at DESC);
"""

SCHEMA_POSTGRES = """
CREATE TABLE IF NOT EXISTS tasks (
    task_id TEXT PRIMARY KEY,
    batch_id TEXT,
    master_session_id TEXT,
    goal TEXT NOT NULL,
    params_json TEXT,
    context_json TEXT,
    toolsets_json TEXT,
    target_worker TEXT,
    worker_id TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    result_json TEXT,
    summary TEXT,
    cancel_reason TEXT,
    fields_json TEXT,
    usage_json TEXT,
    error TEXT,
    callback_topic TEXT NOT NULL,
    allow_redispatch INTEGER DEFAULT 0,
    claim_token TEXT,
    claim_expires_at DOUBLE PRECISION,
    first_progress_deadline_at DOUBLE PRECISION,
    queue_deadline_at DOUBLE PRECISION,
    attempt INTEGER DEFAULT 0,
    max_attempts INTEGER DEFAULT 1,
    priority INTEGER DEFAULT 0,
    depends_on_json TEXT,
    aggregate_key TEXT,
    min_resources_json TEXT,
    trace_context_json TEXT,
    allowed_worker_ids_json TEXT,
    deny_worker_ids_json TEXT,
    resume_from_checkpoint TEXT,
    timeout_seconds INTEGER,
    queue_timeout_seconds INTEGER,
    first_progress_seconds INTEGER,
    created_at DOUBLE PRECISION NOT NULL,
    started_at DOUBLE PRECISION,
    completed_at DOUBLE PRECISION
);

CREATE INDEX IF NOT EXISTS idx_tasks_pending ON tasks(status, priority DESC, created_at);
CREATE INDEX IF NOT EXISTS idx_tasks_batch ON tasks(batch_id, status);
CREATE INDEX IF NOT EXISTS idx_tasks_aggregate ON tasks(batch_id, aggregate_key, status);

CREATE TABLE IF NOT EXISTS batches (
    batch_id TEXT PRIMARY KEY,
    master_session_id TEXT,
    callback_topic TEXT NOT NULL,
    batch_spec_hash TEXT NOT NULL,
    policy_json TEXT,
    created_at DOUBLE PRECISION NOT NULL,
    batch_deadline_at DOUBLE PRECISION
);

CREATE TABLE IF NOT EXISTS workers (
    worker_id TEXT PRIMARY KEY,
    wake_url TEXT,
    session_modes TEXT NOT NULL DEFAULT 'A',
    capabilities_json TEXT,
    resources_json TEXT,
    load_json TEXT,
    max_concurrent INTEGER DEFAULT 1,
    credit_available INTEGER DEFAULT 0,
    running_tasks INTEGER DEFAULT 0,
    last_announce_at DOUBLE PRECISION,
    last_heartbeat_at DOUBLE PRECISION,
    last_seen_at DOUBLE PRECISION,
    status TEXT DEFAULT 'offline',
    online_session_id TEXT,
    drain_requested INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS task_events (
    event_id BIGSERIAL PRIMARY KEY,
    callback_topic TEXT NOT NULL,
    task_id TEXT,
    batch_id TEXT,
    kind TEXT NOT NULL,
    payload_json TEXT,
    event_at DOUBLE PRECISION NOT NULL,
    CHECK (kind = 'AGGREGATE' OR task_id IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_events_topic ON task_events(callback_topic, event_id);

CREATE TABLE IF NOT EXISTS checkpoints (
    checkpoint_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    event_id BIGINT NOT NULL,
    checkpoint_at DOUBLE PRECISION NOT NULL,
    summary TEXT,
    fields_json TEXT,
    resume_blob BYTEA,
    lease_until DOUBLE PRECISION,
    PRIMARY KEY (task_id, checkpoint_id)
);

CREATE INDEX IF NOT EXISTS idx_checkpoints_task ON checkpoints(task_id, checkpoint_at DESC);
"""

POSTGRES_MIGRATIONS = (
    "ALTER TABLE tasks ADD COLUMN IF NOT EXISTS cancel_reason TEXT",
    "ALTER TABLE workers ADD COLUMN IF NOT EXISTS last_seen_at DOUBLE PRECISION",
    "ALTER TABLE workers ADD COLUMN IF NOT EXISTS drain_requested INTEGER DEFAULT 0",
    "ALTER TABLE tasks ADD COLUMN IF NOT EXISTS target_worker TEXT",
)
