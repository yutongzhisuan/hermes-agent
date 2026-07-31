"""TaskWorker E2E against Go Hub with optional two-step poll."""

from __future__ import annotations

import asyncio

import pytest

pytest_plugins = ["extend.task_relay.tests.go_hub_runner"]

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.tests.conftest import make_worker_jwt
from extend.task_relay.tests.go_hub_runner import GoHubRunner, bearer_metadata
from extend.task_relay.tests.test_e2e_mode_a import _spec, _stop_worker, _watch_terminal
from extend.task_relay.worker.backends.stub_backend import StubBackend, StubBackendConfig
from extend.task_relay.worker.task_worker import TaskWorker


async def _start_two_step_worker(hub: GoHubRunner) -> tuple[TaskWorker, asyncio.Task]:
    jwt = make_worker_jwt("w1", allowed_toolsets=[], max_concurrent=1)
    worker = TaskWorker(
        worker_id="w1",
        relay_url=hub.ws_url,
        jwt=jwt,
        backend=StubBackend(StubBackendConfig(sleep_seconds=0.05)),
        poll_wait_ms=500,
        initial_backoff_seconds=0.05,
        prefer_atomic_claim=False,
        probe_resources_enabled=False,
    )
    run_task = asyncio.create_task(worker.run())
    return worker, run_task


@pytest.mark.asyncio
async def test_go_hub_task_worker_two_step_poll_completes(go_hub):
    task_id = "go-e2e-two-step"
    worker, run_task = await _start_two_step_worker(go_hub)
    try:
        stub = TaskRelayStub(go_hub.grpc_channel)
        watch_future = asyncio.create_task(
            _watch_terminal(stub, go_hub.master_jwt, task_id=task_id)
        )
        await stub.DispatchTask(
            pb.DispatchTaskRequest(
                spec=_spec(task_id, "two step goal", "topic-go-two-step"),
                master_session_id="m1",
            ),
            metadata=bearer_metadata(go_hub.master_jwt),
        )
        events = await asyncio.wait_for(watch_future, timeout=8.0)
        assert events[-1].kind == pb.TaskEventKind.TASK_EVENT_KIND_TERMINAL
        assert events[-1].result.status == pb.TaskStatus.TASK_STATUS_COMPLETED
        assert "two step goal" in events[-1].result.summary
    finally:
        await _stop_worker(worker, run_task)
