"""M2 Mode C push delivery tests."""

from __future__ import annotations

import asyncio

import pytest
import websockets

pytestmark = pytest.mark.python_hub

from extend.task_relay.hub.bootstrap import start_ws_server
from extend.task_relay.tests.conftest import make_worker_jwt, make_task_spec
from extend.task_relay.worker.backends.stub_backend import StubBackend, StubBackendConfig
from extend.task_relay.worker.task_worker import TaskWorker


@pytest.mark.asyncio
async def test_mode_c_push_delivers_task_run(router, registry, db, auth):
    server = await start_ws_server(
        router, auth, registry, db, router._config, host="127.0.0.1", port=0
    )
    try:
        ws_url = f"ws://127.0.0.1:{server.sockets[0].getsockname()[1]}"
        jwt = make_worker_jwt("wc1", max_concurrent=1)
        await router.dispatch_task(
            make_task_spec(task_id="t1", goal="hello mode c"), "m1"
        )

        worker = TaskWorker(
            worker_id="wc1",
            relay_url=ws_url,
            jwt=jwt,
            backend=StubBackend(StubBackendConfig(sleep_seconds=0.05)),
            session_modes=["a", "c"],
            max_concurrent=1,
        )

        async def stop_when_done():
            for _ in range(100):
                task = await db.get_task("t1")
                if task and task.status in {"completed", "failed", "cancelled", "lost"}:
                    break
                await asyncio.sleep(0.05)
            await worker.shutdown()

        await asyncio.gather(worker.run(), stop_when_done())

        task = await db.get_task("t1")
        assert task.status == "completed"
        assert "mode c" in (task.summary or "").lower() or "hello" in (task.summary or "").lower()
    finally:
        server.close()
        await server.wait_closed()


@pytest.mark.asyncio
async def test_worker_credit_refreshes_after_complete(router, registry, db, auth):
    server = await start_ws_server(
        router, auth, registry, db, router._config, host="127.0.0.1", port=0
    )
    try:
        ws_url = f"ws://127.0.0.1:{server.sockets[0].getsockname()[1]}"
        jwt = make_worker_jwt("wc2", max_concurrent=2)

        async with websockets.connect(
            ws_url, additional_headers={"Authorization": f"Bearer {jwt}"}
        ) as ws:
            await ws.send(
                '{"jsonrpc":"2.0","id":1,"method":"worker.announce","params":'
                '{"worker_id":"wc2","session_modes":["a","c"],"max_concurrent":2,"credit":2}}'
            )
            resp = await ws.recv()
            assert "worker.announce_ok" in resp

            await router.dispatch_task(make_task_spec(task_id="t2", goal="first"), "m1")
            pushed = await ws.recv()
            assert "task.run" in pushed

            complete = (
                '{"jsonrpc":"2.0","id":2,"method":"task.complete","params":'
                '{"task_id":"t2","status":"completed","summary":"done"}}'
            )
            await ws.send(complete)
            credit_resp = await ws.recv()
            assert "worker.credit_ok" in credit_resp or "task.complete" in credit_resp

            worker_row = await registry.get_worker("wc2")
            assert worker_row is not None
            assert worker_row.credit_available >= 1
    finally:
        server.close()
        await server.wait_closed()
