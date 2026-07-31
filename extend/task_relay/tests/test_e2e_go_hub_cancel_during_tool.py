"""E2E cancel-during-execution against Go Hub + TaskWorker + StubBackend."""

from __future__ import annotations

import asyncio

import pytest

pytest_plugins = ["extend.task_relay.tests.go_hub_runner"]

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.tests.conftest import make_worker_jwt
from extend.task_relay.tests.go_hub_runner import bearer_metadata, wait_for_status
from extend.task_relay.tests.test_e2e_mode_a import _spec, _stop_worker, _watch_terminal
from extend.task_relay.worker.backends.stub_backend import StubBackend, StubBackendConfig
from extend.task_relay.worker.task_worker import TaskWorker


async def _start_worker(hub, backend) -> tuple[TaskWorker, asyncio.Task]:
    jwt = make_worker_jwt("w1", allowed_toolsets=[], max_concurrent=1)
    worker = TaskWorker(
        worker_id="w1",
        relay_url=hub.ws_url,
        jwt=jwt,
        backend=backend,
        poll_wait_ms=500,
        initial_backoff_seconds=0.05,
    )
    run_task = asyncio.create_task(worker.run())
    return worker, run_task


@pytest.mark.asyncio
async def test_go_hub_cancel_during_tool_execution_e2e(go_hub):
    task_id = "go-e2e-cancel-tool"
    backend = StubBackend(StubBackendConfig(sleep_seconds=3.0))
    worker, run_task = await _start_worker(go_hub, backend)
    try:
        stub = TaskRelayStub(go_hub.grpc_channel)
        watch_future = asyncio.create_task(
            _watch_terminal(stub, go_hub.master_jwt, task_id=task_id)
        )
        await stub.DispatchTask(
            pb.DispatchTaskRequest(
                spec=_spec(task_id, "slow tool work", "topic-go-cancel-tool"),
                master_session_id="m1",
            ),
            metadata=bearer_metadata(go_hub.master_jwt),
        )
        await wait_for_status(stub, go_hub.master_jwt, task_id, {"running"})
        await stub.CancelTask(
            pb.CancelTaskRequest(task_id=task_id, reason="cancel during tool"),
            metadata=bearer_metadata(go_hub.master_jwt),
        )
        events = await asyncio.wait_for(watch_future, timeout=8.0)
        assert events[-1].kind == pb.TaskEventKind.TASK_EVENT_KIND_TERMINAL
        assert events[-1].result.status == pb.TaskStatus.TASK_STATUS_CANCELLED
        assert "slow tool work" in (events[-1].result.summary or "")
    finally:
        await _stop_worker(worker, run_task)
