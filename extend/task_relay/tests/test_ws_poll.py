"""Integration tests for the Mode A WebSocket JSON-RPC server (M1)."""

from __future__ import annotations

import asyncio
import base64
import json
import time
from typing import Any

import pytest
import pytest_asyncio
import websockets

pytestmark = pytest.mark.python_hub

from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.models import Checkpoint, TaskSpec
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.hub.bootstrap import start_ws_server
from extend.task_relay.tests.conftest import SECRET, make_auth, make_worker_jwt

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
        assert payload["result"].get("_method") == expected_method
    return payload["result"]


async def recv_notification(ws, expected_method: str | None = None) -> dict:
    raw = await ws.recv()
    payload = json.loads(raw)
    assert payload.get("jsonrpc") == "2.0"
    assert "method" in payload
    assert "id" not in payload, "expected notification, got response"
    if expected_method is not None:
        assert payload["method"] == expected_method
    return payload["params"]


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
    server = await start_ws_server(
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
        result = await recv_ok(ws, "worker.announce_ok")
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
        await recv_ok(ws, "worker.announce_ok")
        await ws.send(poll_msg(max_wait_ms=500, max_tasks=1))
        result = await recv_ok(ws, "worker.poll_result")
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
        await recv_ok(ws, "worker.announce_ok")
        await ws.send(poll_msg(max_wait_ms=100, max_tasks=1))
        result = await recv_ok(ws, "worker.poll_result")
        assert result["offered"] is False


@pytest.mark.asyncio
async def test_task_progress_extends_lease(hub_ws_url, worker_jwt, router, db):
    await router.dispatch_task(spec(task_id="t1", goal="g"), "m1")
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg())
        await recv_ok(ws, "worker.announce_ok")
        await ws.send(poll_msg())
        result = await recv_ok(ws, "worker.poll_result")
        task_id = result["tasks"][0]["task_id"]

        before = await db.get_task(task_id)
        await asyncio.sleep(0.1)
        await ws.send(
            jsonrpc_request(3, "task.progress", {"task_id": task_id, "summary": "ok"})
        )
        await recv_ok(ws, "task.progress")
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
        await recv_ok(ws, "worker.announce_ok")
        await ws.send(poll_msg())
        result = await recv_ok(ws, "worker.poll_result")
        task_id = result["tasks"][0]["task_id"]

        await ws.send(
            jsonrpc_request(
                3,
                "task.complete",
                {"task_id": task_id, "status": "completed", "summary": "done"},
            )
        )
        await recv_ok(ws, "task.complete")
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
        await recv_ok(ws, "worker.announce_ok")
        await ws.send(poll_msg())
        result = await recv_ok(ws, "worker.poll_result")
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
        ack = await recv_ok(ws, "checkpoint.ack")
        assert ack["checkpoint_id"] == "ck1"
        assert ack["event_id"] is not None

        task = await db.get_task(task_id)
        assert task.resume_from_checkpoint == "ck1"


@pytest.mark.asyncio
async def test_poll_run_payload_base64_encodes_binary_resume_blob(
    hub_ws_url, worker_jwt, router, db
):
    """A checkpoint with an opaque binary resume_blob round-trips through task.run."""
    await router.dispatch_task(spec(task_id="t1", goal="g"), "m1")
    task = await db.get_task("t1")
    event = await db.append_event(
        callback_topic=task.callback_topic,
        task_id="t1",
        kind="CHECKPOINT",
        payload={"checkpoint_id": "ck1"},
    )
    binary_blob = b"\x00\x01\xff\xfe binary \x80 data"
    await db.insert_checkpoint(
        Checkpoint(
            checkpoint_id="ck1",
            task_id="t1",
            event_id=event.event_id,
            checkpoint_at=time.time(),
            resume_blob=binary_blob,
        )
    )
    task.resume_from_checkpoint = "ck1"
    await router._persist_task(task)

    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg())
        await recv_ok(ws, "worker.announce_ok")
        await ws.send(poll_msg())
        result = await recv_ok(ws, "worker.poll_result")
        run = result["tasks"][0]["run"]
        assert run["resume_from_checkpoint"] == "ck1"
        encoded = run["resume_blob"]
        assert isinstance(encoded, str)
        assert base64.b64decode(encoded, validate=True) == binary_blob


@pytest.mark.asyncio
async def test_worker_heartbeat(hub_ws_url, worker_jwt, registry):
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg())
        await recv_ok(ws, "worker.announce_ok")
        before = await registry.get_worker("w1")
        await asyncio.sleep(0.05)
        await ws.send(jsonrpc_request(2, "worker.heartbeat", {}))
        await recv_ok(ws, "worker.heartbeat_ok")
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
        await recv_ok(ws, "worker.announce_ok")
        await ws.send(poll_msg())
        await recv_ok(ws, "worker.poll_result")

        await ws.send(
            jsonrpc_request(3, "worker.drain", {"reason": "deploy", "finish_running": True})
        )
        result = await recv_ok(ws, "worker.drain_ok")
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
        await recv_ok(ws, "worker.announce_ok")
        await ws.send(jsonrpc_request(2, "worker.close", {}))
        await recv_ok(ws, "worker.close")

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
        await recv_ok(ws, "worker.announce_ok")
        await ws.send(poll_msg())
        result = await recv_ok(ws, "worker.poll_result")
        task_id = result["tasks"][0]["task_id"]

        await ws.send(
            jsonrpc_request(3, "worker.nack", {"task_id": task_id, "reason": "cannot run"})
        )
        await recv_ok(ws, "worker.nack")
        assert (await router.get_status(task_id)) == "lost"



# ---------------------------------------------------------------------------
# JSON-RPC framing / error codes
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_malformed_json_returns_parse_error(hub_ws_url, worker_jwt):
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send("{not valid json")
        err = await recv_error(ws)
        assert err["code"] == -32700


@pytest.mark.asyncio
async def test_unknown_method_returns_method_not_found(hub_ws_url, worker_jwt):
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(jsonrpc_request(1, "worker.nonexistent", {}))
        err = await recv_error(ws)
        assert err["code"] == -32601


@pytest.mark.asyncio
async def test_missing_method_field_returns_invalid_request(hub_ws_url, worker_jwt):
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(json.dumps({"jsonrpc": "2.0", "id": 1, "params": {}}))
        err = await recv_error(ws)
        assert err["code"] == -32600


@pytest.mark.asyncio
async def test_non_string_method_returns_invalid_request(hub_ws_url, worker_jwt):
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(json.dumps({"jsonrpc": "2.0", "id": 1, "method": 123, "params": {}}))
        err = await recv_error(ws)
        assert err["code"] == -32600


# ---------------------------------------------------------------------------
# Session mode casing and lifecycle race
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_announce_stores_session_modes_uppercase(hub_ws_url, worker_jwt, registry):
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg(session_modes=["a"]))
        await recv_ok(ws, "worker.announce_ok")

    worker = await registry.get_worker("w1")
    assert "A" in worker.session_modes
    assert worker.session_modes == "A"


@pytest.mark.asyncio
async def test_disconnect_does_not_overwrite_newer_session(
    hub_ws_url, worker_jwt, registry
):
    ws1 = await websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    )
    await ws1.send(announce_msg())
    result1 = await recv_ok(ws1, "worker.announce_ok")

    ws2 = await websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    )
    await ws2.send(announce_msg())
    result2 = await recv_ok(ws2, "worker.announce_ok")
    assert result1["session_id"] != result2["session_id"]

    await ws1.close()
    await asyncio.sleep(0.2)
    worker = await registry.get_worker("w1")
    assert worker.status == "idle"
    assert worker.online_session_id == result2["session_id"]

    await ws2.close()
    await asyncio.sleep(0.2)
    worker = await registry.get_worker("w1")
    assert worker.status == "offline"


# ---------------------------------------------------------------------------
# Push delivery
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_task_cancel_pushed_to_worker(hub_ws_url, worker_jwt, router):
    await router.dispatch_task(spec(task_id="t1", goal="g"), "m1")
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg())
        await recv_ok(ws, "worker.announce_ok")
        await ws.send(poll_msg())
        result = await recv_ok(ws, "worker.poll_result")
        task_id = result["tasks"][0]["task_id"]

        await router.on_cancel(task_id, reason="test cancel")

        params = await asyncio.wait_for(
            recv_notification(ws, "task.cancel"),
            timeout=2.0,
        )
        assert params["task_id"] == task_id
        assert params["reason"]

        await ws.send(
            jsonrpc_request(4, "cancel.ack", {"task_id": task_id})
        )
        ack = await recv_ok(ws, "cancel.ack")
        assert ack["acknowledged"] is True


@pytest.mark.asyncio
async def test_task_cancel_pushed_only_once_per_session(hub_ws_url, worker_jwt, router):
    await router.dispatch_task(spec(task_id="t1", goal="g"), "m1")
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg())
        await recv_ok(ws, "worker.announce_ok")
        await ws.send(poll_msg())
        result = await recv_ok(ws, "worker.poll_result")
        task_id = result["tasks"][0]["task_id"]

        await router.on_cancel(task_id, reason="test cancel")

        params = await asyncio.wait_for(
            recv_notification(ws, "task.cancel"),
            timeout=2.0,
        )
        assert params["task_id"] == task_id

        # Wait for the monitor to iterate again; it must not re-notify.
        await asyncio.sleep(1.2)
        with pytest.raises(asyncio.TimeoutError):
            await asyncio.wait_for(
                recv_notification(ws, "task.cancel"),
                timeout=0.3,
            )


@pytest.mark.asyncio
async def test_task_cancel_notification_resets_after_task_leaves_cancelling(
    hub_ws_url, worker_jwt, router
):
    # allow_redispatch so the task can be cancelled, settled, and re-cancelled.
    await router.dispatch_task(
        spec(task_id="t1", goal="g", callback_topic="c-topic", max_attempts=2),
        "m1",
        allow_redispatch=True,
    )
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg())
        await recv_ok(ws, "worker.announce_ok")
        await ws.send(poll_msg())
        result = await recv_ok(ws, "worker.poll_result")
        task_id = result["tasks"][0]["task_id"]

        # First cancel: task enters cancelling and the monitor pushes one notify.
        await router.on_cancel(task_id, reason="first cancel")
        params1 = await asyncio.wait_for(
            recv_notification(ws, "task.cancel"),
            timeout=2.0,
        )
        assert params1["task_id"] == task_id
        assert params1["reason"] == "first cancel"

        # Worker settles the task as lost (redispatchable terminal status).
        await router.on_complete(task_id, status="lost", summary="lost before finish")

        # Wait for the monitor loop to notice the task is no longer cancelling
        # and clear its entry from _notified_cancelling.
        await asyncio.sleep(1.2)

        # Redispatch and claim the same task_id again in the same session.
        await router.dispatch_task(
            spec(task_id=task_id, goal="g", callback_topic="c-topic", max_attempts=2),
            "m1",
            allow_redispatch=True,
        )
        await ws.send(poll_msg(max_wait_ms=500, max_tasks=1))
        result2 = await recv_ok(ws, "worker.poll_result")
        assert result2["tasks"][0]["task_id"] == task_id

        # Second cancel on the redispatched task must produce a new notification.
        await router.on_cancel(task_id, reason="second cancel")
        params2 = await asyncio.wait_for(
            recv_notification(ws, "task.cancel"),
            timeout=2.0,
        )
        assert params2["task_id"] == task_id
        assert params2["reason"] == "second cancel"


@pytest.mark.asyncio
async def test_execution_timeout_pushes_cancel_to_worker(hub_ws_url, worker_jwt, router):
    # Task-level short timeout so the lease expires quickly.
    await router.dispatch_task(
        spec(task_id="t1", goal="g", timeout_seconds=1, first_progress_seconds=10),
        "m1",
    )
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {worker_jwt}"},
    ) as ws:
        await ws.send(announce_msg())
        await recv_ok(ws, "worker.announce_ok")
        await ws.send(poll_msg())
        result = await recv_ok(ws, "worker.poll_result")
        task_id = result["tasks"][0]["task_id"]

        await asyncio.sleep(1.1)
        await router.tick_timeouts()
        assert (await router.get_status(task_id)) == "cancelling"

        params = await asyncio.wait_for(
            recv_notification(ws, "task.cancel"),
            timeout=2.0,
        )
        assert params["task_id"] == task_id
        assert "timeout" in params["reason"].lower()
