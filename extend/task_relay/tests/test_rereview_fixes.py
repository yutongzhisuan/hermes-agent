"""Failing tests for the final re-review Important findings (Task Relay P1)."""

from __future__ import annotations

import asyncio
import base64
import gzip
import hashlib
import json
import time

import pytest
import pytest_asyncio

from extend.task_relay.constants import CANCEL_REASON_TIMEOUT
from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.grpc_server import _context_payload_to_dict
from extend.task_relay.hub.models import Checkpoint
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.worker.context_loader import resolve_context_payload
from extend.task_relay.worker.run_payload import run_payload_from_dict
from extend.task_relay.tests.conftest import SECRET


@pytest_asyncio.fixture
async def db(tmp_path):
    conn = await open_db(str(tmp_path / "relay.db"))
    yield conn
    await conn.close()


@pytest_asyncio.fixture
async def bus(db):
    return EventBus(db, HubConfig())


@pytest_asyncio.fixture
async def registry(db):
    return WorkerRegistry(db)


@pytest_asyncio.fixture
async def router(db, bus, registry):
    cfg = HubConfig(
        jwt_secret=SECRET,
        queue_timeout_seconds=1,
        first_progress_seconds=1,
        timeout_seconds=2,
        cancel_grace_seconds=1,
        max_attempts=2,
        retention_days=7,
    )
    return TaskRouter(db, bus, cfg, registry)


def _spec(
    task_id="t1",
    goal="g",
    callback_topic="topic-1",
):
    from extend.task_relay.hub.models import TaskSpec

    return TaskSpec(task_id=task_id, goal=goal, callback_topic=callback_topic)


async def _announce_poll_worker(registry, worker_id, toolsets=None, max_concurrent=1):
    return await registry.announce(
        worker_id=worker_id,
        session_modes="a",
        toolsets=toolsets or (),
        max_concurrent=max_concurrent,
    )


# -----------------------------------------------------------------------------
# Finding 1: cancel attribution must use a dedicated cancel_reason column.
# -----------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_execution_timeout_sets_cancel_reason_timeout(router, registry, db):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(_spec(task_id="t1"), "m1")
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    await router.on_progress("t1", "still alive")
    await asyncio.sleep(2.1)
    await router.tick_timeouts()
    task = await db.get_task("t1")
    assert task.status == "cancelling"
    assert task.cancel_reason == CANCEL_REASON_TIMEOUT
    assert task.summary == CANCEL_REASON_TIMEOUT


@pytest.mark.asyncio
async def test_master_cancel_during_timeout_cancelling_settles_cancelled(
    router, registry, db
):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(_spec(task_id="t1"), "m1")
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    await router.on_progress("t1", "still alive")

    # Lease timeout enters cancelling with the dedicated timeout marker.
    await asyncio.sleep(2.1)
    await router.tick_timeouts()
    task = await db.get_task("t1")
    assert task.status == "cancelling"
    assert task.cancel_reason == CANCEL_REASON_TIMEOUT

    # Master cancels before the grace window expires; attribution must flip.
    await router.on_cancel("t1", reason="user requested")
    task = await db.get_task("t1")
    assert task.cancel_reason == "user requested"

    await asyncio.sleep(1.1)
    await router.tick_timeouts()
    assert (await router.get_status("t1")) == "cancelled"


# -----------------------------------------------------------------------------
# Finding 2: inline_gzip context bytes must round-trip through base64 JSON.
# -----------------------------------------------------------------------------


def test_context_payload_to_dict_base64_encodes_gzip_data():
    original = b"hello gzip world"
    gzip_data = gzip.compress(original)
    ctx = pb.ContextPayload(
        inline_gzip=pb.InlineGzip(gzip_data=gzip_data, sha256="abc123")
    )
    ctx_dict = _context_payload_to_dict(ctx)

    encoded = ctx_dict["inline_gzip"]["gzip_data"]
    assert isinstance(encoded, str)
    assert encoded == base64.b64encode(gzip_data).decode("ascii")


@pytest.mark.asyncio
async def test_resolve_context_payload_decodes_inline_gzip_base64():
    original = b"round-trip payload"
    gzip_data = gzip.compress(original)
    ctx_dict = {
        "inline_gzip": {
            "gzip_data": base64.b64encode(gzip_data).decode("ascii"),
            "sha256": hashlib.sha256(original.decode("utf-8").encode("utf-8")).hexdigest(),
        }
    }
    run = run_payload_from_dict({"task_id": "t1", "context": ctx_dict})
    result = await resolve_context_payload(run.context)
    assert result == original.decode("utf-8")


# -----------------------------------------------------------------------------
# Finding 3: retention pruning removes old rows but preserves recent ones.
# -----------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_prune_old_data_removes_old_events_and_checkpoints_and_tasks(router, db):
    now = time.time()
    old = now - 10 * 86400
    recent = now - 1

    # Tasks referenced by events/checkpoints must exist first (FK enabled).
    await db._conn.execute(
        "INSERT INTO tasks (task_id, goal, callback_topic, status, created_at, completed_at)"
        " VALUES (?, ?, ?, ?, ?, ?)",
        ("old-event-task", "g", "topic", "completed", old, old),
    )
    await db._conn.execute(
        "INSERT INTO tasks (task_id, goal, callback_topic, status, created_at, completed_at)"
        " VALUES (?, ?, ?, ?, ?, ?)",
        ("recent-event-task", "g", "topic", "completed", recent, recent),
    )

    # Old and recent events.
    await db.append_event(
        callback_topic="topic",
        task_id="old-event-task",
        kind="STATUS",
        payload={},
        event_at=old,
    )
    await db.append_event(
        callback_topic="topic",
        task_id="recent-event-task",
        kind="STATUS",
        payload={},
        event_at=recent,
    )

    # Old and recent checkpoints.
    old_ckpt = Checkpoint(
        checkpoint_id="old-ckpt",
        task_id="old-event-task",
        event_id=1,
        checkpoint_at=old,
        resume_blob=b"old blob",
    )
    recent_ckpt = Checkpoint(
        checkpoint_id="recent-ckpt",
        task_id="recent-event-task",
        event_id=2,
        checkpoint_at=recent,
        resume_blob=b"recent blob",
    )
    await db.insert_checkpoint(old_ckpt)
    await db.insert_checkpoint(recent_ckpt)

    # Old terminal task and recent non-terminal task.
    await db._conn.execute(
        "INSERT INTO tasks (task_id, goal, callback_topic, status, created_at, completed_at)"
        " VALUES (?, ?, ?, ?, ?, ?)",
        ("old-terminal", "g", "topic", "completed", old, old),
    )
    await db._conn.execute(
        "INSERT INTO tasks (task_id, goal, callback_topic, status, created_at, completed_at)"
        " VALUES (?, ?, ?, ?, ?, ?)",
        ("recent-terminal", "g", "topic", "completed", recent, recent),
    )
    await db._conn.commit()

    await router.prune_old_data()

    events = await db.list_events_after("topic", 0)
    assert [e.task_id for e in events] == ["recent-event-task"]

    old_ckpt_row = await db.get_latest_checkpoint("old-event-task")
    recent_ckpt_row = await db.get_latest_checkpoint("recent-event-task")
    assert old_ckpt_row is None
    assert recent_ckpt_row is not None

    old_task = await db.get_task("old-terminal")
    recent_task = await db.get_task("recent-terminal")
    assert old_task is None
    assert recent_task is not None
