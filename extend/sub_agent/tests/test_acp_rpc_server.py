"""Integration tests for the XHermes ACP JSON-RPC server.

Migrated from swarm-network ``tests/test_acp_rpc_server.py``. The stub
backend is inlined here because the swarm-network worker backends are not
part of this package.
"""

from __future__ import annotations

import asyncio
import os
import tempfile

import pytest
from aiohttp import ClientSession, UnixConnector, web

from extend.sub_agent.acp_rpc_server import create_acp_rpc_app
from extend.sub_agent.task_types import TaskCompletePayload


class _StubBackend:
    """Minimal backend: echoes the goal after a short sleep."""

    async def run(self, run, on_progress, on_checkpoint, cancel_event):
        await asyncio.sleep(0.01)
        return TaskCompletePayload(
            status="completed",
            summary=f"stub done: {run.goal}",
            result_text=f"stub done: {run.goal}",
        )


async def _post_rpc(url: str, body: dict, *, connector=None) -> dict:
    async with ClientSession(connector=connector) as session:
        async with session.post(url, json=body) as resp:
            assert resp.status == 200
            return await resp.json()


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
        payload = await _post_rpc(url, body)
        assert payload["result"]["status"] == "completed"
        assert "rpc goal" in payload["result"]["summary"]
    finally:
        await runner.cleanup()


@pytest.mark.asyncio
async def test_acp_rpc_run_via_uds():
    with tempfile.TemporaryDirectory() as tmp:
        sock_path = os.path.join(tmp, "acp.sock")
        app = create_acp_rpc_app(backend=_StubBackend())
        runner = web.AppRunner(app)
        await runner.setup()
        site = web.UnixSite(runner, sock_path)
        await site.start()
        try:
            body = {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "acp.run",
                "params": {
                    "run_id": "run-uds",
                    "task_id": "t1",
                    "goal": "uds goal",
                    "timeout_seconds": 30,
                },
            }
            connector = UnixConnector(path=sock_path)
            payload = await _post_rpc("http://localhost/rpc", body, connector=connector)
            assert payload["result"]["status"] == "completed"
            assert "uds goal" in payload["result"]["summary"]
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
        payload = await _post_rpc(url, body)
        checkpoint = payload["result"]["checkpoint"]
        assert checkpoint["checkpoint_id"] == "cp-9"
        assert checkpoint["summary"] == "mid"
        assert checkpoint["fields"] == {"k": "v"}
        assert checkpoint["resume_blob"] == "state"
    finally:
        await runner.cleanup()
