"""Tests for the Master-facing gRPC TaskRelay service (M1)."""

from __future__ import annotations

import asyncio

import pytest
import pytest_asyncio
from grpclib.client import Channel
from grpclib.const import Status
from grpclib.exceptions import GRPCError

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.hub.auth import Auth
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.grpc_server import _event_to_proto, serve_grpc
from extend.task_relay.hub.models import TaskEvent, TaskSpec
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.worker_registry import WorkerRegistry

SECRET = "t" * 32
ISSUER = "hermes-relay-hub"
AUDIENCE = "task-relay-hub"


def make_auth(**kwargs) -> Auth:
    defaults = dict(secret=SECRET, issuer=ISSUER, audience=AUDIENCE)
    defaults.update(kwargs)
    return Auth(**defaults)


@pytest_asyncio.fixture
async def db(tmp_path):
    conn = await open_db(str(tmp_path / "grpc.db"))
    yield conn
    await conn.close()


@pytest_asyncio.fixture
async def bus(db):
    return EventBus(db, HubConfig())


@pytest_asyncio.fixture
async def registry(db):
    return WorkerRegistry(db)


@pytest_asyncio.fixture
async def router(db, bus, registry):
    cfg = HubConfig(
        jwt_secret=SECRET,
        queue_timeout_seconds=900,
        first_progress_seconds=120,
        timeout_seconds=600,
        cancel_grace_seconds=60,
        max_attempts=1,
        list_tasks_default_limit=10,
        list_tasks_max_limit=50,
    )
    return TaskRouter(db, bus, cfg, registry)


@pytest_asyncio.fixture
async def auth():
    return make_auth()


@pytest.fixture
def master_jwt(auth):
    return auth.issue_master_jwt("master-01", ttl_s=3600)


@pytest_asyncio.fixture
async def grpc_server(router, auth, db, bus, registry):
    server = await serve_grpc(
        router, auth, router._config, db, bus, registry, host="127.0.0.1", port=0
    )
    yield server
    server.close()
    await server.wait_closed()


@pytest_asyncio.fixture
async def grpc_channel(grpc_server):
    port = grpc_server._server.sockets[0].getsockname()[1]
    channel = Channel(host="127.0.0.1", port=port)
    yield channel
    channel.close()


def _spec(task_id="t1", goal="ping", callback_topic="topic-1") -> pb.TaskSpec:
    spec = pb.TaskSpec(task_id=task_id, goal=goal, callback_topic=callback_topic)
    spec.toolsets.append("terminal")
    return spec


def _bearer_metadata(token: str):
    return {"authorization": f"Bearer {token}"}


async def _claim_task(router, registry, task_id: str, worker_id: str = "w1"):
    """Simulate a Mode-A worker claiming a pending task."""
    await registry.announce(
        worker_id,
        toolsets=["terminal"],
        status="idle",
        max_concurrent=1,
    )
    claimed = await router.atomic_claim_for_poll(worker_id, max_tasks=1)
    assert len(claimed) == 1
    assert claimed[0].task_id == task_id
    return claimed[0]


@pytest_asyncio.fixture
async def router_via_worker_complete(router, registry):
    """Dispatch and complete a task through the normal worker path."""
    task_id = "completed-by-worker"
    topic = "topic-completed"
    await router.dispatch_task(
        TaskSpec(task_id=task_id, goal="g", callback_topic=topic),
        master_session_id="m1",
    )
    await _claim_task(router, registry, task_id)
    await router.on_complete(
        task_id,
        status="completed",
        summary="done",
        result_json='{"answer": 42}',
    )
    return task_id, topic


# ---------------------------------------------------------------------------
# Auth
# ---------------------------------------------------------------------------


RPC_METHODS = [
    "DispatchTask",
    "DispatchTaskBatch",
    "GetTaskResult",
    "WatchTask",
    "ListWorkers",
    "ListTasks",
    "CancelTask",
]


async def _call_rpc(stub: TaskRelayStub, method: str, metadata) -> None:
    if method == "DispatchTask":
        await stub.DispatchTask(
            pb.DispatchTaskRequest(spec=_spec()), metadata=metadata
        )
    elif method == "DispatchTaskBatch":
        await stub.DispatchTaskBatch(
            pb.DispatchTaskBatchRequest(batch_id="b", specs=[_spec()]),
            metadata=metadata,
        )
    elif method == "GetTaskResult":
        await stub.GetTaskResult(
            pb.TaskResultRequest(task_id="t"), metadata=metadata
        )
    elif method == "WatchTask":
        async with stub.WatchTask.open(metadata=metadata) as stream:
            await stream.send_message(pb.WatchTaskRequest(task_id="t"), end=True)
            try:
                await asyncio.wait_for(stream.recv_message(), timeout=1.0)
            except asyncio.TimeoutError:
                pytest.fail(f"{method} did not reject unauthenticated request")
    elif method == "ListWorkers":
        await stub.ListWorkers(pb.ListWorkersRequest(), metadata=metadata)
    elif method == "ListTasks":
        await stub.ListTasks(pb.ListTasksRequest(), metadata=metadata)
    elif method == "CancelTask":
        await stub.CancelTask(
            pb.CancelTaskRequest(task_id="t"), metadata=metadata
        )
    else:
        raise ValueError(method)


@pytest.mark.asyncio
async def test_dispatch_requires_authorization(grpc_channel):
    stub = TaskRelayStub(grpc_channel)
    with pytest.raises(GRPCError) as exc:
        await stub.DispatchTask(pb.DispatchTaskRequest(spec=_spec()))
    assert exc.value.status == Status.UNAUTHENTICATED


@pytest.mark.asyncio
async def test_dispatch_rejects_worker_jwt(grpc_channel, auth):
    worker_token = auth.issue_worker_jwt("w1", ["terminal"], max_concurrent=1)
    stub = TaskRelayStub(grpc_channel)
    with pytest.raises(GRPCError) as exc:
        await stub.DispatchTask(
            pb.DispatchTaskRequest(spec=_spec()),
            metadata=_bearer_metadata(worker_token),
        )
    assert exc.value.status == Status.UNAUTHENTICATED


@pytest.mark.asyncio
async def test_dispatch_rejects_invalid_token(grpc_channel, master_jwt):
    stub = TaskRelayStub(grpc_channel)
    with pytest.raises(GRPCError) as exc:
        await stub.DispatchTask(
            pb.DispatchTaskRequest(spec=_spec()),
            metadata={"authorization": "Bearer not-a-token"},
        )
    assert exc.value.status == Status.UNAUTHENTICATED


@pytest.mark.parametrize("method", RPC_METHODS)
@pytest.mark.parametrize("case", ["missing", "invalid_bearer", "bare_token"])
@pytest.mark.asyncio
async def test_auth_interceptor_rejects_all_rpcs(method, case, grpc_channel, master_jwt):
    stub = TaskRelayStub(grpc_channel)
    if case == "missing":
        metadata = None
    elif case == "invalid_bearer":
        metadata = {"authorization": "Bearer invalid-token"}
    else:
        # A valid token without the required "Bearer " scheme must be rejected.
        metadata = {"authorization": master_jwt}
    with pytest.raises(GRPCError) as exc:
        await _call_rpc(stub, method, metadata)
    assert exc.value.status == Status.UNAUTHENTICATED


# ---------------------------------------------------------------------------
# Dispatch + watch terminal
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_watch_receives_terminal(grpc_channel, master_jwt, router_via_worker_complete):
    task_id, topic = router_via_worker_complete
    stub = TaskRelayStub(grpc_channel)

    events: list[pb.TaskEvent] = []
    async with stub.WatchTask.open(metadata=_bearer_metadata(master_jwt)) as stream:
        await stream.send_message(pb.WatchTaskRequest(task_id=task_id), end=True)
        while True:
            event = await stream.recv_message()
            if event is None:
                break
            events.append(event)
            if event.kind == pb.TaskEventKind.TASK_EVENT_KIND_TERMINAL:
                break

    assert len(events) >= 2
    assert events[0].kind == pb.TaskEventKind.TASK_EVENT_KIND_STATUS
    assert events[-1].kind == pb.TaskEventKind.TASK_EVENT_KIND_TERMINAL
    assert events[-1].result.status == pb.TaskStatus.TASK_STATUS_COMPLETED
    assert events[-1].result.summary == "done"


def test_event_to_proto_maps_checkpoint_payload():
    event = TaskEvent(
        event_id=7,
        callback_topic="topic",
        task_id="t1",
        batch_id="b1",
        kind="CHECKPOINT",
        payload_json='{"checkpoint_id":"ck1","summary":"halfway","fields_json":"{\\"version\\":1,\\"metrics\\":[{\\"name\\":\\"m1\\",\\"value\\":1.0}]}"}',
        event_at=1.0,
    )
    proto = _event_to_proto(event)
    assert proto.kind == pb.TaskEventKind.TASK_EVENT_KIND_CHECKPOINT
    assert proto.checkpoint.task_id == "t1"
    assert proto.checkpoint.checkpoint_id == "ck1"
    assert proto.checkpoint.summary == "halfway"
    assert proto.checkpoint.fields.version == 1
    assert len(proto.checkpoint.fields.metrics) == 1
    assert proto.checkpoint.fields.metrics[0].name == "m1"


@pytest.mark.asyncio
async def test_dispatch_task_batch(grpc_channel, master_jwt, router):
    stub = TaskRelayStub(grpc_channel)
    spec1 = _spec(task_id="b1-t1", goal="g1", callback_topic="batch-topic")
    spec2 = _spec(task_id="b1-t2", goal="g2", callback_topic="batch-topic")
    resp = await stub.DispatchTaskBatch(
        pb.DispatchTaskBatchRequest(
            batch_id="batch-1",
            specs=[spec1, spec2],
            master_session_id="m1",
            callback_topic="batch-topic",
        ),
        metadata=_bearer_metadata(master_jwt),
    )
    assert resp.batch_id == "batch-1"
    assert len(resp.tasks) == 2
    assert all(t.status == pb.TaskStatus.TASK_STATUS_PENDING for t in resp.tasks)


# ---------------------------------------------------------------------------
# GetTaskResult + ListTasks
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_get_task_result_returns_terminal(grpc_channel, master_jwt, router, registry):
    task_id = "result-task"
    stub = TaskRelayStub(grpc_channel)
    await stub.DispatchTask(
        pb.DispatchTaskRequest(
            spec=_spec(task_id=task_id, callback_topic="r-topic"),
            master_session_id="m1",
        ),
        metadata=_bearer_metadata(master_jwt),
    )
    await _claim_task(router, registry, task_id)
    await router.on_complete(
        task_id,
        status="completed",
        summary="all good",
        result_json='{"output": "hello"}',
        usage_json='{"prompt_tokens": 10, "completion_tokens": 5}',
    )

    result = await stub.GetTaskResult(
        pb.TaskResultRequest(task_id=task_id),
        metadata=_bearer_metadata(master_jwt),
    )
    assert result.task_id == task_id
    assert result.status == pb.TaskStatus.TASK_STATUS_COMPLETED
    assert result.summary == "all good"
    assert result.result_text == '{"output": "hello"}'
    assert result.usage.total_tokens == 15


@pytest.mark.asyncio
async def test_list_tasks_clamps_limit(grpc_channel, master_jwt):
    stub = TaskRelayStub(grpc_channel)
    for i in range(5):
        await stub.DispatchTask(
            pb.DispatchTaskRequest(
                spec=_spec(task_id=f"lt-{i}", callback_topic="lt-topic"),
                master_session_id="m1",
            ),
            metadata=_bearer_metadata(master_jwt),
        )

    # Above max limit should be clamped down; zero/negative should clamp up.
    resp_large = await stub.ListTasks(
        pb.ListTasksRequest(callback_topic="lt-topic", limit=200),
        metadata=_bearer_metadata(master_jwt),
    )
    assert len(resp_large.tasks) == 5

    resp_zero = await stub.ListTasks(
        pb.ListTasksRequest(callback_topic="lt-topic", limit=0),
        metadata=_bearer_metadata(master_jwt),
    )
    assert len(resp_zero.tasks) == 5


# ---------------------------------------------------------------------------
# CancelTask
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_cancel_pending_task(grpc_channel, master_jwt, router):
    task_id = "cancel-task"
    stub = TaskRelayStub(grpc_channel)
    await stub.DispatchTask(
        pb.DispatchTaskRequest(
            spec=_spec(task_id=task_id, callback_topic="c-topic"),
            master_session_id="m1",
        ),
        metadata=_bearer_metadata(master_jwt),
    )
    resp = await stub.CancelTask(
        pb.CancelTaskRequest(task_id=task_id, reason="test cancel"),
        metadata=_bearer_metadata(master_jwt),
    )
    assert resp.cancelled_task_ids == [task_id]
    assert resp.already_terminal_task_ids == []
    assert await router.get_status(task_id) == "cancelled"


@pytest.mark.asyncio
async def test_cancel_unknown_task_returns_not_found(grpc_channel, master_jwt):
    stub = TaskRelayStub(grpc_channel)
    with pytest.raises(GRPCError) as exc:
        await stub.CancelTask(
            pb.CancelTaskRequest(task_id="no-such-task"),
            metadata=_bearer_metadata(master_jwt),
        )
    assert exc.value.status == Status.NOT_FOUND
    assert "no-such-task" in exc.value.message


# ---------------------------------------------------------------------------
# ListWorkers
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_list_workers_filters_toolsets(grpc_channel, master_jwt, registry):
    await registry.announce(
        "w-terminal",
        toolsets=["terminal"],
        status="idle",
    )
    await registry.announce(
        "w-file",
        toolsets=["file"],
        status="idle",
    )

    stub = TaskRelayStub(grpc_channel)
    resp = await stub.ListWorkers(
        pb.ListWorkersRequest(require_toolsets=["terminal"]),
        metadata=_bearer_metadata(master_jwt),
    )
    ids = {w.worker_id for w in resp.workers}
    assert ids == {"w-terminal"}


# ---------------------------------------------------------------------------
# WatchTask cursor errors
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_watch_cursor_out_of_range(grpc_channel, master_jwt, db):
    stub = TaskRelayStub(grpc_channel)
    # Create two tasks so the global event log has a floor. Delete the events
    # for the watched topic; the cursor pointing at the pruned gap is older
    # than the global floor and must fail fast with FAILED_PRECONDITION.
    await stub.DispatchTask(
        pb.DispatchTaskRequest(
            spec=_spec(task_id="cursor-task", callback_topic="cursor-topic"),
            master_session_id="m1",
        ),
        metadata=_bearer_metadata(master_jwt),
    )
    await stub.DispatchTask(
        pb.DispatchTaskRequest(
            spec=_spec(task_id="other-task", callback_topic="other-topic"),
            master_session_id="m1",
        ),
        metadata=_bearer_metadata(master_jwt),
    )
    await db._conn.execute("DELETE FROM task_events WHERE callback_topic = 'cursor-topic'")
    await db._conn.commit()

    with pytest.raises(GRPCError) as exc:
        async with stub.WatchTask.open(metadata=_bearer_metadata(master_jwt)) as stream:
            await stream.send_message(
                pb.WatchTaskRequest(topic="cursor-topic", since_event_id=1),
                end=True,
            )
            await stream.recv_message()
    assert exc.value.status == Status.FAILED_PRECONDITION
    # Detail is encoded when google.rpc is available; assert message contains cursor info.
    assert "since_event_id" in exc.value.message
