"""Go Hub E2E: queue timeout, allow_redispatch, and related lifecycle paths."""

from __future__ import annotations

import pytest
import pytest_asyncio

pytest_plugins = ["extend.task_relay.tests.go_hub_runner"]

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.tests.go_hub_e2e import (
    dispatch_task,
    task_spec,
    ws_complete,
    ws_connect,
    ws_poll,
)
from extend.task_relay.tests.go_hub_runner import GoHubRunner
from extend.task_relay.tests.live_hub import (
    HubLaunchConfig,
    read_task_row,
    start_live_hub,
    stop_live_hub,
    wait_for_task_status,
)


@pytest_asyncio.fixture
async def go_hub_fast_queue(tmp_path):
    """Go Hub with a 1s default queue timeout for timeout E2E."""
    prev = __import__("os").environ.get("TASK_RELAY_HUB")
    __import__("os").environ["TASK_RELAY_HUB"] = "go"
    try:
        live = await start_live_hub(
            tmp_path, HubLaunchConfig(queue_timeout_seconds=1, max_attempts=2)
        )
        runner = GoHubRunner(
            grpc_channel=live.grpc_channel,
            ws_url=live.ws_url,
            master_jwt=live.master_jwt,
            live=live,
        )
        try:
            yield runner
        finally:
            await stop_live_hub(live)
    finally:
        if prev is None:
            __import__("os").environ.pop("TASK_RELAY_HUB", None)
        else:
            __import__("os").environ["TASK_RELAY_HUB"] = prev


@pytest.mark.asyncio
async def test_go_hub_queue_timeout_marks_lost(go_hub_fast_queue: GoHubRunner):
    hub = go_hub_fast_queue
    await dispatch_task(
        hub.live,
        task_spec("qt-1", "wait in queue", callback_topic="qt-topic"),
    )
    status = await wait_for_task_status(hub.live, "qt-1", {"lost"}, timeout=10.0)
    assert status == "lost"
    row = read_task_row(hub.live, "qt-1")
    assert row["status"] == "lost"
    assert "queue timeout" in (row.get("summary") or "")


@pytest.mark.asyncio
async def test_go_hub_allow_redispatch_reopens_lost_task(go_hub: GoHubRunner):
    spec = task_spec("rd-1", "redispatch me", callback_topic="rd-topic", max_attempts=2)
    await dispatch_task(go_hub.live, spec, allow_redispatch=True)
    ws, _ = await ws_connect(go_hub.live, "w1")
    try:
        poll = await ws_poll(ws)
        assert poll["tasks"][0]["task_id"] == "rd-1"
        await ws_complete(ws, "rd-1", status="lost", summary="gone")
    finally:
        await ws.close()

    resp = await dispatch_task(go_hub.live, spec, allow_redispatch=True)
    assert resp.idempotent_hit is False
    assert pb.TaskStatus.Name(resp.status).endswith("PENDING")
    row = read_task_row(go_hub.live, "rd-1")
    assert row["status"] == "pending"
    assert int(row.get("allow_redispatch") or 0) == 1


@pytest.mark.asyncio
async def test_go_hub_completed_not_redispatched(go_hub: GoHubRunner):
    spec = task_spec("rd-2", "stay completed", callback_topic="rd-topic", max_attempts=2)
    await dispatch_task(go_hub.live, spec, allow_redispatch=True)
    ws, _ = await ws_connect(go_hub.live, "w1")
    try:
        poll = await ws_poll(ws)
        assert poll["tasks"][0]["task_id"] == "rd-2"
        await ws_complete(ws, "rd-2", status="completed", summary="done")
    finally:
        await ws.close()

    resp = await dispatch_task(go_hub.live, spec, allow_redispatch=True)
    assert resp.idempotent_hit is True
    assert pb.TaskStatus.Name(resp.status).endswith("COMPLETED")
    assert read_task_row(go_hub.live, "rd-2")["status"] == "completed"


@pytest.mark.asyncio
async def test_go_hub_redispatch_exhausted_attempts_stays_lost(go_hub: GoHubRunner):
    spec = task_spec("rd-3", "no more tries", callback_topic="rd-topic", max_attempts=1)
    await dispatch_task(go_hub.live, spec, allow_redispatch=True)
    ws, _ = await ws_connect(go_hub.live, "w1")
    try:
        poll = await ws_poll(ws)
        assert poll["tasks"][0]["task_id"] == "rd-3"
        await ws_complete(ws, "rd-3", status="lost", summary="gone")
    finally:
        await ws.close()

    resp = await dispatch_task(go_hub.live, spec, allow_redispatch=True)
    assert resp.idempotent_hit is True
    assert pb.TaskStatus.Name(resp.status).endswith("LOST")
    assert read_task_row(go_hub.live, "rd-3")["status"] == "lost"
