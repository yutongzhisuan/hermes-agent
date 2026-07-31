"""Hub cancel push targets the current worker session.

These tests verify that ``task.cancel`` frames are delivered only to the
worker's active WebSocket session, not a stale/superseded one still in the
Hub's session set.
"""

from __future__ import annotations

import asyncio
import json
from typing import Any

import pytest
import pytest_asyncio
import websockets

from extend.task_relay.constants import CANCEL_REASON_TIMEOUT
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.models import TaskSpec
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.hub.ws_server import WsHubServer
from extend.task_relay.tests.conftest import SECRET, make_auth, make_worker_jwt

def jsonrpc_request(msg_id: Any, method: str, params: dict | None = None) -> str:
    return json.dumps(
        {"jsonrpc": "2.0", "id": msg_id, "method": method, "params": params or {}},
        separators=(",", ":"),
    )


async def recv_response(ws: websockets.WebSocketClientProtocol) -> dict:
    raw = await ws.recv()
    payload = json.loads(raw)
    assert payload.get("jsonrpc") == "2.0"
    assert "error" not in payload, f"unexpected error: {payload.get('error')}"
    return payload["result"]


async def recv_notification(
    ws: websockets.WebSocketClientProtocol,
    expected_method: str,
) -> dict:
    raw = await ws.recv()
    payload = json.loads(raw)
    assert payload.get("jsonrpc") == "2.0"
    assert "id" not in payload, "expected notification, got response"
    assert payload.get("method") == expected_method
    return payload.get("params", {})


@pytest_asyncio.fixture
async def db(tmp_path):
    conn = await open_db(str(tmp_path / "cancel_session.db"))
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
async def hub(router, registry, db):
    return WsHubServer(router, make_auth(), registry, db, router._config)


@pytest_asyncio.fixture
async def cancel_server(hub):
    server = await hub.serve(host="127.0.0.1", port=0)
    yield server
    server.close()
    await server.wait_closed()


@pytest_asyncio.fixture
async def cancel_url(cancel_server):
    port = cancel_server.sockets[0].getsockname()[1]
    return f"ws://127.0.0.1:{port}"


@pytest.mark.asyncio
async def test_push_cancel_targets_current_session(
    cancel_url: str,
    hub: WsHubServer,
    router: TaskRouter,
):
    """push_cancel must send to the active session, not a stale one."""
    token = make_worker_jwt("w1")

    async with websockets.connect(
        cancel_url,
        additional_headers={"Authorization": f"Bearer {token}"},
    ) as ws1:
        await ws1.send(
            jsonrpc_request(
                1,
                "worker.announce",
                {"worker_id": "w1", "session_modes": ["a"], "max_concurrent": 1, "toolsets": []},
            )
        )
        await recv_response(ws1)

        await router.dispatch_task(
            TaskSpec(task_id="t1", goal="g", callback_topic="topic"),
            master_session_id="m1",
        )
        await router.atomic_claim_for_poll("w1", max_tasks=1)

        # A new connection for the same worker becomes the active session.
        async with websockets.connect(
            cancel_url,
            additional_headers={"Authorization": f"Bearer {token}"},
        ) as ws2:
            await ws2.send(
                jsonrpc_request(
                    1,
                    "worker.announce",
                    {"worker_id": "w1", "session_modes": ["a"], "max_concurrent": 1, "toolsets": []},
                )
            )
            await recv_response(ws2)

            await router.on_cancel("t1", reason=CANCEL_REASON_TIMEOUT, grace_seconds=60)

            await hub.push_cancel("t1", CANCEL_REASON_TIMEOUT, hard_deadline_at=12345.0)

            params = await asyncio.wait_for(
                recv_notification(ws2, "task.cancel"), timeout=2.0
            )
            assert params["task_id"] == "t1"
            assert params["reason"] == CANCEL_REASON_TIMEOUT

            # The stale session must not receive the cancel frame.
            with pytest.raises(asyncio.TimeoutError):
                await asyncio.wait_for(
                    recv_notification(ws1, "task.cancel"), timeout=0.3
                )


@pytest.mark.asyncio
async def test_cancel_monitor_loop_targets_current_session(
    cancel_url: str,
    router: TaskRouter,
):
    """The per-session cancel monitor must skip superseded sessions."""
    token = make_worker_jwt("w1")

    async with websockets.connect(
        cancel_url,
        additional_headers={"Authorization": f"Bearer {token}"},
    ) as ws1:
        await ws1.send(
            jsonrpc_request(
                1,
                "worker.announce",
                {"worker_id": "w1", "session_modes": ["a"], "max_concurrent": 1, "toolsets": []},
            )
        )
        await recv_response(ws1)

        await router.dispatch_task(
            TaskSpec(task_id="t1", goal="g", callback_topic="topic"),
            master_session_id="m1",
        )
        await router.atomic_claim_for_poll("w1", max_tasks=1)

        async with websockets.connect(
            cancel_url,
            additional_headers={"Authorization": f"Bearer {token}"},
        ) as ws2:
            await ws2.send(
                jsonrpc_request(
                    1,
                    "worker.announce",
                    {"worker_id": "w1", "session_modes": ["a"], "max_concurrent": 1, "toolsets": []},
                )
            )
            await recv_response(ws2)

            await router.on_cancel("t1", reason=CANCEL_REASON_TIMEOUT, grace_seconds=60)

            params = await asyncio.wait_for(
                recv_notification(ws2, "task.cancel"), timeout=2.5
            )
            assert params["task_id"] == "t1"
            assert params["reason"] == CANCEL_REASON_TIMEOUT

            with pytest.raises(asyncio.TimeoutError):
                await asyncio.wait_for(
                    recv_notification(ws1, "task.cancel"), timeout=0.3
                )
