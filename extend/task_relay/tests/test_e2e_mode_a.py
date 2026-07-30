"""End-to-end conformance suite for Task Relay Mode A (M1).

Spins up the real Hub (gRPC + WebSocket + timeout ticker) and drives the full
Master -> Hub -> Worker -> StubBackend path through the generated gRPC client.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from typing import Any

import pytest
import pytest_asyncio
import websockets
from grpclib.client import Channel
from grpclib.const import Status
from grpclib.exceptions import GRPCError

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.grpc_server import serve_grpc
from extend.task_relay.hub.ws_server import serve_ws
from extend.task_relay.tests.conftest import SECRET, make_worker_jwt
from extend.task_relay.worker.backends.stub_backend import StubBackend, StubBackendConfig
from extend.task_relay.worker.task_executor import TaskBackend, TaskExecutor
from extend.task_relay.worker.task_worker import TaskWorker


@dataclass(frozen=True)
class HubRunner:
    """Live Hub runtime exposed to E2E tests."""

    grpc_channel: Channel
    ws_url: str
    router: Any
    db: Any
    registry: Any
    auth: Any


def _spec(task_id: str, goal: str, callback_topic: str) -> pb.TaskSpec:
    """Build a minimal TaskSpec. No toolsets so the default worker can claim it."""
    return pb.TaskSpec(task_id=task_id, goal=goal, callback_topic=callback_topic)


def _bearer_metadata(token: str) -> dict[str, str]:
    return {"authorization": f"Bearer {token}"}


async def _wait_for_status(
    router,
    task_id: str,
    statuses: set[str],
    timeout: float = 5.0,
) -> str:
    deadline = asyncio.get_event_loop().time() + timeout
    while asyncio.get_event_loop().time() < deadline:
        status = await router.get_status(task_id)
        if status in statuses:
            return status
        await asyncio.sleep(0.05)
    raise AssertionError(
        f"task {task_id} did not reach any of {statuses} within {timeout}s"
    )


async def _watch_terminal(
    stub: TaskRelayStub,
    master_jwt: str,
    **kwargs: Any,
) -> list[pb.TaskEvent]:
    """Open a WatchTask stream and collect events until TERMINAL."""
    events: list[pb.TaskEvent] = []
    async with stub.WatchTask.open(metadata=_bearer_metadata(master_jwt)) as stream:
        await stream.send_message(pb.WatchTaskRequest(**kwargs), end=True)
        while True:
            event = await stream.recv_message()
            if event is None:
                break
            events.append(event)
            if event.kind == pb.TaskEventKind.TASK_EVENT_KIND_TERMINAL:
                break
    return events


async def _start_worker(hub: HubRunner, backend: TaskBackend) -> tuple[TaskWorker, asyncio.Task]:
    """Start a real Mode-A worker connected to ``hub``."""
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


async def _stop_worker(worker: TaskWorker, run_task: asyncio.Task) -> None:
    await worker.shutdown()
    try:
        await asyncio.wait_for(run_task, timeout=2.0)
    except asyncio.TimeoutError:
        run_task.cancel()
        try:
            await run_task
        except Exception:
            pass


@pytest_asyncio.fixture
async def hub(router, registry, db, auth):
    """Start gRPC + WS servers and a timeout ticker on ephemeral ports."""
    grpc_server = await serve_grpc(
        router, auth, router._config, host="127.0.0.1", port=0
    )
    ws_server = await serve_ws(
        router,
        auth,
        registry,
        db,
        router._config,
        host="127.0.0.1",
        port=0,
    )

    shutdown = asyncio.Event()

    async def ticker() -> None:
        while not shutdown.is_set():
            await router.tick_timeouts()
            try:
                await asyncio.wait_for(shutdown.wait(), timeout=0.25)
            except asyncio.TimeoutError:
                pass

    ticker_task = asyncio.create_task(ticker())

    grpc_port = grpc_server._server.sockets[0].getsockname()[1]
    ws_port = ws_server.sockets[0].getsockname()[1]
    channel = Channel(host="127.0.0.1", port=grpc_port)

    runner = HubRunner(
        grpc_channel=channel,
        ws_url=f"ws://127.0.0.1:{ws_port}",
        router=router,
        db=db,
        registry=registry,
        auth=auth,
    )
    try:
        yield runner
    finally:
        shutdown.set()
        await ticker_task
        channel.close()
        ws_server.close()
        grpc_server.close()
        await ws_server.wait_closed()
        await grpc_server.wait_closed()


@pytest.fixture
def backend():
    return StubBackend(StubBackendConfig(sleep_seconds=0.1))


# ---------------------------------------------------------------------------
# 1. Dispatch -> poll -> stub complete -> Watch TERMINAL completed
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_dispatch_poll_stub_complete_watch_terminal(hub, master_jwt, backend):
    task_id = "e2e-complete"
    worker, run_task = await _start_worker(hub, backend)
    try:
        stub = TaskRelayStub(hub.grpc_channel)
        watch_future = asyncio.create_task(
            _watch_terminal(stub, master_jwt, task_id=task_id)
        )

        await stub.DispatchTask(
            pb.DispatchTaskRequest(
                spec=_spec(task_id, "hello e2e", "topic-complete"),
                master_session_id="m1",
            ),
            metadata=_bearer_metadata(master_jwt),
        )

        events = await asyncio.wait_for(watch_future, timeout=5.0)
        assert len(events) >= 2
        assert events[-1].kind == pb.TaskEventKind.TASK_EVENT_KIND_TERMINAL
        assert events[-1].result.status == pb.TaskStatus.TASK_STATUS_COMPLETED
        assert "hello e2e" in events[-1].result.summary
    finally:
        await _stop_worker(worker, run_task)


# ---------------------------------------------------------------------------
# 2. Idempotent dispatch hit
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_idempotent_dispatch_hit(hub, master_jwt):
    task_id = "e2e-idempotent"
    stub = TaskRelayStub(hub.grpc_channel)
    spec = _spec(task_id, "idempotent goal", "topic-idempotent")

    resp1 = await stub.DispatchTask(
        pb.DispatchTaskRequest(spec=spec, master_session_id="m1"),
        metadata=_bearer_metadata(master_jwt),
    )
    resp2 = await stub.DispatchTask(
        pb.DispatchTaskRequest(spec=spec, master_session_id="m1"),
        metadata=_bearer_metadata(master_jwt),
    )

    assert resp1.idempotent_hit is False
    assert resp2.idempotent_hit is True
    assert resp1.status == pb.TaskStatus.TASK_STATUS_PENDING
    assert resp2.status == pb.TaskStatus.TASK_STATUS_PENDING


# ---------------------------------------------------------------------------
# 3. Cancel pending -> cancelled without worker
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_cancel_pending_cancelled_without_worker(hub, master_jwt):
    task_id = "e2e-cancel-pending"
    stub = TaskRelayStub(hub.grpc_channel)
    watch_future = asyncio.create_task(
        _watch_terminal(stub, master_jwt, task_id=task_id)
    )

    await stub.DispatchTask(
        pb.DispatchTaskRequest(
            spec=_spec(task_id, "cancel me", "topic-cancel-pending"),
            master_session_id="m1",
        ),
        metadata=_bearer_metadata(master_jwt),
    )
    cancel_resp = await stub.CancelTask(
        pb.CancelTaskRequest(task_id=task_id, reason="no worker available"),
        metadata=_bearer_metadata(master_jwt),
    )

    events = await asyncio.wait_for(watch_future, timeout=5.0)
    assert cancel_resp.cancelled_task_ids == [task_id]
    assert events[-1].kind == pb.TaskEventKind.TASK_EVENT_KIND_TERMINAL
    assert events[-1].result.status == pb.TaskStatus.TASK_STATUS_CANCELLED


# ---------------------------------------------------------------------------
# 4. Cancel running -> cancelled with partial summary
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_cancel_running_cancelled_with_partial_summary(hub, master_jwt):
    task_id = "e2e-cancel-running"
    backend = StubBackend(StubBackendConfig(sleep_seconds=5.0))
    worker, run_task = await _start_worker(hub, backend)
    try:
        stub = TaskRelayStub(hub.grpc_channel)
        watch_future = asyncio.create_task(
            _watch_terminal(stub, master_jwt, task_id=task_id)
        )

        await stub.DispatchTask(
            pb.DispatchTaskRequest(
                spec=_spec(task_id, "long running", "topic-cancel-running"),
                master_session_id="m1",
            ),
            metadata=_bearer_metadata(master_jwt),
        )

        await _wait_for_status(hub.router, task_id, {"running"})
        await stub.CancelTask(
            pb.CancelTaskRequest(task_id=task_id, reason="master requested"),
            metadata=_bearer_metadata(master_jwt),
        )

        events = await asyncio.wait_for(watch_future, timeout=5.0)
        terminal = events[-1]
        assert terminal.kind == pb.TaskEventKind.TASK_EVENT_KIND_TERMINAL
        assert terminal.result.status == pb.TaskStatus.TASK_STATUS_CANCELLED
        assert "cancelled while echoing" in terminal.result.summary
    finally:
        await _stop_worker(worker, run_task)


# ---------------------------------------------------------------------------
# 5. First-progress miss -> lost (short config)
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_first_progress_miss_marks_lost(hub, master_jwt, monkeypatch):
    task_id = "e2e-fp-lost"
    # Tight first-progress window so a silent claimed task expires fast.
    hub.router._config = HubConfig(
        jwt_secret=SECRET,
        queue_timeout_seconds=900,
        first_progress_seconds=0,
        timeout_seconds=600,
        cancel_grace_seconds=60,
        max_attempts=1,
    )

    # Simulate a worker that claims but drops the immediate claim-ack progress
    # without telling the executor it failed, so the backend keeps running while
    # the Hub's first-progress deadline expires.
    orig_progress = TaskExecutor._progress

    async def dropping_progress(self, task_id_arg: str, summary: str) -> None:
        if task_id_arg == task_id:
            return
        return await orig_progress(self, task_id_arg, summary)

    monkeypatch.setattr(TaskExecutor, "_progress", dropping_progress)

    backend = StubBackend(StubBackendConfig(sleep_seconds=10.0))
    worker, run_task = await _start_worker(hub, backend)
    try:
        stub = TaskRelayStub(hub.grpc_channel)
        watch_future = asyncio.create_task(
            _watch_terminal(stub, master_jwt, task_id=task_id)
        )

        await stub.DispatchTask(
            pb.DispatchTaskRequest(
                spec=_spec(task_id, "will be silent", "topic-fp-lost"),
                master_session_id="m1",
            ),
            metadata=_bearer_metadata(master_jwt),
        )

        events = await asyncio.wait_for(watch_future, timeout=5.0)
        terminal = events[-1]
        assert terminal.kind == pb.TaskEventKind.TASK_EVENT_KIND_TERMINAL
        assert terminal.result.status == pb.TaskStatus.TASK_STATUS_LOST
    finally:
        await _stop_worker(worker, run_task)


# ---------------------------------------------------------------------------
# 6. Watch reconnect since_event_id
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_watch_reconnect_since_event_id(hub, master_jwt, backend):
    task_id = "e2e-reconnect"
    worker, run_task = await _start_worker(hub, backend)
    try:
        stub = TaskRelayStub(hub.grpc_channel)

        await stub.DispatchTask(
            pb.DispatchTaskRequest(
                spec=_spec(task_id, "reconnect test", "topic-reconnect"),
                master_session_id="m1",
            ),
            metadata=_bearer_metadata(master_jwt),
        )

        # First watch: consume the initial STATUS event, then close.
        first_event_id = 0
        async with stub.WatchTask.open(
            metadata=_bearer_metadata(master_jwt)
        ) as stream:
            await stream.send_message(
                pb.WatchTaskRequest(task_id=task_id), end=True
            )
            event = await stream.recv_message()
            assert event is not None
            first_event_id = event.event_id

        await _wait_for_status(hub.router, task_id, {"completed"})

        # Reconnect from the cursor of the first delivered event.
        events = await _watch_terminal(
            stub, master_jwt, task_id=task_id, since_event_id=first_event_id
        )
        assert all(e.event_id > first_event_id for e in events)
        assert events[-1].result.status == pb.TaskStatus.TASK_STATUS_COMPLETED
    finally:
        await _stop_worker(worker, run_task)


# ---------------------------------------------------------------------------
# 7. CursorOutOfRange when since too old
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_watch_cursor_out_of_range_since_too_old(hub, master_jwt):
    stub = TaskRelayStub(hub.grpc_channel)
    await stub.DispatchTask(
        pb.DispatchTaskRequest(
            spec=_spec("cursor-old", "cursor old", "cursor-topic"),
            master_session_id="m1",
        ),
        metadata=_bearer_metadata(master_jwt),
    )
    await stub.DispatchTask(
        pb.DispatchTaskRequest(
            spec=_spec("cursor-other", "cursor other", "other-topic"),
            master_session_id="m1",
        ),
        metadata=_bearer_metadata(master_jwt),
    )
    await hub.db._conn.execute(
        "DELETE FROM task_events WHERE callback_topic = 'cursor-topic'"
    )
    await hub.db._conn.commit()

    with pytest.raises(GRPCError) as exc:
        async with stub.WatchTask.open(
            metadata=_bearer_metadata(master_jwt)
        ) as stream:
            await stream.send_message(
                pb.WatchTaskRequest(topic="cursor-topic", since_event_id=1),
                end=True,
            )
            await stream.recv_message()

    assert exc.value.status == Status.FAILED_PRECONDITION
    assert "since_event_id" in exc.value.message


# ---------------------------------------------------------------------------
# 8. Unauthorized WS rejected
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_unauthorized_ws_rejected(hub):
    with pytest.raises(websockets.exceptions.InvalidStatus) as exc:
        async with websockets.connect(hub.ws_url):
            pass
    assert exc.value.response.status_code == 401
