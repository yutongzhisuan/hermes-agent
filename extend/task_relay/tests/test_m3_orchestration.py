"""M3 orchestration tests: DAG, BatchPolicy, min_resources, AGGREGATE."""

from __future__ import annotations

import asyncio
import json

import pytest

pytestmark = pytest.mark.python_hub

from extend.task_relay.hub.bootstrap import wire_orchestration
from extend.task_relay.hub.models import TaskSpec, Worker
from extend.task_relay.hub.resource_scheduler import (
    sort_workers_by_load,
    sort_workers_for_task,
    worker_load_score,
    worker_meets_resources,
)
from extend.task_relay.tests.conftest import make_task_spec


def _spec(**kwargs) -> TaskSpec:
    return make_task_spec(**kwargs)


async def _announce(registry, worker_id: str, *, resources: dict | None = None):
    await registry.announce(
        worker_id=worker_id,
        session_modes="A",
        toolsets=[],
        max_concurrent=2,
        resources=resources,
    )


@pytest.mark.asyncio
async def test_dag_blocks_claim_until_dependency_completes(router, registry, db, bus):
    wire_orchestration(router, db, bus)
    await _announce(registry, "w1")
    await router.dispatch_task_batch(
        [
            _spec(task_id="a1", goal="first"),
            _spec(task_id="a2", goal="second", depends_on_json=json.dumps(["a1"])),
        ],
        batch_id="dag-1",
        master_session_id="m1",
        callback_topic="t1",
    )
    claimed = await router.atomic_claim_for_poll("w1", max_tasks=2)
    assert [c.task_id for c in claimed] == ["a1"]
    await router.on_complete("a1", status="completed", summary="done")
    claimed2 = await router.atomic_claim_for_poll("w1", max_tasks=1)
    assert [c.task_id for c in claimed2] == ["a2"]


@pytest.mark.asyncio
async def test_dependency_failure_cancels_dependent(router, registry, db, bus):
    wire_orchestration(router, db, bus)
    await _announce(registry, "w1")
    await router.dispatch_task_batch(
        [
            _spec(task_id="b1", goal="root"),
            _spec(task_id="b2", goal="child", depends_on_json=json.dumps(["b1"])),
        ],
        batch_id="dag-2",
        master_session_id="m1",
        callback_topic="t1",
    )
    await router.atomic_claim_for_poll("w1", 1)
    await router.on_complete("b1", status="failed", summary="boom", error="boom")
    child = await db.get_task("b2")
    assert child.status == "cancelled"
    assert "dependency b1 ended failed" in (child.error or "")


@pytest.mark.asyncio
async def test_fail_fast_cancels_batch_siblings(router, registry, db, bus):
    wire_orchestration(router, db, bus)
    await _announce(registry, "w1")
    policy = json.dumps({"fail_fast": True})
    await router.dispatch_task_batch(
        [
            _spec(task_id="f1", goal="one"),
            _spec(task_id="f2", goal="two"),
        ],
        batch_id="ff-1",
        master_session_id="m1",
        callback_topic="t1",
        policy_json=policy,
    )
    await router.atomic_claim_for_poll("w1", 1)
    await router.on_complete("f1", status="failed", summary="fail")
    sibling = await db.get_task("f2")
    assert sibling.status == "cancelled"


@pytest.mark.asyncio
async def test_min_resources_blocks_ineligible_worker(router, registry, db, bus):
    wire_orchestration(router, db, bus)
    await _announce(registry, "small", resources={"cpu_cores": 1, "memory_gb": 4})
    await _announce(registry, "big", resources={"cpu_cores": 8, "memory_gb": 32})
    spec = _spec(
        task_id="r1",
        goal="heavy",
        min_resources_json=json.dumps({"min_cpu_cores": 4, "min_memory_gb": 16}),
    )
    await router.dispatch_task(spec, "m1")
    small_claim = await router.atomic_claim_for_poll("small", 1)
    assert small_claim == []
    big_claim = await router.atomic_claim_for_poll("big", 1)
    assert [c.task_id for c in big_claim] == ["r1"]


@pytest.mark.asyncio
async def test_min_resources_unsatisfiable_queue_timeout_marks_lost(
    router, registry, db, bus
):
    wire_orchestration(router, db, bus)
    await _announce(registry, "small", resources={"cpu_cores": 1, "memory_gb": 4})
    spec = _spec(
        task_id="rq1",
        goal="heavy",
        queue_timeout_seconds=1,
        min_resources_json=json.dumps({"min_cpu_cores": 4, "min_memory_gb": 16}),
    )
    await router.dispatch_task(spec, "m1")
    await asyncio.sleep(1.1)
    await router.tick_timeouts()
    task = await db.get_task("rq1")
    assert task.status == "lost"


def test_sort_workers_by_load_prefers_idle_worker():
    heavy = Worker(
        worker_id="heavy",
        max_concurrent=2,
        running_tasks=2,
        load_json=json.dumps({"running_tasks": 2, "cpu_percent": 80.0}),
    )
    light = Worker(
        worker_id="light",
        max_concurrent=2,
        running_tasks=0,
        load_json=json.dumps({"running_tasks": 0, "cpu_percent": 10.0}),
    )
    assert worker_load_score(light) < worker_load_score(heavy)
    ordered = sort_workers_by_load([heavy, light])
    assert [worker.worker_id for worker in ordered] == ["light", "heavy"]


def test_sort_workers_for_task_prefers_matching_region():
    prefer = make_task_spec(
        task_id="r1",
        params_json=json.dumps({"prefer_region": "ap-southeast-1"}),
    )
    ap = Worker(
        worker_id="ap",
        max_concurrent=2,
        running_tasks=1,
        capabilities_json=json.dumps({"region": "ap-southeast-1"}),
        load_json=json.dumps({"running_tasks": 1}),
    )
    us = Worker(
        worker_id="us",
        max_concurrent=2,
        running_tasks=0,
        capabilities_json=json.dumps({"region": "us-east-1"}),
        load_json=json.dumps({"running_tasks": 0}),
    )
    ordered = sort_workers_for_task(prefer, [us, ap])
    assert [worker.worker_id for worker in ordered] == ["ap", "us"]


@pytest.mark.asyncio
async def test_aggregate_emitted_when_group_terminal(router, registry, db, bus):
    wire_orchestration(router, db, bus)
    await _announce(registry, "w1")
    await router.dispatch_task_batch(
        [
            _spec(task_id="g1", goal="a", aggregate_key="grp"),
            _spec(task_id="g2", goal="b", aggregate_key="grp"),
        ],
        batch_id="agg-1",
        master_session_id="m1",
        callback_topic="topic-agg",
    )
    await router.atomic_claim_for_poll("w1", 1)
    await router.on_complete(
        "g1",
        status="completed",
        summary="s1",
        fields_json=json.dumps({"metrics": [{"name": "tokens", "value": 1}]}),
    )
    await router.atomic_claim_for_poll("w1", 1)
    await router.on_complete("g2", status="completed", summary="s2")
    cursor = await db._conn.execute(
        "SELECT kind, payload_json FROM task_events WHERE kind = 'AGGREGATE'"
    )
    rows = await cursor.fetchall()
    assert len(rows) == 1
    payload = json.loads(rows[0]["payload_json"])
    assert payload["aggregate_key"] == "grp"
    assert set(payload["task_ids"]) == {"g1", "g2"}


@pytest.mark.asyncio
async def test_completion_mode_any_cancels_siblings(router, registry, db, bus):
    wire_orchestration(router, db, bus)
    await _announce(registry, "w1")
    policy = json.dumps({"completion_mode": "ANY"})
    await router.dispatch_task_batch(
        [
            _spec(task_id="any1", goal="one"),
            _spec(task_id="any2", goal="two"),
            _spec(task_id="any3", goal="three"),
        ],
        batch_id="any-1",
        master_session_id="m1",
        callback_topic="t1",
        policy_json=policy,
    )
    await router.atomic_claim_for_poll("w1", 1)
    await router.on_complete("any1", status="completed", summary="done")
    for task_id in ("any2", "any3"):
        task = await db.get_task(task_id)
        assert task.status == "cancelled"


@pytest.mark.asyncio
async def test_completion_mode_threshold_cancels_remaining(router, registry, db, bus):
    wire_orchestration(router, db, bus)
    await _announce(registry, "w1")
    policy = json.dumps({"completion_mode": "THRESHOLD", "success_threshold": 2})
    await router.dispatch_task_batch(
        [
            _spec(task_id="th1", goal="one"),
            _spec(task_id="th2", goal="two"),
            _spec(task_id="th3", goal="three"),
        ],
        batch_id="th-1",
        master_session_id="m1",
        callback_topic="t1",
        policy_json=policy,
    )
    await router.atomic_claim_for_poll("w1", 1)
    await router.on_complete("th1", status="completed", summary="s1")
    pending = await db.get_task("th3")
    assert pending.status == "pending"
    await router.atomic_claim_for_poll("w1", 1)
    await router.on_complete("th2", status="completed", summary="s2")
    remaining = await db.get_task("th3")
    assert remaining.status == "cancelled"


def test_worker_meets_resources_gpu_gate():
    worker = Worker(
        worker_id="w1",
        resources_json=json.dumps({"cpu_cores": 8, "memory_gb": 32, "gpu_count": 0}),
    )
    assert not worker_meets_resources(worker, {"requires_gpu": True})
    worker.resources_json = json.dumps({"gpu_count": 1})
    assert worker_meets_resources(worker, {"requires_gpu": True})
