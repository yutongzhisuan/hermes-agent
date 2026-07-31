"""M3 TLS / mTLS hardening tests."""

from __future__ import annotations

import ssl

import pytest

pytestmark = pytest.mark.python_hub

import pytest
import pytest_asyncio
from grpclib.client import Channel

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.grpc_server import serve_grpc
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.bootstrap import wire_orchestration
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.tls import TlsConfig, load_client_ssl_context, load_server_ssl_context
from extend.task_relay.hub.token_server import TOKEN_PATH, serve_token_http
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.tests.conftest import SECRET, make_auth, make_p15_auth
from extend.task_relay.tests.tls_helpers import generate_test_tls_material


def test_load_server_ssl_context_disabled():
    assert load_server_ssl_context(TlsConfig()) is None


def test_load_server_ssl_context_requires_ca_for_mtls(tmp_path):
    material = generate_test_tls_material(tmp_path)
    tls = TlsConfig(
        cert_file=str(material["server_cert"]),
        key_file=str(material["server_key"]),
        require_client_cert=True,
    )
    with pytest.raises(ValueError, match="require_client_cert requires"):
        load_server_ssl_context(tls)


def test_load_server_ssl_context_with_mtls(tmp_path):
    material = generate_test_tls_material(tmp_path)
    tls = TlsConfig(
        cert_file=str(material["server_cert"]),
        key_file=str(material["server_key"]),
        ca_file=str(material["ca"]),
        require_client_cert=True,
    )
    ctx = load_server_ssl_context(tls)
    assert isinstance(ctx, ssl.SSLContext)


@pytest_asyncio.fixture
async def tls_hub_stack(db):
    auth = make_auth()
    config = HubConfig(jwt_secret=SECRET)
    bus = EventBus(db, config)
    registry = WorkerRegistry(db)
    router = TaskRouter(db, bus, config, registry)
    wire_orchestration(router, db, bus)
    return router, auth, db, bus, registry, config


@pytest.mark.asyncio
async def test_grpc_tls_without_client_cert_rejected(tmp_path, tls_hub_stack):
    material = generate_test_tls_material(tmp_path)
    router, auth, db, bus, registry, config = tls_hub_stack
    server_ctx = load_server_ssl_context(
        TlsConfig(
            cert_file=str(material["server_cert"]),
            key_file=str(material["server_key"]),
            ca_file=str(material["ca"]),
            require_client_cert=True,
        )
    )
    server = await serve_grpc(
        router,
        auth,
        HubConfig(jwt_secret=SECRET),
        db,
        bus,
        registry,
        host="127.0.0.1",
        port=0,
        ssl=server_ctx,
    )
    port = server._server.sockets[0].getsockname()[1]

    client_ctx = ssl.create_default_context()
    client_ctx.check_hostname = False
    client_ctx.verify_mode = ssl.CERT_NONE

    channel = Channel("127.0.0.1", port, ssl=client_ctx)
    stub = TaskRelayStub(channel)
    with pytest.raises(Exception):
        await stub.ListWorkers(pb.ListWorkersRequest())
    channel.close()
    server.close()
    await server.wait_closed()


@pytest.mark.asyncio
async def test_grpc_mtls_accepts_client_cert(tmp_path, tls_hub_stack):
    material = generate_test_tls_material(tmp_path)
    router, auth, db, bus, registry, config = tls_hub_stack
    server_ctx = load_server_ssl_context(
        TlsConfig(
            cert_file=str(material["server_cert"]),
            key_file=str(material["server_key"]),
            ca_file=str(material["ca"]),
            require_client_cert=True,
        )
    )
    server = await serve_grpc(
        router,
        auth,
        HubConfig(jwt_secret=SECRET),
        db,
        bus,
        registry,
        host="127.0.0.1",
        port=0,
        ssl=server_ctx,
    )
    port = server._server.sockets[0].getsockname()[1]

    client_ctx = load_client_ssl_context(
        ca_file=str(material["ca"]),
        cert_file=str(material["client_cert"]),
        key_file=str(material["client_key"]),
        check_hostname=False,
    )
    channel = Channel("127.0.0.1", port, ssl=client_ctx)
    stub = TaskRelayStub(channel)
    master_jwt = auth.issue_master_jwt("master-1")
    response = await stub.ListWorkers(
        pb.ListWorkersRequest(),
        metadata={"authorization": f"Bearer {master_jwt}"},
    )
    assert response.workers == []
    channel.close()
    server.close()
    await server.wait_closed()


@pytest.mark.asyncio
async def test_token_http_tls(tmp_path):
    material = generate_test_tls_material(tmp_path)
    auth = make_p15_auth()
    server_ctx = load_server_ssl_context(
        TlsConfig(
            cert_file=str(material["server_cert"]),
            key_file=str(material["server_key"]),
            ca_file=str(material["ca"]),
        )
    )
    runner = await serve_token_http(auth, host="127.0.0.1", port=0, ssl_context=server_ctx)
    site = runner._sites[0]  # type: ignore[attr-defined]
    port = site._server.sockets[0].getsockname()[1]

    import aiohttp

    client_ctx = load_client_ssl_context(
        ca_file=str(material["ca"]),
        check_hostname=False,
    )
    connector = aiohttp.TCPConnector(ssl=client_ctx)
    async with aiohttp.ClientSession(connector=connector) as session:
        url = f"https://127.0.0.1:{port}{TOKEN_PATH}"
        async with session.post(
            url,
            json={"bootstrap_token": "boot-w1", "worker_id": "w1"},
        ) as resp:
            assert resp.status == 200
            body = await resp.json()
            assert "worker_jwt" in body

    await runner.cleanup()
