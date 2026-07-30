"""End-to-end tests for the Mode A worker client (stub backend)."""

from __future__ import annotations

import asyncio

import pytest
import pytest_asyncio

from extend.task_relay.hub.auth import Auth
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.models import TaskSpec
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.hub.ws_server import serve_ws
from extend.task_relay.worker.backends.stub_backend import StubBackend, StubBackendConfig
from extend.task_relay.worker.task_worker import TaskWorker

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


def _spec(
    task_id: str = "t1",
    goal: str = "g",
    callback_topic: str = "topic-1",
) -> TaskSpec:
    return TaskSpec(task_id=task_id, goal=goal, callback_topic=callback_topic)


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


@pytest.fixture
def backend():
    return StubBackend(StubBackendConfig(sleep_seconds=0.05))


@pytest.mark.asyncio
async def test_worker_stub_backend_executes_task_to_completion(
    router, registry, db, backend
):
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
    try:
        ws_url = f"ws://127.0.0.1:{server.sockets[0].getsockname()[1]}"
        jwt = make_worker_jwt("w1", max_concurrent=1)

        await router.dispatch_task(
            _spec(task_id="t1", goal="hello worker", callback_topic="c1"),
            "m1",
        )

        worker = TaskWorker(
            worker_id="w1",
            relay_url=ws_url,
            jwt=jwt,
            backend=backend,
            poll_wait_ms=500,
        )

        async def stop_after_task():
            for _ in range(50):
                status = await router.get_status("t1")
                if status in {"completed", "failed", "cancelled", "lost"}:
                    break
                await asyncio.sleep(0.05)
            await worker.shutdown()

        await asyncio.gather(worker.run(), stop_after_task())

        task = await db.get_task("t1")
        assert task.status == "completed"
        assert task.summary is not None
        assert "hello worker" in task.summary
    finally:
        server.close()
        await server.wait_closed()
