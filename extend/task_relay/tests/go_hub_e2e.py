"""Shared helpers for Go Hub gRPC + WebSocket E2E tests."""

from __future__ import annotations

import asyncio
import hashlib
import hmac
import json
import time
from typing import Any

import websockets
from grpclib.client import Channel
from grpclib.const import Status
from grpclib.exceptions import GRPCError

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.tests.conftest import SECRET, make_worker_jwt
from extend.task_relay.tests.live_hub import LiveHub, bearer_metadata


def jsonrpc_request(msg_id: Any, method: str, params: dict | None = None) -> str:
    return json.dumps(
        {"jsonrpc": "2.0", "id": msg_id, "method": method, "params": params or {}},
        separators=(",", ":"),
    )


async def ws_recv_result(ws) -> dict:
    payload = json.loads(await ws.recv())
    assert payload.get("jsonrpc") == "2.0"
    assert "error" not in payload, payload.get("error")
    return payload["result"]


async def ws_connect(hub: LiveHub, worker_id: str = "w1", **announce_extra: Any):
    jwt = make_worker_jwt(worker_id, max_concurrent=announce_extra.pop("max_concurrent", 1))
    ws = await websockets.connect(
        hub.ws_url, additional_headers={"Authorization": f"Bearer {jwt}"}
    )
    params: dict[str, Any] = {
        "worker_id": worker_id,
        "session_modes": announce_extra.pop("session_modes", ["a"]),
        "max_concurrent": announce_extra.pop("max_concurrent", 1),
        "toolsets": announce_extra.pop("toolsets", []),
    }
    params.update(announce_extra)
    await ws.send(jsonrpc_request(1, "worker.announce", params))
    await ws_recv_result(ws)
    return ws, jwt


def issue_wake_token(task_id: str, worker_id: str, expires_at: int | None = None) -> tuple[str, int]:
    exp = expires_at or int(time.time()) + 60
    payload = f"{task_id}:{worker_id}:{exp}"
    token = hmac.new(SECRET.encode(), payload.encode(), hashlib.sha256).hexdigest()
    return token, exp


def task_spec(
    task_id: str,
    goal: str,
    callback_topic: str = "topic-1",
    **kwargs: Any,
) -> pb.TaskSpec:
    spec = pb.TaskSpec(task_id=task_id, goal=goal, callback_topic=callback_topic)
    for toolset in kwargs.pop("toolsets", []):
        spec.toolsets.append(toolset)
    if "depends_on" in kwargs:
        spec.depends_on.extend(kwargs.pop("depends_on"))
    if "min_cpu_cores" in kwargs or "min_memory_gb" in kwargs:
        spec.min_resources.min_cpu_cores = kwargs.pop("min_cpu_cores", 0)
        spec.min_resources.min_memory_gb = kwargs.pop("min_memory_gb", 0)
    for key, value in kwargs.items():
        if hasattr(spec, key):
            setattr(spec, key, value)
    return spec


async def dispatch_task(
    hub: LiveHub,
    spec: pb.TaskSpec,
    *,
    allow_redispatch: bool = False,
    master_session_id: str = "m1",
) -> pb.DispatchTaskResponse:
    stub = TaskRelayStub(hub.grpc_channel)
    return await stub.DispatchTask(
        pb.DispatchTaskRequest(
            spec=spec,
            master_session_id=master_session_id,
            allow_redispatch=allow_redispatch,
        ),
        metadata=bearer_metadata(hub.master_jwt),
    )


async def dispatch_batch(
    hub: LiveHub,
    batch_id: str,
    specs: list[pb.TaskSpec],
    callback_topic: str = "batch-topic",
    policy: pb.BatchPolicy | None = None,
    *,
    allow_redispatch: bool = False,
) -> pb.DispatchTaskBatchResponse:
    stub = TaskRelayStub(hub.grpc_channel)
    req = pb.DispatchTaskBatchRequest(
        batch_id=batch_id,
        specs=specs,
        master_session_id="m1",
        callback_topic=callback_topic,
        allow_redispatch=allow_redispatch,
    )
    if policy is not None:
        req.policy.CopyFrom(policy)
    return await stub.DispatchTaskBatch(req, metadata=bearer_metadata(hub.master_jwt))


async def ws_poll(
    ws,
    msg_id: int = 2,
    *,
    atomic: bool = True,
    max_tasks: int = 1,
    max_wait_ms: int = 500,
) -> dict:
    await ws.send(
        jsonrpc_request(
            msg_id,
            "worker.poll",
            {
                "max_wait_ms": max_wait_ms,
                "max_tasks": max_tasks,
                "prefer_atomic_claim": atomic,
            },
        )
    )
    return await ws_recv_result(ws)


async def ws_complete(ws, task_id: str, status: str = "completed", summary: str = "done", msg_id: int = 3):
    await ws.send(
        jsonrpc_request(
            msg_id,
            "task.complete",
            {"task_id": task_id, "status": status, "summary": summary},
        )
    )
    return await ws_recv_result(ws)


async def ws_nack(ws, task_id: str, claim_token: str, msg_id: int = 4):
    await ws.send(
        jsonrpc_request(
            msg_id,
            "worker.nack",
            {"task_id": task_id, "claim_token": claim_token},
        )
    )
    return await ws_recv_result(ws)


async def get_task_status(hub: LiveHub, task_id: str) -> str:
    stub = TaskRelayStub(hub.grpc_channel)
    result = await stub.GetTaskResult(
        pb.TaskResultRequest(task_id=task_id),
        metadata=bearer_metadata(hub.master_jwt),
    )
    return pb.TaskStatus.Name(result.status).removeprefix("TASK_STATUS_").lower()


async def watch_collect(
    hub: LiveHub,
    *,
    task_id: str | None = None,
    topic: str | None = None,
    batch_id: str | None = None,
    since_event_id: int = 0,
    max_events: int = 10,
    timeout: float = 5.0,
) -> list[pb.TaskEvent]:
    stub = TaskRelayStub(hub.grpc_channel)
    req = pb.WatchTaskRequest(since_event_id=since_event_id)
    if task_id:
        req.task_id = task_id
    elif topic:
        req.topic = topic
    elif batch_id:
        req.batch_id = batch_id
    events: list[pb.TaskEvent] = []
    deadline = asyncio.get_event_loop().time() + timeout
    async with stub.WatchTask.open(metadata=bearer_metadata(hub.master_jwt)) as stream:
        await stream.send_message(req, end=True)
        while len(events) < max_events and asyncio.get_event_loop().time() < deadline:
            try:
                event = await asyncio.wait_for(stream.recv_message(), timeout=1.0)
            except asyncio.TimeoutError:
                break
            if event is None:
                break
            events.append(event)
            if event.kind == pb.TaskEventKind.TASK_EVENT_KIND_TERMINAL:
                break
    return events


async def watch_until_terminal(hub: LiveHub, task_id: str, timeout: float = 8.0) -> list[pb.TaskEvent]:
    stub = TaskRelayStub(hub.grpc_channel)
    events: list[pb.TaskEvent] = []
    deadline = asyncio.get_event_loop().time() + timeout
    async with stub.WatchTask.open(metadata=bearer_metadata(hub.master_jwt)) as stream:
        await stream.send_message(pb.WatchTaskRequest(task_id=task_id), end=True)
        while asyncio.get_event_loop().time() < deadline:
            event = await asyncio.wait_for(stream.recv_message(), timeout=timeout)
            if event is None:
                break
            events.append(event)
            if event.kind == pb.TaskEventKind.TASK_EVENT_KIND_TERMINAL:
                break
    return events
