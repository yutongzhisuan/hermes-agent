"""Integration tests for the XHermes ACP JSON-RPC server.

Migrated from swarm-network ``tests/test_acp_rpc_server.py``. The stub
backend is inlined here because the swarm-network worker backends are not
part of this package.
"""

from __future__ import annotations

import asyncio

import pytest
from aiohttp import ClientSession, web

from extend.task_relay.acp_rpc_server import create_acp_rpc_app
from extend.task_relay.task_types import TaskCompletePayload


class _StubBackend:
    """Minimal backend: echoes the goal after a short sleep."""

    async def run(self, run, on_progress, on_checkpoint, cancel_event):
        await asyncio.sleep(0.01)
        return TaskCompletePayload(
            status="completed",
            summary=f"stub done: {run.goal}",
            result_text=f"stub done: {run.goal}",
        )


@pytest.mark.asyncio
async def test_acp_rpc_run_via_http():
    app = create_acp_rpc_app(backend=_StubBackend())
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


class _CheckpointBackend:
    async def run(self, run, on_progress, on_checkpoint, cancel_event):
        await on_checkpoint(
            checkpoint_id="cp-9",
            summary="mid",
            fields={"k": "v"},
            resume_blob="state",
        )
        return TaskCompletePayload(status="completed", summary="ok", result_text="ok")


@pytest.mark.asyncio
async def test_acp_rpc_returns_last_checkpoint():
    app = create_acp_rpc_app(backend=_CheckpointBackend())
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
            "params": {"run_id": "run-cp", "task_id": "t1", "goal": "g", "timeout_seconds": 30},
        }
        async with ClientSession() as session:
            async with session.post(url, json=body) as resp:
                assert resp.status == 200
                payload = await resp.json()
        checkpoint = payload["result"]["checkpoint"]
        assert checkpoint["checkpoint_id"] == "cp-9"
        assert checkpoint["summary"] == "mid"
        assert checkpoint["fields"] == {"k": "v"}
        assert checkpoint["resume_blob"] == "state"
    finally:
        await runner.cleanup()
