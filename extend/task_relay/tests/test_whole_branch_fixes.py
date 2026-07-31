"""Regression tests for the final whole-branch Important findings."""

from __future__ import annotations

import asyncio
import json
import time
from typing import Any

import pytest
import pytest_asyncio

from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.models import TaskSpec
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.tests.conftest import SECRET, make_worker_jwt
from extend.task_relay.worker.task_executor import (
    TaskBackend,
    TaskCompletePayload,
    TaskExecutor,
    TaskRunPayload,
)
from extend.task_relay.worker.task_worker import TaskWorker


@pytest_asyncio.fixture
async def db(tmp_path):
    conn = await open_db(str(tmp_path / "whole.db"))
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


async def _announce_poll_worker(registry, worker_id, toolsets=None, max_concurrent=1):
    return await registry.announce(
        worker_id=worker_id,
        session_modes="a",
        toolsets=toolsets or (),
        max_concurrent=max_concurrent,
    )


def _spec(task_id="t1", goal="g", callback_topic="topic-1") -> TaskSpec:
    return TaskSpec(task_id=task_id, goal=goal, callback_topic=callback_topic)


# -----------------------------------------------------------------------------
# Finding 1: redispatch is controlled by the current request's allow_redispatch.
# -----------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_redispatch_uses_current_request_allow_redispatch(router, db):
    """A new dispatch with allow_redispatch=true must reopen a terminal task
    even if the original dispatch had allow_redispatch=false.
    """
    task_id = "redispatch-flag"
    await router.dispatch_task(
        _spec(task_id=task_id),
        master_session_id="m1",
        allow_redispatch=False,
    )

    # Force the task into a terminal lost state.
    await db._conn.execute(
        "UPDATE tasks SET status = ?, completed_at = ?, attempt = ? WHERE task_id = ?",
        ("lost", time.time(), 1, task_id),
    )
    await db._conn.commit()

    # First redispatch with allow_redispatch=false is idempotent.
    resp_false = await router.dispatch_task(
        _spec(task_id=task_id),
        master_session_id="m1",
        allow_redispatch=False,
    )
    assert resp_false.idempotent_hit is True
    assert resp_false.status == "lost"

    # Second redispatch with allow_redispatch=true reopens the task.
    resp_true = await router.dispatch_task(
        _spec(task_id=task_id),
        master_session_id="m1",
        allow_redispatch=True,
    )
    assert resp_true.idempotent_hit is False
    assert resp_true.status == "pending"

    task = await db.get_task(task_id)
    assert task.status == "pending"
    assert task.allow_redispatch == 1


@pytest.mark.asyncio
async def test_redispatch_updates_stored_allow_redispatch_flag(router, db):
    """Dispatching a terminal task with a different allow_redispatch value
    must persist the new value.
    """
    task_id = "redispatch-update"
    await router.dispatch_task(
        _spec(task_id=task_id),
        master_session_id="m1",
        allow_redispatch=True,
    )
    await db._conn.execute(
        "UPDATE tasks SET status = ?, completed_at = ?, attempt = ? WHERE task_id = ?",
        ("failed", time.time(), 1, task_id),
    )
    await db._conn.commit()

    await router.dispatch_task(
        _spec(task_id=task_id),
        master_session_id="m1",
        allow_redispatch=False,
    )

    task = await db.get_task(task_id)
    assert task.allow_redispatch == 0


# -----------------------------------------------------------------------------
# Finding 3: TaskExecutor allows a fallback completion after a send failure.
# -----------------------------------------------------------------------------


class FlakyOnceWs:
    """Fails the first task.complete request, then succeeds."""

    def __init__(self) -> None:
        self.complete_calls = 0

    async def request(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        if method == "task.complete":
            self.complete_calls += 1
            if self.complete_calls == 1:
                raise RuntimeError("transport down")
        return {}


@pytest.mark.asyncio
async def test_executor_allows_retry_after_send_failure():
    """After a failed task.complete, a second _complete_once call must send."""
    ws = FlakyOnceWs()
    executor = TaskExecutor(ws, _ImmediateBackend())

    with pytest.raises(RuntimeError, match="transport down"):
        await executor.execute(
            TaskRunPayload(
                task_id="t1",
                attempt=1,
                goal="g",
                params=None,
                context=None,
                toolsets=[],
                timeout_seconds=60,
                first_progress_seconds=None,
                trace_context=None,
                resume_from_checkpoint=None,
            ),
            asyncio.Event(),
        )

    assert ws.complete_calls == 1
    assert not executor.completion_attempted

    success = await executor._complete_once(
        "t1",
        TaskCompletePayload(status="failed", summary="fallback"),
    )
    assert success is True
    assert ws.complete_calls == 2
    assert executor.completion_attempted


class _ImmediateBackend(TaskBackend):
    async def run(self, run, on_progress, on_checkpoint, cancel_event):
        return TaskCompletePayload(status="completed", summary="done")


# Replaces the previous expectation that a send failure blocks all retries.
@pytest.mark.asyncio
async def test_worker_sends_fallback_complete_after_send_failure(monkeypatch):
    """A failed task.complete must still allow the outer error handler to emit
    a fallback failure completion.
    """
    from extend.task_relay.tests.test_worker import FakeWorkerWs, _run_payload_dict

    fake_ws = FakeWorkerWs()
    fake_ws.raise_on_complete = True
    monkeypatch.setattr(
        "extend.task_relay.worker.task_worker.TaskWorkerWs",
        lambda *args, **kwargs: fake_ws,
    )

    worker = TaskWorker(
        worker_id="w1",
        relay_url="ws://x",
        jwt=make_worker_jwt("w1"),
        backend=_ImmediateBackend(),
        poll_wait_ms=10_000,
    )
    fake_ws.poll_results.append([_run_payload_dict("t1")])

    await _run_worker_until(worker, lambda: fake_ws.complete_count >= 2, timeout=2.0)

    completes = [r for r in fake_ws.requests if r[0] == "task.complete"]
    assert len(completes) == 2
    assert completes[0][1]["status"] == "completed"
    assert completes[1][1]["status"] == "failed"


async def _run_worker_until(worker: TaskWorker, predicate, timeout: float = 5.0) -> None:
    run_task = asyncio.create_task(worker.run())
    try:
        deadline = asyncio.get_event_loop().time() + timeout
        while not predicate() and asyncio.get_event_loop().time() < deadline:
            await asyncio.sleep(0.02)
        await worker.shutdown()
        await asyncio.wait_for(run_task, timeout=2.0)
    except asyncio.TimeoutError:
        run_task.cancel()
        try:
            await run_task
        except Exception:
            pass


# -----------------------------------------------------------------------------
# Finding 4: requeue after lease expiry emits a STATUS pending event.
# -----------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_lost_requeue_emits_status_pending_event(router, registry, db):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(
        _spec(task_id="t1"),
        master_session_id="m1",
        allow_redispatch=True,
    )
    claimed = await router.atomic_claim_for_poll("w1", max_tasks=1)
    assert len(claimed) == 1

    before = await db.newest_event_id()

    # Let the first-progress deadline expire.
    await asyncio.sleep(1.1)
    await router.tick_timeouts()

    task = await db.get_task("t1")
    assert task.status == "pending"

    new_events = await db.list_events_after("topic-1", before or 0)
    kinds = [e.kind for e in new_events]
    assert "PROGRESS" in kinds
    assert "STATUS" in kinds
    status_events = [e for e in new_events if e.kind == "STATUS"]
    assert any(
        json.loads(e.payload_json or "{}").get("status") == "pending"
        for e in status_events
    )


@pytest.mark.asyncio
async def test_failed_requeue_emits_status_pending_event(router, registry, db):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(
        _spec(task_id="t1"),
        master_session_id="m1",
        allow_redispatch=True,
    )
    claimed = await router.atomic_claim_for_poll("w1", max_tasks=1)
    assert len(claimed) == 1

    # Send progress to satisfy first-progress deadline, then let lease expire.
    await router.on_progress("t1", "still alive")

    before = await db.newest_event_id()

    await asyncio.sleep(2.1)
    await router.tick_timeouts()  # enters cancelling
    await asyncio.sleep(1.1)
    await router.tick_timeouts()  # settles as failed, requeues

    task = await db.get_task("t1")
    assert task.status == "pending"

    new_events = await db.list_events_after("topic-1", before or 0)
    kinds = [e.kind for e in new_events]
    assert "PROGRESS" in kinds
    assert "STATUS" in kinds
    status_events = [e for e in new_events if e.kind == "STATUS"]
    assert any(
        json.loads(e.payload_json or "{}").get("status") == "pending"
        for e in status_events
    )
