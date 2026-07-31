"""Tests for RemoteAcpTaskBackend JSON-RPC delegation."""

from __future__ import annotations

import asyncio
import json
from http.server import BaseHTTPRequestHandler, HTTPServer
from threading import Thread

import pytest

from extend.task_relay.worker.backends.remote_acp_backend import (
    RemoteAcpBackend,
    RemoteAcpBackendConfig,
)
from extend.task_relay.worker.task_executor import TaskRunPayload


class _FakeRemoteAcpHandler(BaseHTTPRequestHandler):
    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length", "0"))
        body = json.loads(self.rfile.read(length).decode("utf-8"))
        method = body.get("method")
        if method == "acp.run":
            result = {
                "status": "completed",
                "summary": "remote ok",
                "result_text": "remote ok",
            }
        elif method == "acp.cancel":
            result = {"cancelled": True}
        else:
            result = {"error": "unknown method"}
        payload = json.dumps({"jsonrpc": "2.0", "id": 1, "result": result}).encode(
            "utf-8"
        )
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, format: str, *args) -> None:  # noqa: A003
        return


@pytest.fixture
def remote_acp_url():
    server = HTTPServer(("127.0.0.1", 0), _FakeRemoteAcpHandler)
    thread = Thread(target=server.serve_forever, daemon=True)
    thread.start()
    host, port = server.server_address
    try:
        yield f"http://{host}:{port}/rpc"
    finally:
        server.shutdown()
        thread.join(timeout=2)


@pytest.mark.asyncio
async def test_remote_acp_backend_completes(remote_acp_url):
    backend = RemoteAcpBackend(RemoteAcpBackendConfig(endpoint_url=remote_acp_url))
    run = TaskRunPayload(
        task_id="r1",
        attempt=1,
        goal="remote goal",
        params=None,
        context=None,
        toolsets=[],
        timeout_seconds=30,
        first_progress_seconds=None,
        trace_context=None,
        resume_from_checkpoint=None,
    )
    cancel_event = asyncio.Event()
    progress = []

    async def on_progress(summary: str) -> None:
        progress.append(summary)

    async def on_checkpoint(**kwargs) -> None:
        return None

    result = await backend.run(run, on_progress, on_checkpoint, cancel_event)
    assert result.status == "completed"
    assert result.summary == "remote ok"
    assert progress
