"""Worker-side TLS/mTLS client tests."""

from __future__ import annotations

import ssl

import pytest
import pytest_asyncio

from extend.task_relay.hub.bootstrap import start_ws_server
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.bootstrap import wire_orchestration
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.tls import TlsConfig, load_client_ssl_context, load_server_ssl_context
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.tests.conftest import SECRET, make_auth, make_worker_jwt
from extend.task_relay.tests.tls_helpers import generate_test_tls_material
from extend.task_relay.worker.task_worker_ws import TaskWorkerWs
from extend.task_relay.worker.tls_client import ClientTlsConfig, build_client_ssl_context


def test_build_client_ssl_context_disabled():
    assert build_client_ssl_context(ClientTlsConfig()) is None


def test_build_client_ssl_context_requires_cert_pair(tmp_path):
    material = generate_test_tls_material(tmp_path)
    tls = ClientTlsConfig(
        ca_file=str(material["ca"]),
        cert_file=str(material["client_cert"]),
    )
    with pytest.raises(ValueError, match="cert_file and key_file"):
        build_client_ssl_context(tls)


def test_build_client_ssl_context_requires_ca_for_mtls(tmp_path):
    material = generate_test_tls_material(tmp_path)
    tls = ClientTlsConfig(
        cert_file=str(material["client_cert"]),
        key_file=str(material["client_key"]),
    )
    with pytest.raises(ValueError, match="ca_file is required"):
        build_client_ssl_context(tls)


def test_build_client_ssl_context_mtls(tmp_path):
    material = generate_test_tls_material(tmp_path)
    ctx = build_client_ssl_context(
        ClientTlsConfig(
            ca_file=str(material["ca"]),
            cert_file=str(material["client_cert"]),
            key_file=str(material["client_key"]),
            skip_hostname_verify=True,
        )
    )
    assert isinstance(ctx, ssl.SSLContext)


@pytest_asyncio.fixture
async def tls_ws_stack(db):
    auth = make_auth()
    config = HubConfig(jwt_secret=SECRET)
    bus = EventBus(db, config)
    registry = WorkerRegistry(db)
    router = TaskRouter(db, bus, config, registry)
    wire_orchestration(router, db, bus)
    return router, auth, registry, db, config


@pytest.mark.asyncio
async def test_worker_ws_mtls_connect(tmp_path, tls_ws_stack):
    material = generate_test_tls_material(tmp_path)
    router, auth, registry, db, config = tls_ws_stack
    server_ctx = load_server_ssl_context(
        TlsConfig(
            cert_file=str(material["server_cert"]),
            key_file=str(material["server_key"]),
            ca_file=str(material["ca"]),
            require_client_cert=True,
        )
    )
    server = await start_ws_server(
        router,
        auth,
        registry,
        db,
        config,
        host="127.0.0.1",
        port=0,
        ssl=server_ctx,
    )
    port = server.sockets[0].getsockname()[1]
    relay_url = f"wss://127.0.0.1:{port}"
    worker_jwt = make_worker_jwt("w1")
    client_ctx = load_client_ssl_context(
        ca_file=str(material["ca"]),
        cert_file=str(material["client_cert"]),
        key_file=str(material["client_key"]),
        check_hostname=False,
    )

    ws = TaskWorkerWs(relay_url, worker_jwt, ssl_context=client_ctx)
    await ws.connect()
    result = await ws.request(
        "worker.announce",
        {"worker_id": "w1", "session_modes": ["a"], "max_concurrent": 1},
    )
    assert "session_id" in result
    await ws.close()
    server.close()
    await server.wait_closed()
