"""Tests for the Task Relay Hub config + SQLite store (M1)."""

import sqlite3

import pytest
import pytest_asyncio

from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.models import Task, Worker


@pytest_asyncio.fixture
async def db(tmp_path):
    # Unclosed aiosqlite connections leave their worker thread running and
    # hang the interpreter at exit — always close.
    conn = await open_db(str(tmp_path / "t.db"))
    yield conn
    await conn.close()


def test_hub_config_defaults():
    cfg = HubConfig()
    assert cfg.queue_timeout_seconds == 900
    assert cfg.first_progress_seconds == 120
    assert cfg.timeout_seconds == 600
    assert cfg.max_attempts == 1
    assert cfg.cancel_grace_seconds == 60
    assert cfg.watch_stream_buffer_events == 1024
    assert cfg.list_tasks_default_limit == 100
    assert cfg.list_tasks_max_limit == 500
    assert cfg.retention_days == 7


@pytest.mark.asyncio
async def test_append_event_is_globally_monotonic(db):
    e1 = await db.append_event(callback_topic="t", task_id="a", kind="STATUS", payload={})
    e2 = await db.append_event(callback_topic="t", task_id="b", kind="STATUS", payload={})
    assert e2.event_id == e1.event_id + 1


@pytest.mark.asyncio
async def test_schema_tables_and_indexes(db):
    rows = await db._conn.execute_fetchall(
        "SELECT type, name, sql FROM sqlite_master WHERE type IN ('table', 'index')"
    )
    tables = {r[1]: r[2] for r in rows if r[0] == "table"}
    indexes = {r[1] for r in rows if r[0] == "index"}
    for table in ("tasks", "batches", "workers", "task_events", "checkpoints"):
        assert table in tables, f"missing table {table}"
    assert "idx_tasks_pending" in indexes
    assert "idx_events_topic" in indexes
    assert "AUTOINCREMENT" in tables["task_events"]
    assert "kind = 'AGGREGATE' OR task_id IS NOT NULL" in tables["task_events"]


@pytest.mark.asyncio
async def test_append_event_check_constraint_rejects_missing_task_id(db):
    with pytest.raises(sqlite3.IntegrityError):
        await db.append_event(callback_topic="t", task_id=None, kind="STATUS", payload={})
    # AGGREGATE rows may carry no task_id.
    ev = await db.append_event(callback_topic="t", task_id=None, kind="AGGREGATE", payload={})
    assert ev.task_id is None


@pytest.mark.asyncio
async def test_task_round_trip_and_status_update(db):
    task = Task(task_id="t1", goal="do something", callback_topic="topic-1", created_at=1.0)
    await db.insert_task(task)
    got = await db.get_task("t1")
    assert got is not None
    assert got.task_id == "t1"
    assert got.goal == "do something"
    assert got.status == "pending"
    assert got.max_attempts == 1

    await db.update_task_status("t1", "running")
    got = await db.get_task("t1")
    assert got.status == "running"

    assert await db.get_task("missing") is None


@pytest.mark.asyncio
async def test_upsert_and_get_worker(db):
    worker = Worker(worker_id="w1", status="idle", last_heartbeat_at=10.0)
    await db.upsert_worker(worker)
    got = await db.get_worker("w1")
    assert got is not None
    assert got.worker_id == "w1"
    assert got.status == "idle"
    assert got.session_modes == "A"

    # Upsert updates in place.
    worker.status = "busy"
    worker.last_heartbeat_at = 20.0
    await db.upsert_worker(worker)
    got = await db.get_worker("w1")
    assert got.status == "busy"
    assert got.last_heartbeat_at == 20.0

    assert await db.get_worker("missing") is None


@pytest.mark.asyncio
async def test_list_events_after_and_oldest_event_id_for_filter(db):
    e1 = await db.append_event(callback_topic="topic-a", task_id="t1", kind="STATUS", payload={"n": 1})
    e2 = await db.append_event(callback_topic="topic-b", task_id="t2", kind="STATUS", payload={"n": 2})
    e3 = await db.append_event(callback_topic="topic-a", task_id="t1", kind="RESULT", payload={"n": 3})

    events = await db.list_events_after("topic-a", e1.event_id)
    assert [e.event_id for e in events] == [e3.event_id]

    oldest = await db.oldest_event_id_for_filter(topic="topic-a")
    assert oldest == e1.event_id
    oldest_b = await db.oldest_event_id_for_filter(topic="topic-b")
    assert oldest_b == e2.event_id
    assert await db.oldest_event_id_for_filter(topic="nope") is None


@pytest.mark.asyncio
async def test_open_db_migrates_cancel_reason_column(tmp_path):
    """Existing databases created before ``cancel_reason`` must gain the column."""
    db_path = str(tmp_path / "legacy.db")
    old_schema = """
    CREATE TABLE tasks (
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
    """
    sync_conn = sqlite3.connect(db_path)
    sync_conn.executescript(old_schema)
    sync_conn.close()

    db = await open_db(db_path)
    try:
        task = Task(
            task_id="t1",
            goal="do something",
            callback_topic="topic-1",
            created_at=1.0,
            cancel_reason="user requested",
        )
        await db.insert_task(task)
        got = await db.get_task("t1")
        assert got is not None
        assert got.cancel_reason == "user requested"
    finally:
        await db.close()
