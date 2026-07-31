"""M3 Postgres store adapter tests (unit + optional real-DB integration)."""

from __future__ import annotations

import json
import os
import uuid

import pytest
import pytest_asyncio

from extend.task_relay.hub.bootstrap import wire_orchestration
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.db_conn import _translate_named, _translate_qmark
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.models import Checkpoint, Task, Worker
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.tests.conftest import SECRET, make_task_spec

POSTGRES_URL = os.environ.get("TASK_RELAY_TEST_PG")
_PG_TABLES = ("checkpoints", "task_events", "tasks", "workers", "batches")


def test_translate_qmark():
    sql, params = _translate_qmark("SELECT * FROM t WHERE a = ? AND b = ?", ("x", 1))
    assert sql == "SELECT * FROM t WHERE a = $1 AND b = $2"
    assert params == ["x", 1]


def test_translate_named():
    sql, params = _translate_named(
        "UPDATE tasks SET status = :status WHERE task_id = :task_id",
        {"status": "running", "task_id": "t1"},
    )
    assert "$1" in sql and "$2" in sql
    assert set(params) == {"running", "t1"}


def test_hub_config_from_args_security_flags():
    from extend.task_relay.hub.config import hub_config_from_args, parse_args

    args = parse_args(
        [
            "--jwt-secret",
            "secret",
            "--require-signed-context-ref",
            "--encrypt-inline-context-at-rest",
        ]
    )
    cfg = hub_config_from_args(args)
    assert cfg.require_signed_context_ref is True
    assert cfg.encrypt_inline_context_at_rest is True
    sql, params = _translate_named(
        "UPDATE tasks SET status = :status WHERE task_id = :task_id",
        {"status": "running", "task_id": "t1"},
    )
    assert "$1" in sql and "$2" in sql
    assert set(params) == {"running", "t1"}


@pytest.mark.asyncio
async def test_sqlite_open_db_still_default(tmp_path):
    db = await open_db(str(tmp_path / "relay.db"))
    assert db.dialect == "sqlite"
    await db.close()


async def _truncate_pg(db) -> None:
    await db._conn.execute(
        f"TRUNCATE {', '.join(_PG_TABLES)} RESTART IDENTITY CASCADE"
    )


@pytest_asyncio.fixture
async def pg_db():
    if not POSTGRES_URL:
        pytest.skip("TASK_RELAY_TEST_PG not set")
    db = await open_db(POSTGRES_URL)
    await _truncate_pg(db)
    yield db
    await _truncate_pg(db)
    await db.close()


@pytest_asyncio.fixture
async def pg_router(pg_db):
    bus = EventBus(pg_db, HubConfig(jwt_secret=SECRET))
    registry = WorkerRegistry(pg_db)
    router = TaskRouter(pg_db, bus, HubConfig(jwt_secret=SECRET), registry)
    wire_orchestration(router, pg_db, bus)
    return router, registry, pg_db, bus


@pytest.mark.integration
@pytest.mark.asyncio
async def test_postgres_schema_and_worker_upsert(pg_db):
    assert pg_db.dialect == "postgres"
    await pg_db.upsert_worker(
        Worker(
            worker_id="pg-w1",
            session_modes="A",
            capabilities_json=json.dumps({"toolsets": ["terminal"]}),
            max_concurrent=2,
            status="idle",
            last_seen_at=1.0,
        )
    )
    await pg_db.upsert_worker(
        Worker(
            worker_id="pg-w1",
            session_modes="A,C",
            capabilities_json=json.dumps({"toolsets": ["terminal", "file"]}),
            max_concurrent=2,
            status="busy",
            last_seen_at=2.0,
        )
    )
    loaded = await pg_db.get_worker("pg-w1")
    assert loaded is not None
    assert loaded.session_modes == "A,C"
    assert loaded.status == "busy"


@pytest.mark.integration
@pytest.mark.asyncio
async def test_postgres_dispatch_claim_complete(pg_router):
    router, registry, db, _bus = pg_router
    await registry.announce(
        worker_id="pg-w1",
        session_modes="A",
        toolsets=["terminal"],
        max_concurrent=1,
        online_session_id=str(uuid.uuid4()),
    )
    spec = make_task_spec(task_id="pg-dispatch-1")
    response = await router.dispatch_task(spec, master_session_id="m1")
    assert response.task_id == "pg-dispatch-1"

    claimed = await router.atomic_claim_for_poll("pg-w1", max_tasks=1)
    assert len(claimed) == 1
    await router.on_progress("pg-dispatch-1", "started")
    await router.on_complete("pg-dispatch-1", status="completed", summary="ok")

    task = await db.get_task("pg-dispatch-1")
    assert task is not None
    assert task.status == "completed"
    assert task.summary == "ok"


@pytest.mark.integration
@pytest.mark.asyncio
async def test_postgres_event_log_monotonic_and_replay(pg_db):
    e1 = await pg_db.append_event(
        callback_topic="pg-topic",
        task_id="pg-e1",
        kind="task.progress",
        payload={"step": 1},
    )
    e2 = await pg_db.append_event(
        callback_topic="pg-topic",
        task_id="pg-e1",
        kind="task.progress",
        payload={"step": 2},
    )
    assert e1.event_id is not None
    assert e2.event_id is not None
    assert e2.event_id > e1.event_id

    replay = await pg_db.list_events_after("pg-topic", after_event_id=e1.event_id)
    assert len(replay) == 1
    assert replay[0].event_id == e2.event_id


@pytest.mark.integration
@pytest.mark.asyncio
async def test_postgres_checkpoint_blob(pg_db):
    await pg_db.insert_task(
        Task(
            task_id="pg-ckpt",
            goal="g",
            callback_topic="t",
            status="running",
            created_at=1.0,
        )
    )
    blob = b"\x00resume-bytes\xff"
    await pg_db.insert_checkpoint(
        Checkpoint(
            checkpoint_id="ck1",
            task_id="pg-ckpt",
            event_id=1,
            checkpoint_at=2.0,
            summary="half",
            resume_blob=blob,
        )
    )
    loaded = await pg_db.get_latest_checkpoint("pg-ckpt")
    assert loaded is not None
    assert loaded.resume_blob == blob


@pytest.mark.integration
@pytest.mark.asyncio
async def test_postgres_batch_aggregate(pg_router):
    router, registry, db, _bus = pg_router
    await registry.announce(
        worker_id="pg-w1",
        session_modes="A",
        toolsets=[],
        max_concurrent=2,
        online_session_id=str(uuid.uuid4()),
    )
    await router.dispatch_task_batch(
        [
            make_task_spec(task_id="pg-g1", aggregate_key="grp", callback_topic="pg-agg-topic"),
            make_task_spec(task_id="pg-g2", aggregate_key="grp", callback_topic="pg-agg-topic"),
        ],
        batch_id="pg-batch-1",
        master_session_id="m1",
        callback_topic="pg-agg-topic",
    )
    await router.atomic_claim_for_poll("pg-w1", max_tasks=1)
    await router.on_complete("pg-g1", status="completed", summary="s1")
    await router.atomic_claim_for_poll("pg-w1", max_tasks=1)
    await router.on_complete("pg-g2", status="completed", summary="s2")

    cursor = await db._conn.execute(
        "SELECT kind FROM task_events WHERE callback_topic = ?",
        ("pg-agg-topic",),
    )
    kinds = [row["kind"] for row in await cursor.fetchall()]
    assert "AGGREGATE" in kinds
