"""Go Hub E2E: batch DAG, fail-fast, min_resources, aggregate."""

from __future__ import annotations

import asyncio
import json

import pytest

pytest_plugins = ["extend.task_relay.tests.go_hub_runner"]

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.tests.go_hub_e2e import (
    dispatch_batch,
    task_spec,
    ws_complete,
    ws_connect,
    ws_poll,
)
from extend.task_relay.tests.go_hub_runner import GoHubRunner
from extend.task_relay.tests.live_hub import bearer_metadata, query_sql, read_task_row


@pytest.mark.asyncio
async def test_go_hub_dag_blocks_until_dependency_completes(go_hub: GoHubRunner):
    await dispatch_batch(
        go_hub.live,
        "dag-go-1",
        [
            task_spec("dag-a1", "first"),
            task_spec("dag-a2", "second", depends_on=["dag-a1"]),
        ],
    )
    ws, _ = await ws_connect(go_hub.live, "w1", max_concurrent=2)
    try:
        poll1 = await ws_poll(ws)
        assert poll1.get("offered") is True
        claimed = poll1["tasks"][0]
        assert claimed["task_id"] == "dag-a1"
        await ws_complete(ws, "dag-a1")
        poll2 = await ws_poll(ws, msg_id=3)
        assert poll2.get("offered") is True
        assert poll2["tasks"][0]["task_id"] == "dag-a2"
    finally:
        await ws.close()


@pytest.mark.asyncio
async def test_go_hub_dependency_failure_cancels_child(go_hub: GoHubRunner):
    await dispatch_batch(
        go_hub.live,
        "dag-go-2",
        [
            task_spec("dag-b1", "root"),
            task_spec("dag-b2", "child", depends_on=["dag-b1"]),
        ],
    )
    ws, _ = await ws_connect(go_hub.live, "w1")
    try:
        poll = await ws_poll(ws)
        assert poll["tasks"][0]["task_id"] == "dag-b1"
        await ws_complete(ws, "dag-b1", status="failed", summary="boom")
        for _ in range(40):
            child = read_task_row(go_hub.live, "dag-b2")
            if child["status"] == "cancelled":
                break
            await asyncio.sleep(0.05)
        child = read_task_row(go_hub.live, "dag-b2")
        assert child["status"] == "cancelled"
        assert "dependency" in (child.get("error") or "").lower()
    finally:
        await ws.close()


@pytest.mark.asyncio
async def test_go_hub_fail_fast_cancels_sibling(go_hub: GoHubRunner):
    policy = pb.BatchPolicy(fail_fast=True)
    await dispatch_batch(
        go_hub.live,
        "ff-go-1",
        [task_spec("ff-1", "one"), task_spec("ff-2", "two")],
        policy=policy,
    )
    ws, _ = await ws_connect(go_hub.live, "w1")
    try:
        poll = await ws_poll(ws)
        task_id = poll["tasks"][0]["task_id"]
        await ws_complete(ws, task_id, status="failed", summary="fail")
        for _ in range(40):
            sibling = read_task_row(go_hub.live, "ff-1" if task_id == "ff-2" else "ff-2")
            if sibling["status"] == "cancelled":
                break
            await asyncio.sleep(0.05)
        other_id = "ff-2" if task_id == "ff-1" else "ff-1"
        assert read_task_row(go_hub.live, other_id)["status"] == "cancelled"
    finally:
        await ws.close()


@pytest.mark.asyncio
async def test_go_hub_min_resources_routes_to_capable_worker(go_hub: GoHubRunner):
    small_ws, _ = await ws_connect(
        go_hub.live, "small", resources={"cpu_cores": 1, "memory_gb": 4}
    )
    big_ws, _ = await ws_connect(
        go_hub.live, "big", resources={"cpu_cores": 8, "memory_gb": 32}
    )
    try:
        stub_req = task_spec(
            "res-1",
            "heavy",
            min_cpu_cores=4,
            min_memory_gb=16,
        )
        stub = TaskRelayStub(go_hub.grpc_channel)
        await stub.DispatchTask(
            pb.DispatchTaskRequest(spec=stub_req, master_session_id="m1"),
            metadata=bearer_metadata(go_hub.master_jwt),
        )
        small_poll = await ws_poll(small_ws, msg_id=10)
        assert small_poll.get("offered") is False
        big_poll = await ws_poll(big_ws, msg_id=11)
        assert big_poll.get("offered") is True
        assert big_poll["tasks"][0]["task_id"] == "res-1"
    finally:
        await small_ws.close()
        await big_ws.close()


@pytest.mark.asyncio
async def test_go_hub_aggregate_emitted_on_batch_complete(go_hub: GoHubRunner):
    await dispatch_batch(
        go_hub.live,
        "agg-go-1",
        [
            task_spec("agg-1", "a", aggregate_key="group"),
            task_spec("agg-2", "b", aggregate_key="group"),
        ],
    )
    ws, _ = await ws_connect(go_hub.live, "w1", max_concurrent=2)
    try:
        for tid in ("agg-1", "agg-2"):
            poll = await ws_poll(ws, msg_id=2 if tid == "agg-1" else 3)
            assert poll["tasks"][0]["task_id"] == tid
            await ws_complete(ws, tid, msg_id=4 if tid == "agg-1" else 5)
        rows = query_sql(
            go_hub.live,
            "SELECT kind FROM task_events WHERE batch_id = ? AND kind = 'AGGREGATE'",
            ("agg-go-1",),
        )
        assert rows
    finally:
        await ws.close()


@pytest.mark.asyncio
async def test_go_hub_completion_mode_any_cancels_siblings(go_hub: GoHubRunner):
    policy = pb.BatchPolicy(
        completion_mode=pb.BatchPolicy.CompletionMode.COMPLETION_MODE_ANY,
    )
    await dispatch_batch(
        go_hub.live,
        "any-go-1",
        [
            task_spec("any-1", "one"),
            task_spec("any-2", "two"),
            task_spec("any-3", "three"),
        ],
        policy=policy,
    )
    ws, _ = await ws_connect(go_hub.live, "w1", max_concurrent=3)
    try:
        poll = await ws_poll(ws)
        task_id = poll["tasks"][0]["task_id"]
        await ws_complete(ws, task_id, status="completed", summary="done")
        for tid in ("any-1", "any-2", "any-3"):
            if tid == task_id:
                continue
            for _ in range(40):
                if read_task_row(go_hub.live, tid)["status"] == "cancelled":
                    break
                await asyncio.sleep(0.05)
            assert read_task_row(go_hub.live, tid)["status"] == "cancelled"
    finally:
        await ws.close()


@pytest.mark.asyncio
async def test_go_hub_completion_mode_threshold_cancels_remaining(go_hub: GoHubRunner):
    policy = pb.BatchPolicy(
        completion_mode=pb.BatchPolicy.CompletionMode.COMPLETION_MODE_THRESHOLD,
        success_threshold=2,
    )
    await dispatch_batch(
        go_hub.live,
        "th-go-1",
        [
            task_spec("th-1", "one"),
            task_spec("th-2", "two"),
            task_spec("th-3", "three"),
        ],
        policy=policy,
    )
    ws, _ = await ws_connect(go_hub.live, "w1", max_concurrent=3)
    try:
        poll1 = await ws_poll(ws)
        await ws_complete(ws, poll1["tasks"][0]["task_id"], status="completed", msg_id=3)
        assert read_task_row(go_hub.live, "th-3")["status"] == "pending"
        poll2 = await ws_poll(ws, msg_id=4)
        await ws_complete(ws, poll2["tasks"][0]["task_id"], status="completed", msg_id=5)
        for _ in range(40):
            if read_task_row(go_hub.live, "th-3")["status"] == "cancelled":
                break
            await asyncio.sleep(0.05)
        assert read_task_row(go_hub.live, "th-3")["status"] == "cancelled"
    finally:
        await ws.close()
