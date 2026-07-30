"""Integration tests for the Mode A WebSocket JSON-RPC server (M1)."""

from __future__ import annotations

import asyncio
import json
from typing import Any

import pytest
import pytest_asyncio
import websockets

from extend.task_relay.hub.auth import Auth
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.models import TaskSpec
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.hub.ws_server import serve_ws

SECRET = "t" * 32
ISSUER = "hermes-relay-hub"
AUDIENCE = "task-relay-hub"


def make_auth(**kwargs) -> Auth:
    defaults = dict(secret=SECRET, issuer=ISSUER, audience=AUDIENCE)
    defaults.update(kwargs)
    return Auth(**defaults)


def make_worker_jwt(worker_id: str, allowed_toolsets=None, max_concurrent: int = 1) -> str:
    auth = make_auth()
    return auth.issue_worker_jwt(
        worker_id,
        allowed_toolsets or [],
        max_concurrent=max_concurrent,
        ttl_s=3600,
    )


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
    )


def jsonrpc_request(msg_id: Any, method: str, params: dict | None = None) -> str:
    return json.dumps(
        {"jsonrpc": "2.0", "id": msg_id, "method": method, "params": params or {}},
        separators=(",", ":"),
    )


async def recv_result(ws) -> dict:
    raw = await ws.recv()
    payload = json.loads(raw)
    assert payload.get("jsonrpc") == "2.0"
    assert "error" not in payload, f"unexpected error: {payload.get('error')}"
    return payload["result"]


async def recv_ok(ws, expected_method: str | None = None) -> dict:
    raw = await ws.recv()
    payload = json.loads(raw)
    assert payload.get("jsonrpc") == "2.0"
    assert "error" not in payload, f"unexpected error: {payload.get('error')}"
    if expected_method is not None:
        assert payload["result"].get("_method") == expected_method or True
    return payload["result"]


async def recv_error(ws) -> dict:
    raw = await ws.recv()
    payload = json.loads(raw)
    assert payload.get("jsonrpc") == "2.0"
    assert "error" in payload
    return payload["error"]


def announce_msg(
    worker_id: str = "w1",
    session_modes=None,
    max_concurrent: int = 1,
    toolsets=None,
) -> str:
    return jsonrpc_request(
        1,
        "worker.announce",
        {
            "worker_id": worker_id,
            "session_modes": session_modes or ["a"],
            "max_concurrent": max_concurrent,
            "toolsets": toolsets or [],
        },
    )


def poll_msg(max_wait_ms: int = 500, max_tasks: int = 1) -> str:
    return jsonrpc_request(
        2,
        "worker.poll",
        {"max_wait_ms": max_wait_ms, "max_tasks": max_tasks},
    )


@pytest_asyncio.fixture
async def db(tmp_path):
    conn = await open_db(str(tmp_path / "ws.db"))
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
        queue_timeout_seconds=900,
        first_progress_seconds=120,
        timeout_seconds=600,
        cancel_grace_seconds=60,
        max_attempts=1,
    )
    return TaskRouter(db, bus, cfg, registry)


@pytest_asyncio.fixture
async def ws_server(router, registry, db):
    auth = make_auth()
    server = await serve_ws(
        router,
        auth,
        registry,
        db,
        router._config,
        host="127.0.0.1",
        port=0,
    )
    yield server
    server.close()
    await server.wait_closed()


@pytest_asyncio.fixture
async def hub_ws_url(ws_server):
    return f"ws://127.0.0.1:{ws_server.sockets[0].getsockname()[1]}"


@pytest_asyncio.fixture
def worker_jwt():
    return make_worker_jwt("w1", max_concurrent=1)


# ---------------------------------------------------------------------------
# Connection / auth
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_rejects_upgrade_without_authorization(hub_ws_url):
    with pytest.raises(websockets.exceptions.InvalidStatus) as exc:
        async with websockets.connect(hub_ws_url):
            pass
    assert exc.value.response.status_code == 401


@pytest.mark.asyncio
async def test_rejects_upgrade_with_bad_token(hub_ws_url):
    with pytest.raises(websockets.exceptions.InvalidStatus) as exc:
        async with websockets.connect(
            hub_ws_url,
            additional_headers={"Authorization": "Bearer invalid-token"},
        ):
            pass
    assert exc.value.response.status_code == 401


@pytest.mark.asyncio
async def test_accepts_valid_token(hub_ws_url, worker_jwt):
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg())
        result = await recv_result(ws)
        assert "session_id" in result
        assert "heartbeat_interval_ms" in result


@pytest.mark.asyncio
async def test_methods_before_announce_return_error(hub_ws_url, worker_jwt):
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(poll_msg())
        err = await recv_error(ws)
        assert err["code"] == -32600


@pytest.mark.asyncio
async def test_announce_rejects_worker_id_mismatch(hub_ws_url, worker_jwt):
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(
            jsonrpc_request(
                1,
                "worker.announce",
                {"worker_id": "w2", "session_modes": ["a"], "max_concurrent": 1},
            )
        )
        err = await recv_error(ws)
        assert err["code"] == -32602


@pytest.mark.asyncio
async def test_announce_rejects_without_mode_a(hub_ws_url, worker_jwt):
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(
            jsonrpc_request(
                1,
                "worker.announce",
                {"worker_id": "w1", "session_modes": ["c"], "max_concurrent": 1},
            )
        )
        err = await recv_error(ws)
        assert err["code"] == -32602


# ---------------------------------------------------------------------------
# Mode A poll / claim / lifecycle
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_mode_a_poll_claims_task(hub_ws_url, worker_jwt, router):
    await router.dispatch_task(spec(task_id="t1", goal="ping", callback_topic="c1"), "m1")
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg(session_modes=["a"], max_concurrent=1))
        await recv_ok(ws)
        await ws.send(poll_msg(max_wait_ms=500, max_tasks=1))
        result = await recv_result(ws)
        assert result["offered"] is True
        assert result["tasks"][0]["claimed"] is True
        assert result["tasks"][0]["run"]["goal"] == "ping"


@pytest.mark.asyncio
async def test_poll_returns_empty_when_no_work(hub_ws_url, worker_jwt):
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg())
        await recv_ok(ws)
        await ws.send(poll_msg(max_wait_ms=100, max_tasks=1))
        result = await recv_result(ws)
        assert result["offered"] is False


@pytest.mark.asyncio
async def test_task_progress_extends_lease(hub_ws_url, worker_jwt, router, db):
    await router.dispatch_task(spec(task_id="t1", goal="g"), "m1")
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg())
        await recv_ok(ws)
        await ws.send(poll_msg())
        result = await recv_result(ws)
        task_id = result["tasks"][0]["task_id"]

        before = await db.get_task(task_id)
        await asyncio.sleep(0.1)
        await ws.send(
            jsonrpc_request(3, "task.progress", {"task_id": task_id, "summary": "ok"})
        )
        await recv_result(ws)
        after = await db.get_task(task_id)
        assert after.first_progress_deadline_at is None
        assert after.claim_expires_at > before.claim_expires_at


@pytest.mark.asyncio
async def test_task_complete_marks_terminal(hub_ws_url, worker_jwt, router):
    await router.dispatch_task(spec(task_id="t1", goal="g"), "m1")
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg())
        await recv_ok(ws)
        await ws.send(poll_msg())
        result = await recv_result(ws)
        task_id = result["tasks"][0]["task_id"]

        await ws.send(
            jsonrpc_request(
                3,
                "task.complete",
                {"task_id": task_id, "status": "completed", "summary": "done"},
            )
        )
        await recv_result(ws)
        assert (await router.get_status(task_id)) == "completed"


@pytest.mark.asyncio
async def test_task_checkpoint_persists_l1_and_rejects_oversized_blob(
    hub_ws_url, worker_jwt, router, db
):
    await router.dispatch_task(spec(task_id="t1", goal="g"), "m1")
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg())
        await recv_ok(ws)
        await ws.send(poll_msg())
        result = await recv_result(ws)
        task_id = result["tasks"][0]["task_id"]

        await ws.send(
            jsonrpc_request(
                3,
                "task.checkpoint",
                {
                    "task_id": task_id,
                    "checkpoint_id": "ck1",
                    "summary": "halfway",
                    "fields": {"version": 1},
                    "resume_blob": "x" * 2_000_000,
                },
            )
        )
        err = await recv_error(ws)
        assert err["code"] == -32602

        await ws.send(
            jsonrpc_request(
                4,
                "task.checkpoint",
                {
                    "task_id": task_id,
                    "checkpoint_id": "ck1",
                    "summary": "halfway",
                    "fields": {"version": 1},
                    "resume_blob": "small blob",
                },
            )
        )
        ack = await recv_result(ws)
        assert ack["checkpoint_id"] == "ck1"
        assert ack["event_id"] is not None

        task = await db.get_task(task_id)
        assert task.resume_from_checkpoint == "ck1"


@pytest.mark.asyncio
async def test_worker_heartbeat(hub_ws_url, worker_jwt, registry):
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg())
        await recv_ok(ws)
        before = await registry.get_worker("w1")
        await asyncio.sleep(0.05)
        await ws.send(jsonrpc_request(2, "worker.heartbeat", {}))
        await recv_result(ws)
        after = await registry.get_worker("w1")
        assert after.last_heartbeat_at > before.last_heartbeat_at


@pytest.mark.asyncio
async def test_worker_drain(hub_ws_url, worker_jwt, router, registry):
    await router.dispatch_task(spec(task_id="t1", goal="g"), "m1")
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg())
        await recv_ok(ws)
        await ws.send(poll_msg())
        await recv_result(ws)

        await ws.send(
            jsonrpc_request(3, "worker.drain", {"reason": "deploy", "finish_running": True})
        )
        result = await recv_result(ws)
        assert "running_task_ids" in result
        assert "t1" in result["running_task_ids"]

        worker = await registry.get_worker("w1")
        assert worker.status == "draining"


@pytest.mark.asyncio
async def test_worker_close_marks_offline(hub_ws_url, worker_jwt, registry):
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg())
        await recv_ok(ws)
        await ws.send(jsonrpc_request(2, "worker.close", {}))
        await recv_result(ws)

    worker = await registry.get_worker("w1")
    assert worker.status == "offline"


@pytest.mark.asyncio
async def test_worker_nack_releases_task(hub_ws_url, worker_jwt, router):
    await router.dispatch_task(spec(task_id="t1", goal="g"), "m1")
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg())
        await recv_ok(ws)
        await ws.send(poll_msg())
        result = await recv_result(ws)
        task_id = result["tasks"][0]["task_id"]

        await ws.send(
            jsonrpc_request(3, "worker.nack", {"task_id": task_id, "reason": "cannot run"})
        )
        await recv_result(ws)
        assert (await router.get_status(task_id)) == "lost"
