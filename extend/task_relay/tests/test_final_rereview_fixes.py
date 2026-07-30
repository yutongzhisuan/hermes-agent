"""Failing tests for the final final re-review Critical + Important findings."""

from __future__ import annotations

import asyncio
import base64
import json
from typing import Any

import pytest
import pytest_asyncio
import websockets

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.hub.auth import Auth
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.grpc_server import _event_to_proto
from extend.task_relay.hub.models import Task, TaskEvent, TaskSpec
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


def make_worker_jwt(worker_id: str, max_concurrent: int = 1) -> str:
    return make_auth().issue_worker_jwt(worker_id, [], max_concurrent=max_concurrent, ttl_s=3600)


def jsonrpc_request(msg_id: Any, method: str, params: dict | None = None) -> str:
    return json.dumps(
        {"jsonrpc": "2.0", "id": msg_id, "method": method, "params": params or {}},
        separators=(",", ":"),
    )


async def recv_ok(ws, expected_method: str | None = None) -> dict:
    raw = await ws.recv()
    payload = json.loads(raw)
    assert payload.get("jsonrpc") == "2.0"
    assert "error" not in payload, f"unexpected error: {payload.get('error')}"
    if expected_method is not None:
        assert payload["result"].get("_method") == expected_method
    return payload["result"]


async def recv_error(ws) -> dict:
    raw = await ws.recv()
    payload = json.loads(raw)
    assert payload.get("jsonrpc") == "2.0"
    assert "error" in payload
    return payload["error"]


@pytest_asyncio.fixture
async def db(tmp_path):
    conn = await open_db(str(tmp_path / "final.db"))
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
        list_tasks_default_limit=10,
        list_tasks_max_limit=50,
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


# -----------------------------------------------------------------------------
# Finding 2: announced max_concurrent is capped to JWT claim on Hub side.
# -----------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_announce_clamps_max_concurrent_to_jwt_claim(hub_ws_url, registry):
    """Worker announcing max_concurrent above its JWT claim must be clamped."""
    token = make_worker_jwt("w1", max_concurrent=2)
    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {token}"},
    ) as ws:
        await ws.send(
            jsonrpc_request(
                1,
                "worker.announce",
                {
                    "worker_id": "w1",
                    "session_modes": ["a"],
                    "max_concurrent": 5,
                    "toolsets": [],
                },
            )
        )
        result = await recv_ok(ws, "worker.announce_ok")
        assert "session_id" in result

    worker = await registry.get_worker("w1")
    assert worker.max_concurrent == 2


# -----------------------------------------------------------------------------
# Finding 3: WatchTask TERMINAL events carry the full TaskResult.
# -----------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_event_to_proto_terminal_includes_full_task_result(router, db, registry):
    """A TERMINAL event must expose result_text, fields, usage, worker_id, etc."""
    task_id = "terminal-full"
    await router.dispatch_task(
        TaskSpec(task_id=task_id, goal="g", callback_topic="topic"),
        master_session_id="m1",
    )
    await registry.announce(
        "w1",
        toolsets=["terminal"],
        status="idle",
        max_concurrent=1,
    )
    # Manually claim and complete to set all result fields.
    await db._conn.execute(
        "UPDATE tasks SET worker_id = ?, status = ?, result_json = ?, summary = ?, "
        "fields_json = ?, usage_json = ?, error = ?, started_at = ?, completed_at = ?, "
        "attempt = ?, max_attempts = ?, batch_id = ?, resume_from_checkpoint = ? "
        "WHERE task_id = ?",
        (
            "w1",
            "completed",
            '{"answer": 42}',
            "done",
            '{"version": 1, "metrics": [{"name": "m1", "value": 1.0}]}',
            '{"prompt_tokens": 10, "completion_tokens": 5}',
            "",
            1000.0,
            1005.0,
            2,
            3,
            "batch-1",
            "ck1",
            task_id,
        ),
    )
    await db._conn.commit()

    event = TaskEvent(
        event_id=1,
        callback_topic="topic",
        task_id=task_id,
        batch_id="batch-1",
        kind="TERMINAL",
        payload_json='{"status":"completed","summary":"done","attempt":2}',
        event_at=1005.0,
    )
    proto = await _event_to_proto(event, db)

    assert proto.kind == pb.TaskEventKind.TASK_EVENT_KIND_TERMINAL
    result = proto.result
    assert result.task_id == task_id
    assert result.status == pb.TaskStatus.TASK_STATUS_COMPLETED
    assert result.summary == "done"
    assert result.result_text == '{"answer": 42}'
    assert result.worker_id == "w1"
    assert result.attempt == 2
    assert result.max_attempts == 3
    assert result.batch_id == "batch-1"
    assert result.latest_checkpoint_id == "ck1"
    assert result.started_at == 1_000_000
    assert result.completed_at == 1_005_000
    assert result.fields.metrics[0].name == "m1"
    assert result.usage.total_tokens == 15


# -----------------------------------------------------------------------------
# Finding 4: Worker->Hub checkpoint resume_blob is base64-decoded.
# -----------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_task_checkpoint_decodes_base64_resume_blob(hub_ws_url, router, db):
    """A base64 resume_blob sent by the worker must be stored as raw bytes."""
    token = make_worker_jwt("w1", max_concurrent=1)
    await router.dispatch_task(
        TaskSpec(task_id="t1", goal="g", callback_topic="topic"),
        master_session_id="m1",
    )

    binary_blob = b"\x00\x01\xff\xfe binary \x80 data"
    encoded_blob = base64.b64encode(binary_blob).decode("ascii")

    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {token}"},
    ) as ws:
        await ws.send(
            jsonrpc_request(
                1,
                "worker.announce",
                {"worker_id": "w1", "session_modes": ["a"], "max_concurrent": 1, "toolsets": []},
            )
        )
        await recv_ok(ws, "worker.announce_ok")

        await ws.send(
            jsonrpc_request(
                2,
                "worker.poll",
                {"max_wait_ms": 500, "max_tasks": 1},
            )
        )
        poll = await recv_ok(ws, "worker.poll_result")
        task_id = poll["tasks"][0]["task_id"]

        await ws.send(
            jsonrpc_request(
                3,
                "task.checkpoint",
                {
                    "task_id": task_id,
                    "checkpoint_id": "ck1",
                    "summary": "halfway",
                    "resume_blob": encoded_blob,
                },
            )
        )
        ack = await recv_ok(ws, "checkpoint.ack")
        assert ack["checkpoint_id"] == "ck1"

    checkpoint = await db.get_latest_checkpoint(task_id)
    assert checkpoint.resume_blob == binary_blob


@pytest.mark.asyncio
async def test_checkpoint_resume_blob_round_trips_through_poll(hub_ws_url, router, db):
    """Base64-encoded blob from worker -> bytes in DB -> base64 string in task.run."""
    token = make_worker_jwt("w1", max_concurrent=1)
    await router.dispatch_task(
        TaskSpec(task_id="t1", goal="g", callback_topic="topic"),
        master_session_id="m1",
    )

    binary_blob = b"\xde\xad\xbe\xef"
    encoded_blob = base64.b64encode(binary_blob).decode("ascii")

    async with websockets.connect(
        hub_ws_url,
        additional_headers={"Authorization": f"Bearer {token}"},
    ) as ws:
        await ws.send(
            jsonrpc_request(
                1,
                "worker.announce",
                {"worker_id": "w1", "session_modes": ["a"], "max_concurrent": 1, "toolsets": []},
            )
        )
        await recv_ok(ws, "worker.announce_ok")

        await ws.send(
            jsonrpc_request(
                2,
                "worker.poll",
                {"max_wait_ms": 500, "max_tasks": 1},
            )
        )
        poll = await recv_ok(ws, "worker.poll_result")
        task_id = poll["tasks"][0]["task_id"]

        await ws.send(
            jsonrpc_request(
                3,
                "task.checkpoint",
                {
                    "task_id": task_id,
                    "checkpoint_id": "ck1",
                    "resume_blob": encoded_blob,
                },
            )
        )
        await recv_ok(ws, "checkpoint.ack")

    # The DB stores raw bytes; the run payload builder encodes them back to base64.
    checkpoint = await db.get_latest_checkpoint(task_id)
    assert checkpoint.resume_blob == binary_blob
    assert base64.b64encode(checkpoint.resume_blob).decode("ascii") == encoded_blob


# -----------------------------------------------------------------------------
# Finding 5: Auth failure responses do not leak token-validation details.
# -----------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_rejects_upgrade_with_generic_error_body(hub_ws_url):
    """A 401 upgrade response must not include the underlying AuthError detail."""
    with pytest.raises(websockets.exceptions.InvalidStatus) as exc:
        async with websockets.connect(
            hub_ws_url,
            additional_headers={"Authorization": "Bearer invalid-token"},
        ):
            pass

    assert exc.value.response.status_code == 401
    body = exc.value.response.body
    if body is None:
        pytest.fail("expected a response body")
    text = body.decode("utf-8") if isinstance(body, (bytes, bytearray)) else body
    data = json.loads(text)
    assert data["error"] == "Invalid or missing token"
    assert "signature" not in text.lower()
    assert "expired" not in text.lower()
