"""Store for the Task Relay Hub (SQLite default, Postgres for HA).

Schema is verbatim from the design spec §Persistence (portable types). The
``task_events.event_id`` monotonic sequence is the single globally-monotonic
event cursor the WatchTask semantics rely on.
"""

import json
import time
from dataclasses import asdict, fields
from typing import Any

import aiosqlite

from extend.task_relay.hub.db_conn import (
    PostgresDbConn,
    SqliteDbConn,
    is_postgres_url,
)
from extend.task_relay.hub.db_schema import (
    POSTGRES_MIGRATIONS,
    SCHEMA_POSTGRES,
    SCHEMA_SQLITE,
)
from extend.task_relay.hub.json_util import safe_json_loads
from extend.task_relay.hub.models import Batch, Checkpoint, Task, TaskEvent, Worker


def _insert_sql(table: str, model: type) -> str:
    cols = [f.name for f in fields(model)]
    placeholders = ", ".join(f":{c}" for c in cols)
    return f"INSERT INTO {table} ({', '.join(cols)}) VALUES ({placeholders})"


class Database:
    """Async wrapper around a store connection with the hub schema."""

    def __init__(self, conn: SqliteDbConn | PostgresDbConn):
        self._conn = conn

    @property
    def dialect(self) -> str:
        return self._conn.dialect

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
            "SELECT * FROM tasks WHERE batch_id = ? ORDER BY created_at ASC",
            (batch_id,),
        )
        return [Task(**dict(row)) for row in await cursor.fetchall()]

    async def list_pending_tasks(self) -> list[Task]:
        cursor = await self._conn.execute(
            "SELECT * FROM tasks WHERE status = 'pending' ORDER BY created_at ASC"
        )
        return [Task(**dict(row)) for row in await cursor.fetchall()]

    async def update_batch(self, batch: Batch) -> None:
        await self._conn.execute(
            "UPDATE batches SET batch_deadline_at = ?, policy_json = ? WHERE batch_id = ?",
            (batch.batch_deadline_at, batch.policy_json, batch.batch_id),
        )
        await self._conn.commit()

    async def aggregate_event_exists(self, batch_id: str, aggregate_key: str) -> bool:
        cursor = await self._conn.execute(
            "SELECT payload_json FROM task_events WHERE batch_id = ? AND kind = 'AGGREGATE'",
            (batch_id,),
        )
        for row in await cursor.fetchall():
            payload = safe_json_loads(row["payload_json"])
            if isinstance(payload, dict) and payload.get("aggregate_key") == aggregate_key:
                return True
        return False

    async def list_tasks(
        self,
        *,
        batch_id: str | None = None,
        callback_topic: str | None = None,
        master_session_id: str | None = None,
        statuses: list[str] | None = None,
        worker_id: str | None = None,
        limit: int = 100,
        offset: int = 0,
    ) -> list[Task]:
        clauses: list[str] = []
        params: list = []
        if batch_id is not None:
            clauses.append("batch_id = ?")
            params.append(batch_id)
        if callback_topic is not None:
            clauses.append("callback_topic = ?")
            params.append(callback_topic)
        if master_session_id is not None:
            clauses.append("master_session_id = ?")
            params.append(master_session_id)
        if statuses:
            clauses.append(f"status IN ({','.join('?' for _ in statuses)})")
            params.extend(statuses)
        if worker_id is not None:
            clauses.append("worker_id = ?")
            params.append(worker_id)
        where = f"WHERE {' AND '.join(clauses)}" if clauses else ""
        cursor = await self._conn.execute(
            f"SELECT * FROM tasks {where} ORDER BY created_at DESC, task_id"
            " LIMIT ? OFFSET ?",
            (*params, limit, offset),
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

    async def list_workers(
        self, *, only_schedulable: bool = False
    ) -> list[Worker]:
        clauses: list[str] = []
        if only_schedulable:
            clauses.append("status NOT IN ('offline', 'stale', 'draining')")
        where = f"WHERE {' AND '.join(clauses)}" if clauses else ""
        cursor = await self._conn.execute(
            f"SELECT * FROM workers {where} ORDER BY worker_id"
        )
        return [Worker(**dict(row)) for row in await cursor.fetchall()]

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


async def _migrate_sqlite(conn: SqliteDbConn) -> None:
    """Apply additive schema migrations for SQLite."""
    raw = conn._conn

    async def _add_column_if_missing(
        table: str, column: str, ddl: str, pragma_table: str | None = None
    ) -> None:
        try:
            await raw.execute(ddl)
        except aiosqlite.OperationalError as exc:
            msg = str(exc).lower()
            if "duplicate column name" in msg:
                return
            if "syntax error" not in msg:
                raise
            rows = await raw.execute_fetchall(f"PRAGMA table_info({pragma_table or table})")
            columns = {row[1] for row in rows}
            if column not in columns:
                fallback = ddl.replace(" IF NOT EXISTS", "")
                await raw.execute(fallback)

    await _add_column_if_missing(
        "tasks",
        "cancel_reason",
        "ALTER TABLE tasks ADD COLUMN IF NOT EXISTS cancel_reason TEXT",
    )
    await _add_column_if_missing(
        "workers",
        "last_seen_at",
        "ALTER TABLE workers ADD COLUMN IF NOT EXISTS last_seen_at REAL",
    )
    await _add_column_if_missing(
        "workers",
        "drain_requested",
        "ALTER TABLE workers ADD COLUMN IF NOT EXISTS drain_requested INTEGER DEFAULT 0",
    )
    await _add_column_if_missing(
        "tasks",
        "target_worker",
        "ALTER TABLE tasks ADD COLUMN IF NOT EXISTS target_worker TEXT",
    )
    await raw.commit()


async def _migrate_postgres(conn: PostgresDbConn) -> None:
    for statement in POSTGRES_MIGRATIONS:
        await conn.execute(statement)
    await conn.commit()


async def open_db(path_or_url: str) -> Database:
    """Open a hub database (SQLite path or ``postgres://`` URL)."""
    if is_postgres_url(path_or_url):
        return await _open_postgres(path_or_url)
    return await _open_sqlite(path_or_url)


async def _open_sqlite(path: str) -> Database:
    raw = await aiosqlite.connect(path)
    raw.row_factory = aiosqlite.Row
    await raw.execute("PRAGMA foreign_keys = ON")
    conn = SqliteDbConn(raw)
    await conn.executescript(SCHEMA_SQLITE)
    await _migrate_sqlite(conn)
    await conn.commit()
    return Database(conn)


async def _open_postgres(url: str) -> Database:
    try:
        import asyncpg
    except ImportError as exc:
        raise RuntimeError(
            "asyncpg is required for postgres:// URLs; install with "
            "uv sync --extra task-relay"
        ) from exc

    raw: Any = await asyncpg.connect(url)
    conn = PostgresDbConn(raw)
    await conn.executescript(SCHEMA_POSTGRES)
    await _migrate_postgres(conn)
    return Database(conn)
