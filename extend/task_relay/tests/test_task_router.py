"""Tests for the Task Relay Hub TaskRouter + WorkerRegistry (M1).

TDD order: write failing tests first, then implement the router/registry.
"""

import asyncio
import json
import time

import pytest
import pytest_asyncio

from extend.task_relay.hub.auth import WorkerClaims
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.models import TaskSpec
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.hub.task_router import (
    BatchDispatchResponse,
    ClaimedTask,
    DispatchTaskResponse,
    TaskRouter,
    TaskRouterError,
)


SECRET = "t" * 32


@pytest_asyncio.fixture
async def db(tmp_path):
    conn = await open_db(str(tmp_path / "t.db"))
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
    )
    return TaskRouter(db, bus, cfg, registry)


def spec(
    task_id="t1",
    goal="g",
    callback_topic="topic-1",
    toolsets=None,
    allowed_worker_ids=None,
    deny_worker_ids=None,
    timeout_seconds=None,
    queue_timeout_seconds=None,
    max_attempts=None,
    first_progress_seconds=None,
    priority=0,
    depends_on=None,
) -> TaskSpec:
    return TaskSpec(
        task_id=task_id,
        goal=goal,
        callback_topic=callback_topic,
        toolsets_json=json.dumps(toolsets) if toolsets is not None else None,
        allowed_worker_ids_json=json.dumps(allowed_worker_ids)
        if allowed_worker_ids is not None
        else None,
        deny_worker_ids_json=json.dumps(deny_worker_ids)
        if deny_worker_ids is not None
        else None,
        timeout_seconds=timeout_seconds,
        queue_timeout_seconds=queue_timeout_seconds,
        max_attempts=max_attempts,
        first_progress_seconds=first_progress_seconds,
        priority=priority,
        depends_on_json=json.dumps(depends_on) if depends_on is not None else None,
    )


async def _announce_poll_worker(registry, worker_id, toolsets=None, max_concurrent=1):
    return await registry.announce(
        worker_id=worker_id,
        session_modes="a",
        toolsets=toolsets or (),
        max_concurrent=max_concurrent,
    )


# ---------------------------------------------------------------------------
# Dispatch / idempotency
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_idempotent_dispatch(router):
    r1 = await router.dispatch_task(spec(task_id="t1", goal="g"), "m1")
    r2 = await router.dispatch_task(spec(task_id="t1", goal="g"), "m1")
    assert isinstance(r1, DispatchTaskResponse)
    assert r2.idempotent_hit is True
    assert r1.task_id == r2.task_id == "t1"
    assert r2.status == "pending"


@pytest.mark.asyncio
async def test_dispatch_returns_actual_status(router, registry):
    await router.dispatch_task(spec(task_id="t1"), "m1")
    await _announce_poll_worker(registry, "w1")
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    r = await router.dispatch_task(spec(task_id="t1"), "m1")
    assert r.idempotent_hit is True
    assert r.status == "running"


@pytest.mark.asyncio
async def test_dispatch_rejects_invalid_spec(router):
    with pytest.raises(TaskRouterError):
        await router.dispatch_task(spec(task_id="", goal="g"), "m1")


@pytest.mark.asyncio
async def test_dispatch_uses_spec_timeouts(router, db):
    s = spec(task_id="t1", queue_timeout_seconds=30, timeout_seconds=60)
    await router.dispatch_task(s, "m1")
    task = await db.get_task("t1")
    assert task.queue_timeout_seconds == 30
    assert task.timeout_seconds == 60


# ---------------------------------------------------------------------------
# Atomic claim
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_atomic_claim_moves_pending_to_running(router, registry):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(spec(task_id="t1", goal="g"), "m1")
    claimed = await router.atomic_claim_for_poll("w1", max_tasks=1)
    assert len(claimed) == 1
    assert claimed[0].task_id == "t1"
    assert (await router.get_status("t1")) == "running"


@pytest.mark.asyncio
async def test_claim_emits_running_status_and_progress(router, registry, bus):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(spec(task_id="t1"), "m1")
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    events = await bus._db.list_events_after("topic-1", 0)
    kinds = [e.kind for e in events]
    assert "STATUS" in kinds
    assert "PROGRESS" in kinds


@pytest.mark.asyncio
async def test_claim_increments_attempt_and_sets_deadlines(router, registry, db):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(spec(task_id="t1"), "m1")
    before = time.time()
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    after = time.time()
    task = await db.get_task("t1")
    assert task.attempt == 1
    assert task.worker_id == "w1"
    assert task.first_progress_deadline_at is not None
    assert before + 0.5 <= task.first_progress_deadline_at <= after + 1.5
    assert task.claim_expires_at is not None
    assert task.claim_token is not None


@pytest.mark.asyncio
async def test_claim_respects_max_tasks(router, registry):
    await _announce_poll_worker(registry, "w1", max_concurrent=2)
    await router.dispatch_task(spec(task_id="t1"), "m1")
    await router.dispatch_task(spec(task_id="t2"), "m1")
    claimed = await router.atomic_claim_for_poll("w1", max_tasks=1)
    assert len(claimed) == 1


@pytest.mark.asyncio
async def test_claim_skips_missing_worker(router):
    await router.dispatch_task(spec(task_id="t1"), "m1")
    claimed = await router.atomic_claim_for_poll("w1", max_tasks=1)
    assert claimed == []


@pytest.mark.asyncio
async def test_claim_skips_offline_or_draining_worker(router, registry):
    await router.dispatch_task(spec(task_id="t1"), "m1")
    await registry.announce("w1", session_modes="a", status="offline")
    assert await router.atomic_claim_for_poll("w1", max_tasks=1) == []
    await registry.announce("w1", session_modes="a", status="draining")
    assert await router.atomic_claim_for_poll("w1", max_tasks=1) == []


@pytest.mark.asyncio
async def test_claim_respects_allowed_worker_ids(router, registry):
    await _announce_poll_worker(registry, "w1")
    await _announce_poll_worker(registry, "w2")
    await router.dispatch_task(
        spec(task_id="t1", allowed_worker_ids=["w2"]), "m1"
    )
    assert await router.atomic_claim_for_poll("w1", max_tasks=1) == []
    claimed = await router.atomic_claim_for_poll("w2", max_tasks=1)
    assert len(claimed) == 1


@pytest.mark.asyncio
async def test_claim_respects_deny_worker_ids(router, registry):
    await _announce_poll_worker(registry, "w1")
    await _announce_poll_worker(registry, "w2")
    await router.dispatch_task(spec(task_id="t1", deny_worker_ids=["w1"]), "m1")
    assert await router.atomic_claim_for_poll("w1", max_tasks=1) == []
    assert len(await router.atomic_claim_for_poll("w2", max_tasks=1)) == 1


@pytest.mark.asyncio
async def test_claim_respects_toolsets(router, registry):
    await _announce_poll_worker(registry, "w1", toolsets=["terminal"])
    await _announce_poll_worker(registry, "w2", toolsets=["file"])
    await router.dispatch_task(spec(task_id="t1", toolsets=["terminal"]), "m1")
    assert await router.atomic_claim_for_poll("w2", max_tasks=1) == []
    claimed = await router.atomic_claim_for_poll("w1", max_tasks=1)
    assert len(claimed) == 1


@pytest.mark.asyncio
async def test_claim_jwt_toolset_scope_allows_claim(router, registry):
    await _announce_poll_worker(registry, "w1", toolsets=["terminal", "file"])
    await router.dispatch_task(spec(task_id="t1", toolsets=["file"]), "m1")
    claims = WorkerClaims(
        sub="w1", allowed_toolsets=["terminal", "file"], max_concurrent=1, exp=9999999999
    )
    claimed = await router.atomic_claim_for_poll("w1", max_tasks=1, worker_claims=claims)
    assert len(claimed) == 1


@pytest.mark.asyncio
async def test_claim_jwt_toolset_scope_denies_claim(router, registry):
    await _announce_poll_worker(registry, "w1", toolsets=["terminal", "file"])
    await router.dispatch_task(spec(task_id="t1", toolsets=["file"]), "m1")
    claims = WorkerClaims(
        sub="w1", allowed_toolsets=["terminal"], max_concurrent=1, exp=9999999999
    )
    assert await router.atomic_claim_for_poll("w1", max_tasks=1, worker_claims=claims) == []


@pytest.mark.asyncio
async def test_claim_jwt_toolset_scope_intersects_with_advertised(router, registry):
    await _announce_poll_worker(registry, "w1", toolsets=["terminal", "file"])
    await router.dispatch_task(spec(task_id="t1", toolsets=["terminal", "file"]), "m1")
    # JWT allows terminal only, so the file requirement cannot be satisfied
    # even though the worker advertised it.
    claims = WorkerClaims(
        sub="w1", allowed_toolsets=["terminal"], max_concurrent=1, exp=9999999999
    )
    assert await router.atomic_claim_for_poll("w1", max_tasks=1, worker_claims=claims) == []


# ---------------------------------------------------------------------------
# Progress / complete
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_on_progress_extends_lease_and_clears_first_progress_deadline(
    router, registry, db
):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(spec(task_id="t1"), "m1")
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    before = await db.get_task("t1")
    assert before.first_progress_deadline_at is not None
    await asyncio.sleep(0.1)
    await router.on_progress("t1", summary="working")
    after = await db.get_task("t1")
    assert after.first_progress_deadline_at is None
    assert after.claim_expires_at > before.claim_expires_at


@pytest.mark.asyncio
async def test_on_progress_during_cancelling_does_not_extend_grace_window(
    router, registry, db
):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(spec(task_id="t1"), "m1")
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    await router.on_cancel("t1", reason="stop")
    before = await db.get_task("t1")
    assert before.status == "cancelling"
    assert before.claim_expires_at is not None
    await asyncio.sleep(0.1)
    await router.on_progress("t1", summary="still shutting down")
    after = await db.get_task("t1")
    assert after.status == "cancelling"
    assert after.claim_expires_at == before.claim_expires_at


@pytest.mark.asyncio
async def test_complete_is_monotonic(router, registry):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(spec(task_id="t1"), "m1")
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    await router.on_complete("t1", status="completed", summary="ok")
    await router.on_complete("t1", status="failed", summary="nope")
    assert (await router.get_status("t1")) == "completed"


@pytest.mark.asyncio
async def test_complete_emits_terminal_event(router, registry, bus):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(spec(task_id="t1"), "m1")
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    await router.on_complete("t1", status="completed", summary="ok")
    events = await bus._db.list_events_after("topic-1", 0)
    assert any(e.kind == "TERMINAL" for e in events)


@pytest.mark.asyncio
async def test_on_complete_for_missing_task_raises(router):
    with pytest.raises(TaskRouterError):
        await router.on_complete("missing", status="completed")


# ---------------------------------------------------------------------------
# Timeouts
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_queue_timeout_marks_lost(router):
    await router.dispatch_task(spec(task_id="t1"), "m1")
    await asyncio.sleep(1.1)
    await router.tick_timeouts()
    assert (await router.get_status("t1")) == "lost"


@pytest.mark.asyncio
async def test_first_progress_deadline_marks_lost(router, registry):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(spec(task_id="t1"), "m1")
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    await asyncio.sleep(1.1)
    await router.tick_timeouts()
    assert (await router.get_status("t1")) == "lost"


@pytest.mark.asyncio
async def test_execution_lease_timeout_enters_cancelling_with_timeout_reason(
    router, registry, db
):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(spec(task_id="t1"), "m1")
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    # first_progress would also fire, but lease timeout is later (timeout_seconds=2)
    # Progress once to clear first_progress deadline, then wait for lease.
    await router.on_progress("t1", "still alive")
    await asyncio.sleep(2.1)
    await router.tick_timeouts()
    task = await db.get_task("t1")
    assert task.status == "cancelling"
    assert task.summary == "timeout"


@pytest.mark.asyncio
async def test_execution_lease_timeout_grace_expires_to_failed(router, registry):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(spec(task_id="t1"), "m1")
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    await router.on_progress("t1", "still alive")
    # Lease timeout enters cancelling; grace is 1s.
    await asyncio.sleep(2.1)
    await router.tick_timeouts()
    assert (await router.get_status("t1")) == "cancelling"
    await asyncio.sleep(1.1)
    await router.tick_timeouts()
    assert (await router.get_status("t1")) == "failed"


# ---------------------------------------------------------------------------
# Redispatch
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_redispatch_lost_when_allow_redispatch_and_attempts_remain(
    router, registry
):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(spec(task_id="t1"), "m1", allow_redispatch=True)
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    await router.on_complete("t1", status="lost", summary="gone")
    r = await router.dispatch_task(spec(task_id="t1"), "m1", allow_redispatch=True)
    assert r.idempotent_hit is False
    assert r.status == "pending"
    assert r.attempt == 1  # consumed one attempt, not incremented again


@pytest.mark.asyncio
async def test_completed_not_redispatched(router, registry):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(spec(task_id="t1"), "m1", allow_redispatch=True)
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    await router.on_complete("t1", status="completed")
    r = await router.dispatch_task(spec(task_id="t1"), "m1", allow_redispatch=True)
    assert r.idempotent_hit is True
    assert r.status == "completed"


@pytest.mark.asyncio
async def test_redispatch_exhausted_attempts_stays_terminal(router, registry):
    await _announce_poll_worker(registry, "w1")
    s = spec(task_id="t1", max_attempts=1)
    await router.dispatch_task(s, "m1", allow_redispatch=True)
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    await router.on_complete("t1", status="lost")
    r = await router.dispatch_task(spec(task_id="t1"), "m1", allow_redispatch=True)
    assert r.idempotent_hit is True
    assert r.status == "lost"


# ---------------------------------------------------------------------------
# Cancel
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_cancel_pending_immediately(router):
    await router.dispatch_task(spec(task_id="t1"), "m1")
    await router.on_cancel("t1", reason="no longer needed")
    assert (await router.get_status("t1")) == "cancelled"


@pytest.mark.asyncio
async def test_cancel_running_hits_grace_then_cancelled(router, registry):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(spec(task_id="t1"), "m1")
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    await router.on_cancel("t1", reason="stop")
    assert (await router.get_status("t1")) == "cancelling"
    await asyncio.sleep(1.1)
    await router.tick_timeouts()
    assert (await router.get_status("t1")) == "cancelled"


@pytest.mark.asyncio
async def test_cancel_running_worker_settles_first(router, registry):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(spec(task_id="t1"), "m1")
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    await router.on_cancel("t1", reason="stop")
    # Worker reports completion before grace expires.
    await router.on_complete("t1", status="completed", summary="done anyway")
    assert (await router.get_status("t1")) == "completed"


# ---------------------------------------------------------------------------
# Batch dispatch
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_dispatch_task_batch_treats_tasks_independent(router, registry):
    await _announce_poll_worker(registry, "w1", max_concurrent=2)
    specs = [spec(task_id="b1-t1"), spec(task_id="b1-t2")]
    resp = await router.dispatch_task_batch(
        specs,
        batch_id="batch-1",
        master_session_id="m1",
        callback_topic="batch-topic",
        allow_redispatch=False,
    )
    assert isinstance(resp, BatchDispatchResponse)
    assert resp.batch_id == "batch-1"
    assert len(resp.tasks) == 2
    assert resp.idempotent_hit is False
    # Both claimable immediately (independent).
    claimed = await router.atomic_claim_for_poll("w1", max_tasks=2)
    assert {c.task_id for c in claimed} == {"b1-t1", "b1-t2"}


@pytest.mark.asyncio
async def test_batch_idempotent_on_tasks(router):
    specs = [spec(task_id="b2-t1")]
    r1 = await router.dispatch_task_batch(
        specs, batch_id="batch-2", master_session_id="m1", callback_topic="bt"
    )
    r2 = await router.dispatch_task_batch(
        specs, batch_id="batch-2", master_session_id="m1", callback_topic="bt"
    )
    assert r2.idempotent_hit is True
    assert r2.tasks[0].status == r1.tasks[0].status


@pytest.mark.asyncio
async def test_batch_idempotent_exact_redispatch_returns_existing(router, db):
    specs = [spec(task_id="b3-t1"), spec(task_id="b3-t2", goal="other")]
    r1 = await router.dispatch_task_batch(
        specs, batch_id="batch-3", master_session_id="m1", callback_topic="bt"
    )
    r2 = await router.dispatch_task_batch(
        specs, batch_id="batch-3", master_session_id="m1", callback_topic="bt"
    )
    assert r1.idempotent_hit is False
    assert r2.idempotent_hit is True
    assert {t.task_id for t in r2.tasks} == {"b3-t1", "b3-t2"}
    batch = await db.get_batch("batch-3")
    assert batch is not None
    assert batch.batch_spec_hash


@pytest.mark.asyncio
async def test_batch_redispatch_with_different_spec_not_idempotent(router):
    specs1 = [spec(task_id="b4-t1")]
    await router.dispatch_task_batch(
        specs1, batch_id="batch-4", master_session_id="m1", callback_topic="bt"
    )
    specs2 = [spec(task_id="b4-t1", goal="changed")]
    with pytest.raises(TaskRouterError):
        await router.dispatch_task_batch(
            specs2, batch_id="batch-4", master_session_id="m1", callback_topic="bt"
        )


@pytest.mark.asyncio
async def test_batch_stores_policy_json(router, db):
    policy = json.dumps({"completion_mode": "ALL", "fail_fast": True})
    specs = [spec(task_id="b5-t1")]
    await router.dispatch_task_batch(
        specs,
        batch_id="batch-5",
        master_session_id="m1",
        callback_topic="bt",
        policy_json=policy,
    )
    batch = await db.get_batch("batch-5")
    assert batch.policy_json == policy


@pytest.mark.asyncio
async def test_batch_rejects_dependency_cycle(router):
    specs = [
        spec(task_id="c1", depends_on=["c2"]),
        spec(task_id="c2", depends_on=["c1"]),
    ]
    with pytest.raises(TaskRouterError):
        await router.dispatch_task_batch(
            specs, batch_id="batch-c", master_session_id="m1", callback_topic="bt"
        )


# ---------------------------------------------------------------------------
# Status vocabulary / guards
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_status_vocabulary_rejects_invalid_transition(router, registry):
    await _announce_poll_worker(registry, "w1")
    await router.dispatch_task(spec(task_id="t1"), "m1")
    await router.atomic_claim_for_poll("w1", max_tasks=1)
    with pytest.raises(TaskRouterError):
        await router.on_complete("t1", status="bogus")
