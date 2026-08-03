"""Shared fixtures for the Task Relay test suite."""

from __future__ import annotations

import pytest
import pytest_asyncio
from grpclib.client import Channel

from extend.task_relay.hub.auth import Auth
from extend.task_relay.hub.config import BootstrapEntry, HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.grpc_server import serve_grpc
from extend.task_relay.hub.bootstrap import start_ws_server, wire_orchestration
from extend.task_relay.hub.models import TaskSpec
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.worker_registry import WorkerRegistry

SECRET = "t" * 32
ISSUER = "xhermes-relay-hub"
AUDIENCE = "task-relay-hub"


def hub_backend() -> str:
    value = __import__("os").environ.get("TASK_RELAY_HUB", __import__("os").environ.get("HUB", "python"))
    return value.strip().lower()


def is_go_hub() -> bool:
    return hub_backend() == "go"


def pytest_configure(config):
    config.addinivalue_line(
        "markers",
        "python_hub: test requires Python in-process hub (skipped when TASK_RELAY_HUB=go)",
    )


def pytest_collection_modifyitems(config, items):
    if not is_go_hub():
        return
    skip = pytest.mark.skip(
        reason="requires Python in-process hub (set TASK_RELAY_HUB=python to run)"
    )
    for item in items:
        if "python_hub" in item.keywords:
            item.add_marker(skip)


def make_auth(**kwargs) -> Auth:
    defaults = dict(secret=SECRET, issuer=ISSUER, audience=AUDIENCE)
    defaults.update(kwargs)
    return Auth(**defaults)


def make_worker_jwt(
    worker_id: str,
    allowed_toolsets: list[str] | None = None,
    max_concurrent: int = 1,
) -> str:
    return make_auth().issue_worker_jwt(
        worker_id,
        allowed_toolsets or [],
        max_concurrent=max_concurrent,
        ttl_s=3600,
    )


def make_task_spec(**kwargs) -> TaskSpec:
    """Build a :class:`TaskSpec` with sensible test defaults."""
    defaults = dict(
        task_id="t1",
        goal="test goal",
        callback_topic="topic-1",
        priority=0,
        timeout_seconds=600,
        first_progress_seconds=120,
        max_attempts=1,
    )
    defaults.update(kwargs)
    return TaskSpec(**defaults)


def make_p15_auth(**kwargs) -> Auth:
    """Auth with the bootstrap token fixture used by P1.5 HTTP token tests."""
    return make_auth(
        bootstrap_tokens={
            "boot-w1": BootstrapEntry(
                worker_id="w1",
                allowed_toolsets=("terminal",),
                max_concurrent=2,
            )
        },
        **kwargs,
    )


@pytest_asyncio.fixture
async def db(tmp_path):
    conn = await open_db(str(tmp_path / "relay.db"))
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
    router = TaskRouter(db, bus, cfg, registry)
    wire_orchestration(router, db, bus)
    return router


@pytest_asyncio.fixture
async def auth():
    return make_auth()


@pytest.fixture
def master_jwt(auth):
    return auth.issue_master_jwt("master-01", ttl_s=3600)


@pytest.fixture
def worker_jwt():
    return make_worker_jwt("w1", allowed_toolsets=[], max_concurrent=1)


@pytest_asyncio.fixture
async def grpc_server(router, auth, db, bus, registry):
    server = await serve_grpc(
        router, auth, router._config, db, bus, registry, host="127.0.0.1", port=0
    )
    yield server
    server.close()
    await server.wait_closed()


@pytest_asyncio.fixture
async def grpc_channel(grpc_server):
    port = grpc_server._server.sockets[0].getsockname()[1]
    channel = Channel(host="127.0.0.1", port=port)
    yield channel
    channel.close()


@pytest_asyncio.fixture
async def ws_server(router, registry, db, auth):
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
