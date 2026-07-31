"""Go Hub E2E: Prometheus metrics endpoint."""

from __future__ import annotations

import socket

import pytest

pytest_plugins = ["extend.task_relay.tests.go_hub_runner"]

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.tests.go_hub_e2e import ws_complete, ws_connect, ws_poll
from extend.task_relay.tests.live_hub import HubLaunchConfig, bearer_metadata, start_live_hub, stop_live_hub


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


@pytest.mark.asyncio
async def test_go_hub_metrics_endpoint_after_dispatch(tmp_path):
    metrics_port = _free_port()
    hub = await start_live_hub(tmp_path, HubLaunchConfig(metrics_port=metrics_port))
    try:
        stub = TaskRelayStub(hub.grpc_channel)
        await stub.DispatchTask(
            pb.DispatchTaskRequest(
                spec=pb.TaskSpec(task_id="met-1", goal="metrics", callback_topic="met-t"),
                master_session_id="m1",
            ),
            metadata=bearer_metadata(hub.master_jwt),
        )
        ws, _ = await ws_connect(hub, "w1")
        try:
            poll = await ws_poll(ws)
            await ws_complete(ws, poll["tasks"][0]["task_id"])
        finally:
            await ws.close()
        import urllib.request

        body = urllib.request.urlopen(f"http://127.0.0.1:{metrics_port}/metrics", timeout=3).read().decode()
        assert "relay_tasks_dispatched_total" in body
        assert "relay_tasks_terminal_total" in body
    finally:
        await stop_live_hub(hub)


@pytest.mark.asyncio
async def test_go_hub_worker_sessions_gauge(tmp_path):
    metrics_port = _free_port()
    hub = await start_live_hub(tmp_path, HubLaunchConfig(metrics_port=metrics_port))
    try:
        ws, _ = await ws_connect(hub, "wg1")
        await ws.close()
        import urllib.request

        body = urllib.request.urlopen(f"http://127.0.0.1:{metrics_port}/metrics", timeout=3).read().decode()
        assert "relay_worker_sessions_active" in body
    finally:
        await stop_live_hub(hub)
