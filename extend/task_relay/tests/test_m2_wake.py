"""M2 Mode B wake token and worker.claim tests."""

from __future__ import annotations

import pytest

from extend.task_relay.hub.bootstrap import start_ws_server
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.wake_scheduler import WakeScheduler
from extend.task_relay.tests.conftest import make_auth, make_task_spec, make_worker_jwt


@pytest.mark.asyncio
async def test_wake_token_verify_and_consume(auth, db, registry):
    wake = WakeScheduler(db, registry, auth, HubConfig(jwt_secret=auth._secret))
    token, expires_at = wake.issue_wake_token("t1", "wb1")
    assert wake.verify_wake_token("t1", "wb1", token, expires_at)
    assert not wake.verify_wake_token("t1", "wb1", token, expires_at)


@pytest.mark.asyncio
async def test_worker_claim_with_wake_token(router, registry, db, auth):
    wake = WakeScheduler(
        db,
        registry,
        auth,
        router._config,
        relay_ws_url="ws://127.0.0.1:9000",
    )
    server = await start_ws_server(
        router,
        auth,
        registry,
        db,
        router._config,
        wake=wake,
        host="127.0.0.1",
        port=0,
    )
    try:
        import websockets

        ws_url = f"ws://127.0.0.1:{server.sockets[0].getsockname()[1]}"
        jwt = make_worker_jwt("wb1", max_concurrent=1)
        await router.dispatch_task(make_task_spec(task_id="t-wake-1"), "m1")

        token, expires_at = wake.issue_wake_token("t-wake-1", "wb1")
        async with websockets.connect(
            ws_url, additional_headers={"Authorization": f"Bearer {jwt}"}
        ) as ws:
            await ws.send(
                '{"jsonrpc":"2.0","id":1,"method":"worker.announce","params":'
                '{"worker_id":"wb1","session_modes":["a","b"],"max_concurrent":1}}'
            )
            await ws.recv()

            claim = (
                '{"jsonrpc":"2.0","id":2,"method":"worker.claim","params":'
                f'{{"task_id":"t-wake-1","wake_token":"{token}","expires_at":{int(expires_at)}}}}}'
            )
            await ws.send(claim)
            resp = await ws.recv()
            assert "worker.claim_ok" in resp
            assert "t-wake-1" in resp

        task = await db.get_task("t-wake-1")
        assert task.status == "running"
        assert task.worker_id == "wb1"
    finally:
        server.close()
        await server.wait_closed()
