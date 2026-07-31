"""Two-step poll offer / claim / nack tests (optional prefer_atomic_claim: false)."""

from __future__ import annotations

import json

import pytest
import pytest_asyncio
import websockets

pytestmark = pytest.mark.python_hub

from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.bootstrap import start_ws_server
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.tests.conftest import SECRET, make_auth, make_task_spec, make_worker_jwt
from extend.task_relay.tests.test_ws_poll import (
    announce_msg,
    jsonrpc_request,
    poll_msg,
    recv_result,
)


async def _announce(registry, worker_id: str):
    await registry.announce(
        worker_id=worker_id,
        session_modes="A",
        toolsets=["terminal"],
        max_concurrent=2,
    )


@pytest.mark.asyncio
async def test_two_step_offer_returns_preview_without_context(router, registry, db):
    await _announce(registry, "w1")
    spec = make_task_spec(
        task_id="ts1",
        goal="analyze nginx 5xx on web-1",
        params_json=json.dumps({"prefer_atomic_claim": False}),
        context_json=json.dumps({"inline": "secret payload"}),
        toolsets_json=json.dumps(["terminal"]),
    )
    await router.dispatch_task(spec, "m1")

    offered = await router.offer_tasks_for_poll("w1", 1)
    assert len(offered) == 1
    assert offered[0].task_id == "ts1"

    task = await db.get_task("ts1")
    assert task.status == "pending"
    assert task.claim_token == offered[0].claim_token

    claimed = await router.claim_offered_task("ts1", "w1", offered[0].claim_token)
    assert claimed is not None
    assert claimed.task_id == "ts1"
    task = await db.get_task("ts1")
    assert task.status == "running"
    assert task.worker_id == "w1"


@pytest.mark.asyncio
async def test_two_step_first_claim_wins_second_fails(router, registry, db):
    await _announce(registry, "w1")
    await _announce(registry, "w2")
    await router.dispatch_task(make_task_spec(task_id="ts2", goal="work"), "m1")

    offer = await router.offer_tasks_for_poll("w1", 1)
    assert len(offer) == 1
    token = offer[0].claim_token

    ok = await router.claim_offered_task("ts2", "w1", token)
    assert ok is not None
    fail = await router.claim_offered_task("ts2", "w2", token)
    assert fail is None


@pytest.mark.asyncio
async def test_two_step_nack_releases_offer(router, registry, db):
    await _announce(registry, "w1")
    await router.dispatch_task(make_task_spec(task_id="ts3", goal="work"), "m1")

    offer = await router.offer_tasks_for_poll("w1", 1)
    assert len(offer) == 1
    released = await router.release_offer("ts3", offer[0].claim_token)
    assert released is True

    task = await db.get_task("ts3")
    assert task.claim_token is None
    assert task.status == "pending"

    claimed = await router.atomic_claim_for_poll("w1", 1)
    assert [c.task_id for c in claimed] == ["ts3"]


@pytest.mark.asyncio
async def test_active_offer_blocks_atomic_claim(router, registry, db):
    await _announce(registry, "w1")
    await router.dispatch_task(make_task_spec(task_id="ts4", goal="work"), "m1")

    offer = await router.offer_tasks_for_poll("w1", 1)
    assert len(offer) == 1

    blocked = await router.atomic_claim_for_poll("w1", 1)
    assert blocked == []


@pytest.mark.asyncio
async def test_expired_offer_cleared_by_timeout_tick(router, registry, db):
    cfg = HubConfig(jwt_secret=SECRET, poll_offer_seconds=1)
    bus = EventBus(db, cfg)
    registry = WorkerRegistry(db)
    router = TaskRouter(db, bus, cfg, registry)
    await _announce(registry, "w1")
    await router.dispatch_task(make_task_spec(task_id="ts5", goal="work"), "m1")

    offer = await router.offer_tasks_for_poll("w1", 1)
    assert len(offer) == 1

    import asyncio

    await asyncio.sleep(1.1)
    await router.tick_timeouts()

    task = await db.get_task("ts5")
    assert task.claim_token is None
    claimed = await router.atomic_claim_for_poll("w1", 1)
    assert [c.task_id for c in claimed] == ["ts5"]


@pytest_asyncio.fixture
async def ws_hub(tmp_path):
    conn = await open_db(str(tmp_path / "two_step.db"))
    cfg = HubConfig(jwt_secret=SECRET, poll_offer_seconds=30)
    bus = EventBus(conn, cfg)
    registry = WorkerRegistry(conn)
    router = TaskRouter(conn, bus, cfg, registry)
    auth = make_auth()
    server = await start_ws_server(
        router, auth, registry, conn, cfg, host="127.0.0.1", port=0
    )
    url = f"ws://127.0.0.1:{server.sockets[0].getsockname()[1]}"
    jwt = make_worker_jwt("w1", allowed_toolsets=["terminal"], max_concurrent=1)
    yield router, registry, url, jwt
    server.close()
    await server.wait_closed()
    await conn.close()


@pytest.mark.asyncio
async def test_ws_two_step_poll_claim_flow(ws_hub):
    router, registry, url, jwt = ws_hub
    await _announce(registry, "w1")
    await router.dispatch_task(
        make_task_spec(
            task_id="ws-ts1",
            goal="preview only goal",
            context_json=json.dumps({"inline": "hidden"}),
            toolsets_json=json.dumps(["terminal"]),
        ),
        "m1",
    )

    async with websockets.connect(
        url, additional_headers={"Authorization": f"Bearer {jwt}"}
    ) as ws:
        await ws.send(announce_msg(worker_id="w1", toolsets=["terminal"]))
        await recv_result(ws)

        poll = jsonrpc_request(
            2,
            "worker.poll",
            {"max_wait_ms": 500, "max_tasks": 1, "prefer_atomic_claim": False},
        )
        await ws.send(poll)
        result = await recv_result(ws)
        assert result["offered"] is True
        task_info = result["tasks"][0]
        assert task_info["claimed"] is False
        assert "preview" in task_info
        assert "run" not in task_info
        assert "secret" not in json.dumps(task_info)

        claim = jsonrpc_request(
            3,
            "worker.claim",
            {"task_id": "ws-ts1", "claim_token": task_info["claim_token"]},
        )
        await ws.send(claim)
        claim_result = await recv_result(ws)
        assert claim_result["claimed"] is True
        assert claim_result["run"]["goal"] == "preview only goal"
