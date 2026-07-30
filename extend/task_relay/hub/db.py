"""SQLite store for the Task Relay Hub.

Schema is verbatim from the design spec §Persistence (portable types, so the
same contract can be ported to Postgres behind this module later). The
`task_events.event_id` AUTOINCREMENT column is the single globally-monotonic
event sequence the WatchTask cursor semantics rely on.
"""

import json
import time
from dataclasses import asdict, fields

import aiosqlite

from extend.task_relay.hub.models import Batch, Checkpoint, Task, TaskEvent, Worker

_SCHEMA = """
CREATE TABLE IF NOT EXISTS tasks (
    task_id TEXT PRIMARY KEY,
    batch_id TEXT,
    master_session_id TEXT,
    goal TEXT NOT NULL,
    params_json TEXT,
    context_json TEXT,
    toolsets_json TEXT,
    worker_id TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    result_json TEXT,
    summary TEXT,
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
    status TEXT DEFAULT 'offline',
    online_session_id TEXT
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


def _insert_sql(table: str, model: type) -> str:
    cols = [f.name for f in fields(model)]
    placeholders = ", ".join(f":{c}" for c in cols)
    return f"INSERT INTO {table} ({', '.join(cols)}) VALUES ({placeholders})"


class Database:
    """Async wrapper around an aiosqlite connection with the hub schema."""

    def __init__(self, conn: aiosqlite.Connection):
        self._conn = conn

    async def close(self) -> None:
        await self._conn.close()

    # -- tasks -----------------------------------------------------------

    async def insert_task(self, task: Task) -> None:
        await self._conn.execute(_insert_sql("tasks", Task), asdict(task))
        await self._conn.commit()

    async def get_task(self, task_id: str) -> Task | None:
        cursor = await self._conn.execute(
            "SELECT * FROM tasks WHERE task_id = ?", (task_id,)
        )
        row = await cursor.fetchone()
        return Task(**dict(row)) if row is not None else None

    async def list_tasks_by_batch(self, batch_id: str) -> list[Task]:
        cursor = await self._conn.execute(
            "SELECT * FROM tasks WHERE batch_id = ? ORDER BY created_at, task_id",
            (batch_id,),
        )
        return [Task(**dict(row)) for row in await cursor.fetchall()]

    async def update_task_status(self, task_id: str, status: str) -> None:
        await self._conn.execute(
            "UPDATE tasks SET status = ? WHERE task_id = ?", (status, task_id)
        )
        await self._conn.commit()

    # -- events ----------------------------------------------------------

    async def append_event(
        self,
        *,
        callback_topic: str,
        task_id: str | None,
        kind: str,
        payload: dict | None = None,
        batch_id: str | None = None,
        event_at: float | None = None,
    ) -> TaskEvent:
        """Append one event to the global log; returns it with its event_id."""
        payload_json = json.dumps(payload) if payload is not None else None
        at = event_at if event_at is not None else time.time()
        cursor = await self._conn.execute(
            "INSERT INTO task_events"
            " (callback_topic, task_id, batch_id, kind, payload_json, event_at)"
            " VALUES (?, ?, ?, ?, ?, ?)",
            (callback_topic, task_id, batch_id, kind, payload_json, at),
        )
        await self._conn.commit()
        return TaskEvent(
            event_id=cursor.lastrowid,
            callback_topic=callback_topic,
            task_id=task_id,
            batch_id=batch_id,
            kind=kind,
            payload_json=payload_json,
            event_at=at,
        )

    async def list_events_after(
        self, callback_topic: str, after_event_id: int = 0, limit: int = 1000
    ) -> list[TaskEvent]:
        """Replay events on a topic with event_id > after_event_id, oldest first."""
        cursor = await self._conn.execute(
            "SELECT * FROM task_events"
            " WHERE callback_topic = ? AND event_id > ?"
            " ORDER BY event_id LIMIT ?",
            (callback_topic, after_event_id, limit),
        )
        return [TaskEvent(**dict(row)) for row in await cursor.fetchall()]

    async def list_events_for_filter(
        self,
        *,
        topic: str | None = None,
        batch_id: str | None = None,
        task_id: str | None = None,
        after_event_id: int = 0,
        limit: int = 1000,
    ) -> list[TaskEvent]:
        """Replay events matching a WatchTask filter, oldest first."""
        clauses = ["event_id > ?"]
        params: list = [after_event_id]
        if topic is not None:
            clauses.append("callback_topic = ?")
            params.append(topic)
        if batch_id is not None:
            clauses.append("batch_id = ?")
            params.append(batch_id)
        if task_id is not None:
            clauses.append("task_id = ?")
            params.append(task_id)
        cursor = await self._conn.execute(
            f"SELECT * FROM task_events WHERE {' AND '.join(clauses)}"
            " ORDER BY event_id LIMIT ?",
            (*params, limit),
        )
        return [TaskEvent(**dict(row)) for row in await cursor.fetchall()]

    async def newest_event_id(self) -> int | None:
        """Global tail of the event log; None when no events exist."""
        cursor = await self._conn.execute("SELECT MAX(event_id) FROM task_events")
        row = await cursor.fetchone()
        return row[0] if row is not None else None

    async def oldest_event_id(self) -> int | None:
        """Global head of the event log; None when no events exist."""
        cursor = await self._conn.execute("SELECT MIN(event_id) FROM task_events")
        row = await cursor.fetchone()
        return row[0] if row is not None else None

    async def oldest_event_id_for_filter(
        self,
        *,
        topic: str | None = None,
        batch_id: str | None = None,
        task_id: str | None = None,
    ) -> int | None:
        """Oldest retained event_id for a WatchTask filter (CursorOutOfRange).

        Returns None when no retained event matches the filter.
        """
        clauses = []
        params = []
        if topic is not None:
            clauses.append("callback_topic = ?")
            params.append(topic)
        if batch_id is not None:
            clauses.append("batch_id = ?")
            params.append(batch_id)
        if task_id is not None:
            clauses.append("task_id = ?")
            params.append(task_id)
        where = f" WHERE {' AND '.join(clauses)}" if clauses else ""
        cursor = await self._conn.execute(
            f"SELECT MIN(event_id) FROM task_events{where}", params
        )
        row = await cursor.fetchone()
        return row[0] if row is not None else None

    # -- checkpoints ------------------------------------------------------

    async def insert_checkpoint(self, checkpoint: Checkpoint) -> None:
        await self._conn.execute(_insert_sql("checkpoints", Checkpoint), asdict(checkpoint))
        await self._conn.commit()

    async def get_latest_checkpoint(self, task_id: str) -> Checkpoint | None:
        cursor = await self._conn.execute(
            "SELECT * FROM checkpoints WHERE task_id = ? ORDER BY checkpoint_at DESC LIMIT 1",
            (task_id,),
        )
        row = await cursor.fetchone()
        return Checkpoint(**dict(row)) if row is not None else None

    # -- workers ---------------------------------------------------------

    async def upsert_worker(self, worker: Worker) -> None:
        cols = [f.name for f in fields(Worker)]
        updates = ", ".join(f"{c} = excluded.{c}" for c in cols if c != "worker_id")
        sql = (
            f"INSERT INTO workers ({', '.join(cols)})"
            f" VALUES ({', '.join(f':{c}' for c in cols)})"
            f" ON CONFLICT(worker_id) DO UPDATE SET {updates}"
        )
        await self._conn.execute(sql, asdict(worker))
        await self._conn.commit()

    async def get_worker(self, worker_id: str) -> Worker | None:
        cursor = await self._conn.execute(
            "SELECT * FROM workers WHERE worker_id = ?", (worker_id,)
        )
        row = await cursor.fetchone()
        return Worker(**dict(row)) if row is not None else None

    # -- batches ---------------------------------------------------------

    async def insert_batch(self, batch: Batch) -> None:
        await self._conn.execute(_insert_sql("batches", Batch), asdict(batch))
        await self._conn.commit()

    async def get_batch(self, batch_id: str) -> Batch | None:
        cursor = await self._conn.execute(
            "SELECT * FROM batches WHERE batch_id = ?", (batch_id,)
        )
        row = await cursor.fetchone()
        return Batch(**dict(row)) if row is not None else None


async def open_db(path: str) -> Database:
    """Open (creating if needed) a hub database at `path` and apply the schema."""
    conn = await aiosqlite.connect(path)
    conn.row_factory = aiosqlite.Row
    await conn.executescript(_SCHEMA)
    await conn.commit()
    return Database(conn)
