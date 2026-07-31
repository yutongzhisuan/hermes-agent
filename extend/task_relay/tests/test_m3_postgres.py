"""M3 Postgres store adapter tests."""

from __future__ import annotations

import os

import pytest
import pytest_asyncio

from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.db_conn import _translate_named, _translate_qmark
from extend.task_relay.tests.conftest import make_task_spec


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


@pytest.mark.asyncio
async def test_sqlite_open_db_still_default(tmp_path):
    db = await open_db(str(tmp_path / "relay.db"))
    assert db.dialect == "sqlite"
    await db.close()


POSTGRES_URL = os.environ.get("TASK_RELAY_TEST_PG")


@pytest_asyncio.fixture
async def pg_db():
    if not POSTGRES_URL:
        pytest.skip("TASK_RELAY_TEST_PG not set")
    db = await open_db(POSTGRES_URL)
    yield db
    await db.close()


@pytest.mark.skipif(not POSTGRES_URL, reason="TASK_RELAY_TEST_PG not set")
@pytest.mark.asyncio
async def test_postgres_task_roundtrip(pg_db):
    assert pg_db.dialect == "postgres"
    spec = make_task_spec(task_id="pg-t1")
    from extend.task_relay.hub.task_router import TaskRouter
    from extend.task_relay.hub.event_bus import EventBus
    from extend.task_relay.hub.config import HubConfig
    from extend.task_relay.hub.worker_registry import WorkerRegistry
    from extend.task_relay.tests.conftest import SECRET

    bus = EventBus(pg_db, HubConfig(jwt_secret=SECRET))
    registry = WorkerRegistry(pg_db)
    router = TaskRouter(pg_db, bus, HubConfig(jwt_secret=SECRET), registry)
    response = await router.dispatch_task(spec, master_session_id="m1")
    assert response.task_id == "pg-t1"
    task = await pg_db.get_task("pg-t1")
    assert task is not None
    assert task.status == "pending"


@pytest.mark.asyncio
async def test_postgres_conn_event_id_monotonic(tmp_path):
    try:
        import asyncpg
    except ImportError:
        pytest.skip("asyncpg not installed")

    if not POSTGRES_URL:
        pytest.skip("TASK_RELAY_TEST_PG not set")

    db = await open_db(POSTGRES_URL)
    try:
        e1 = await db.append_event(
            callback_topic="topic",
            task_id="t1",
            kind="task.progress",
            payload={"step": 1},
        )
        e2 = await db.append_event(
            callback_topic="topic",
            task_id="t1",
            kind="task.progress",
            payload={"step": 2},
        )
        assert e1.event_id is not None
        assert e2.event_id is not None
        assert e2.event_id > e1.event_id
    finally:
        await db.close()
