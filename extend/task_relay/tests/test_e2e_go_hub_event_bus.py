"""Go Hub E2E: WatchTask replay, cursor, and SlowConsumer via gRPC."""

from __future__ import annotations

import asyncio
import subprocess

import pytest
from grpclib.const import Status
from grpclib.exceptions import GRPCError

pytest_plugins = ["extend.task_relay.tests.go_hub_runner"]

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.tests.go_hub_e2e import task_spec, watch_collect, watch_until_terminal
from extend.task_relay.tests.go_hub_runner import GoHubRunner, bearer_metadata
from extend.task_relay.tests.live_hub import (
    HUB_GO,
    delete_task_events_for_topic,
    query_sql,
    start_live_hub,
    stop_live_hub,
)
from extend.task_relay.tests.test_e2e_mode_a import HubRunner, _start_worker, _stop_worker
from extend.task_relay.worker.backends.stub_backend import StubBackend, StubBackendConfig


@pytest.mark.asyncio
async def test_go_hub_event_ids_monotonic(go_hub: GoHubRunner):
    stub = TaskRelayStub(go_hub.grpc_channel)
    for idx in range(3):
        await stub.DispatchTask(
            pb.DispatchTaskRequest(
                spec=task_spec(f"evt-{idx}", f"goal-{idx}", f"topic-{idx % 2}"),
                master_session_id="m1",
            ),
            metadata=bearer_metadata(go_hub.master_jwt),
        )
    rows = query_sql(
        go_hub.live,
        "SELECT event_id FROM task_events ORDER BY event_id ASC",
    )
    ids = [int(row["event_id"]) for row in rows]
    assert len(ids) >= 3
    assert ids == sorted(ids)
    assert len(set(ids)) == len(ids)


@pytest.mark.asyncio
async def test_go_hub_watch_replay_after_cursor(go_hub: GoHubRunner):
    stub = TaskRelayStub(go_hub.grpc_channel)
    task_id = "replay-1"
    backend = StubBackend(StubBackendConfig(sleep_seconds=0.05))
    runner = HubRunner(
        grpc_channel=go_hub.grpc_channel,
        ws_url=go_hub.ws_url,
        router=None,
        db=None,
        registry=None,
        auth=go_hub.live.auth,
        live=go_hub.live,
    )
    worker, run_task = await _start_worker(runner, backend)
    try:
        await stub.DispatchTask(
            pb.DispatchTaskRequest(
                spec=task_spec(task_id, "replay goal", "replay-topic"),
                master_session_id="m1",
            ),
            metadata=bearer_metadata(go_hub.master_jwt),
        )
        events = await watch_until_terminal(go_hub.live, task_id, timeout=10.0)
        assert events
        status_events = [
            event for event in events
            if event.kind == pb.TaskEventKind.TASK_EVENT_KIND_STATUS
        ]
        assert status_events
        cursor = status_events[0].event_id
        replay = await watch_collect(
            go_hub.live, task_id=task_id, since_event_id=cursor, max_events=10, timeout=3.0
        )
        assert all(event.event_id > cursor for event in replay)
    finally:
        await _stop_worker(worker, run_task)


@pytest.mark.asyncio
async def test_go_hub_watch_cursor_out_of_range(tmp_path):
    hub = await start_live_hub(tmp_path)
    try:
        stub = TaskRelayStub(hub.grpc_channel)
        await stub.DispatchTask(
            pb.DispatchTaskRequest(
                spec=task_spec("c1", "one", "cursor-topic"),
                master_session_id="m1",
            ),
            metadata=bearer_metadata(hub.master_jwt),
        )
        await stub.DispatchTask(
            pb.DispatchTaskRequest(
                spec=task_spec("c2", "two", "other-topic"),
                master_session_id="m1",
            ),
            metadata=bearer_metadata(hub.master_jwt),
        )
        delete_task_events_for_topic(hub, "cursor-topic")
        with pytest.raises(GRPCError) as exc:
            async with stub.WatchTask.open(metadata=bearer_metadata(hub.master_jwt)) as stream:
                await stream.send_message(
                    pb.WatchTaskRequest(topic="cursor-topic", since_event_id=1),
                    end=True,
                )
                await stream.recv_message()
        assert exc.value.status == Status.FAILED_PRECONDITION
        assert "since_event_id" in exc.value.message
    finally:
        await stop_live_hub(hub)


def test_go_hub_slow_consumer_overflow():
    """SlowConsumer parity is validated at the eventbus layer (same as Python test_event_bus)."""
    result = subprocess.run(
        ["go", "test", "./internal/eventbus/...", "-run", "TestSlowConsumerOverflow", "-count=1"],
        cwd=HUB_GO,
        capture_output=True,
        text=True,
    )
    assert result.returncode == 0, result.stderr or result.stdout
