"""P1.5 gap-closure tests: target_worker, token HTTP, JWT refresh, GetTaskResult checkpoint."""

from __future__ import annotations

import json
import time

import pytest
import pytest_asyncio
from aiohttp import web
from aiohttp.test_utils import TestClient, TestServer
from grpclib.client import Channel

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.hub.auth import Auth
from extend.task_relay.hub.config import BootstrapEntry, HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.grpc_server import serve_grpc
from extend.task_relay.hub.models import Checkpoint, TaskSpec
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.token_server import create_token_app
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.worker.jwt_manager import derive_token_url, ensure_worker_jwt

SECRET = "t" * 32
ISSUER = "hermes-relay-hub"
AUDIENCE = "task-relay-hub"


def make_auth(**kwargs) -> Auth:
    defaults = dict(
        secret=SECRET,
        issuer=ISSUER,
        audience=AUDIENCE,
        bootstrap_tokens={
            "boot-w1": BootstrapEntry(
                worker_id="w1",
                allowed_toolsets=("terminal",),
                max_concurrent=2,
            )
        },
    )
    defaults.update(kwargs)
    return Auth(**defaults)


@pytest_asyncio.fixture
async def db(tmp_path):
    conn = await open_db(str(tmp_path / "p15.db"))
    yield conn
    await conn.close()


@pytest_asyncio.fixture
async def router(db):
    bus = EventBus(db, HubConfig(jwt_secret=SECRET))
    registry = WorkerRegistry(db)
    return TaskRouter(db, bus, HubConfig(jwt_secret=SECRET), registry)


@pytest_asyncio.fixture
async def registry(db):
    return WorkerRegistry(db)


async def _announce(registry, worker_id: str):
    await registry.announce(worker_id, session_modes="a", toolsets=["terminal"])


@pytest.mark.asyncio
async def test_claim_respects_target_worker(router, registry):
    await _announce(registry, "w1")
    await _announce(registry, "w2")
    await router.dispatch_task(
        TaskSpec(task_id="t1", goal="pin", callback_topic="c1", target_worker="w2"),
        "m1",
    )
    assert await router.atomic_claim_for_poll("w1", max_tasks=1) == []
    claimed = await router.atomic_claim_for_poll("w2", max_tasks=1)
    assert len(claimed) == 1
    assert claimed[0].task_id == "t1"


@pytest.mark.asyncio
async def test_token_http_bootstrap_exchange():
    auth = make_auth()
    app = create_token_app(auth)
    async with TestClient(TestServer(app)) as client:
        resp = await client.post(
            "/v1/worker/token",
            json={"bootstrap_token": "boot-w1", "worker_id": "w1"},
        )
        assert resp.status == 200
        body = await resp.json()
        assert "worker_jwt" in body
        assert body["expires_at"] > time.time()
        claims = auth.verify_worker_jwt(body["worker_jwt"])
        assert claims.sub == "w1"
        assert claims.max_concurrent == 2


@pytest.mark.asyncio
async def test_token_http_refresh_existing_jwt():
    auth = make_auth()
    original = auth.issue_worker_jwt("w1", ["terminal"], max_concurrent=2, ttl_s=60)
    app = create_token_app(auth)
    async with TestClient(TestServer(app)) as client:
        resp = await client.post(
            "/v1/worker/token",
            json={"worker_jwt": original},
        )
        assert resp.status == 200
        body = await resp.json()
        refreshed = body["worker_jwt"]
        assert refreshed != original
        claims = auth.verify_worker_jwt(refreshed)
        assert claims.sub == "w1"
        assert claims.max_concurrent == 2


@pytest.mark.asyncio
async def test_derive_token_url_from_ws():
    url = derive_token_url("ws://127.0.0.1:9000/ws/worker")
    assert url == "http://127.0.0.1:9001/v1/worker/token"


@pytest.mark.asyncio
async def test_ensure_worker_jwt_exchanges_bootstrap(tmp_path):
    auth = make_auth()
    app = create_token_app(auth)
    bootstrap_file = tmp_path / "bootstrap.txt"
    bootstrap_file.write_text("boot-w1", encoding="utf-8")
    jwt_file = tmp_path / "worker.jwt"

    async with TestClient(TestServer(app)) as client:
        token_url = str(client.make_url("/v1/worker/token"))
        token = await ensure_worker_jwt(
            worker_id="w1",
            jwt_file=jwt_file,
            token_url=token_url,
            bootstrap_file=bootstrap_file,
        )
        assert auth.verify_worker_jwt(token).sub == "w1"
        assert jwt_file.read_text(encoding="utf-8").strip() == token


@pytest.mark.asyncio
async def test_get_task_result_include_latest_checkpoint(db, tmp_path):
    bus = EventBus(db, HubConfig(jwt_secret=SECRET))
    registry = WorkerRegistry(db)
    router = TaskRouter(db, bus, HubConfig(jwt_secret=SECRET), registry)
    auth = make_auth()
    master_jwt = auth.issue_master_jwt("master-1")

    await router.dispatch_task(
        TaskSpec(task_id="ckpt-task", goal="g", callback_topic="topic-ckpt"),
        "m1",
    )
    task = await db.get_task("ckpt-task")
    assert task is not None
    task.resume_from_checkpoint = "ck-1"
    await db.insert_checkpoint(
        Checkpoint(
            checkpoint_id="ck-1",
            task_id="ckpt-task",
            event_id=1,
            checkpoint_at=time.time(),
            summary="partial summary",
            fields_json=json.dumps({"version": 1, "report": "partial report"}),
            resume_blob=None,
            lease_until=None,
        )
    )
    await db._conn.execute(
        "UPDATE tasks SET resume_from_checkpoint = ? WHERE task_id = ?",
        ("ck-1", "ckpt-task"),
    )
    await db._conn.commit()

    grpc_server = await serve_grpc(
        router, auth, HubConfig(jwt_secret=SECRET), db, bus, registry, port=0
    )
    host, port = grpc_server._server.sockets[0].getsockname()[:2]
    try:
        async with Channel(host, port) as channel:
            stub = TaskRelayStub(channel)
            result = await stub.GetTaskResult(
                pb.TaskResultRequest(
                    task_id="ckpt-task",
                    include_latest_checkpoint=True,
                ),
                metadata=[("authorization", f"Bearer {master_jwt}")],
            )
            assert result.latest_checkpoint_id == "ck-1"
            assert result.summary == "partial summary"
            assert result.fields.report == "partial report"
    finally:
        grpc_server.close()
        await grpc_server.wait_closed()
