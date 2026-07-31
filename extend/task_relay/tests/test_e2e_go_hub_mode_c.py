"""Go Hub E2E: Mode C push delivery and credit refresh."""

from __future__ import annotations

import asyncio
import json

import pytest
import websockets

pytest_plugins = ["extend.task_relay.tests.go_hub_runner"]

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.tests.conftest import make_worker_jwt
from extend.task_relay.tests.go_hub_e2e import jsonrpc_request, ws_recv_result
from extend.task_relay.tests.go_hub_runner import GoHubRunner, bearer_metadata
from extend.task_relay.tests.live_hub import read_task_row, wait_for_task_status
from extend.task_relay.worker.backends.stub_backend import StubBackend, StubBackendConfig
from extend.task_relay.worker.task_worker import TaskWorker


@pytest.mark.asyncio
async def test_go_hub_mode_c_push_delivers_task_run(go_hub: GoHubRunner):
    jwt = make_worker_jwt("wc1", max_concurrent=1)
    worker = TaskWorker(
        worker_id="wc1",
        relay_url=go_hub.ws_url,
        jwt=jwt,
        backend=StubBackend(StubBackendConfig(sleep_seconds=0.05)),
        session_modes=["a", "c"],
        max_concurrent=1,
        poll_wait_ms=500,
        initial_backoff_seconds=0.05,
    )
    run_task = asyncio.create_task(worker.run())
    try:
        await asyncio.sleep(0.5)
        stub = TaskRelayStub(go_hub.grpc_channel)
        await stub.DispatchTask(
            pb.DispatchTaskRequest(
                spec=pb.TaskSpec(task_id="mc-1", goal="hello mode c", callback_topic="mc-t"),
                master_session_id="m1",
            ),
            metadata=bearer_metadata(go_hub.master_jwt),
        )
        await wait_for_task_status(go_hub.live, "mc-1", {"completed"}, timeout=15.0)
        row = read_task_row(go_hub.live, "mc-1")
        assert row["status"] == "completed"
    finally:
        await worker.shutdown()
        run_task.cancel()
        try:
            await run_task
        except asyncio.CancelledError:
            pass


@pytest.mark.asyncio
async def test_go_hub_mode_c_credit_refresh_after_complete(go_hub: GoHubRunner):
    jwt = make_worker_jwt("wc2", max_concurrent=2)
    async with websockets.connect(
        go_hub.ws_url, additional_headers={"Authorization": f"Bearer {jwt}"}
    ) as ws:
        await ws.send(
            jsonrpc_request(
                1,
                "worker.announce",
                {
                    "worker_id": "wc2",
                    "session_modes": ["a", "c"],
                    "max_concurrent": 2,
                    "credit": 2,
                },
            )
        )
        await ws_recv_result(ws)
        stub = TaskRelayStub(go_hub.grpc_channel)
        await stub.DispatchTask(
            pb.DispatchTaskRequest(
                spec=pb.TaskSpec(task_id="mc-2", goal="credit test", callback_topic="mc-t"),
                master_session_id="m1",
            ),
            metadata=bearer_metadata(go_hub.master_jwt),
        )
        pushed = json.loads(await ws.recv())
        assert pushed.get("method") == "task.run"
        await ws.send(
            jsonrpc_request(
                2,
                "task.complete",
                {"task_id": "mc-2", "status": "completed", "summary": "done"},
            )
        )
        await ws_recv_result(ws)
        await ws.send(jsonrpc_request(3, "worker.credit", {"available": 2}))
        credit = await ws_recv_result(ws)
        assert credit.get("accepted", 0) >= 1
