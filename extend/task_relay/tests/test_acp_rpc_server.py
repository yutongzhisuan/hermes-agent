"""Integration tests for the XHermes ACP JSON-RPC server."""

from __future__ import annotations

import pytest
from aiohttp import ClientSession, web

from extend.task_relay.worker.acp_rpc_server import create_acp_rpc_app
from extend.task_relay.worker.backends.stub_backend import StubBackend


@pytest.mark.asyncio
async def test_acp_rpc_run_via_http():
    app = create_acp_rpc_app(backend=StubBackend())
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", 0)
    await site.start()
    port = site._server.sockets[0].getsockname()[1]
    url = f"http://127.0.0.1:{port}/rpc"
    try:
        body = {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "acp.run",
            "params": {
                "run_id": "run-1",
                "task_id": "t1",
                "goal": "rpc goal",
                "timeout_seconds": 30,
            },
        }
        async with ClientSession() as session:
            async with session.post(url, json=body) as resp:
                assert resp.status == 200
                payload = await resp.json()
        assert payload["result"]["status"] == "completed"
        assert "rpc goal" in payload["result"]["summary"]
    finally:
        await runner.cleanup()
