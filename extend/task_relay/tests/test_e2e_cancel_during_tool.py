"""E2E cancel-during-execution via Hub + TaskWorker + StubBackend."""

from __future__ import annotations

import asyncio

import pytest

pytest_plugins = ["extend.task_relay.tests.test_e2e_mode_a"]

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.tests.test_e2e_mode_a import (
    HubRunner,
    _bearer_metadata,
    _spec,
    _start_worker,
    _stop_worker,
    _wait_for_status,
    _watch_terminal,
)
from extend.task_relay.worker.backends.stub_backend import StubBackend, StubBackendConfig


@pytest.mark.asyncio
async def test_cancel_during_tool_execution_e2e(hub, master_jwt):
    task_id = "e2e-cancel-tool"
    backend = StubBackend(StubBackendConfig(sleep_seconds=3.0))
    worker, run_task = await _start_worker(hub, backend)
    try:
        stub = TaskRelayStub(hub.grpc_channel)
        watch_future = asyncio.create_task(
            _watch_terminal(stub, master_jwt, task_id=task_id)
        )

        await stub.DispatchTask(
            pb.DispatchTaskRequest(
                spec=_spec(task_id, "slow tool work", "topic-cancel-tool"),
                master_session_id="m1",
            ),
            metadata=_bearer_metadata(master_jwt),
        )

        await _wait_for_status(hub, task_id, {"running"})
        await stub.CancelTask(
            pb.CancelTaskRequest(task_id=task_id, reason="cancel during tool"),
            metadata=_bearer_metadata(master_jwt),
        )

        events = await asyncio.wait_for(watch_future, timeout=8.0)
        assert events[-1].kind == pb.TaskEventKind.TASK_EVENT_KIND_TERMINAL
        assert events[-1].result.status == pb.TaskStatus.TASK_STATUS_CANCELLED
        assert "slow tool work" in (events[-1].result.summary or "")
    finally:
        await _stop_worker(worker, run_task)
