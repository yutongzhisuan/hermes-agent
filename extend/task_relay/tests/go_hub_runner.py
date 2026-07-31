"""Shared pytest helpers for running E2E tests against the Go Task Relay Hub."""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from pathlib import Path

import pytest_asyncio
from grpclib.client import Channel

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.tests.live_hub import (
    HubLaunchConfig,
    LiveHub,
    bearer_metadata,
    start_live_hub,
    stop_live_hub,
    wait_for_task_status,
)

_STATUS_TO_NAME = {
    pb.TaskStatus.TASK_STATUS_PENDING: "pending",
    pb.TaskStatus.TASK_STATUS_RUNNING: "running",
    pb.TaskStatus.TASK_STATUS_COMPLETED: "completed",
    pb.TaskStatus.TASK_STATUS_FAILED: "failed",
    pb.TaskStatus.TASK_STATUS_LOST: "lost",
    pb.TaskStatus.TASK_STATUS_CANCELLED: "cancelled",
}


@dataclass(frozen=True)
class GoHubRunner:
    """Live Go Hub runtime exposed to cross-hub E2E tests."""

    grpc_channel: Channel
    ws_url: str
    master_jwt: str
    live: LiveHub


@pytest_asyncio.fixture
async def go_hub(tmp_path):
    """Start Go Hub regardless of TASK_RELAY_HUB (explicit go-only tests)."""
    prev = __import__("os").environ.get("TASK_RELAY_HUB")
    __import__("os").environ["TASK_RELAY_HUB"] = "go"
    try:
        live = await start_live_hub(tmp_path, HubLaunchConfig())
        runner = GoHubRunner(
            grpc_channel=live.grpc_channel,
            ws_url=live.ws_url,
            master_jwt=live.master_jwt,
            live=live,
        )
        try:
            yield runner
        finally:
            await stop_live_hub(live)
    finally:
        if prev is None:
            __import__("os").environ.pop("TASK_RELAY_HUB", None)
        else:
            __import__("os").environ["TASK_RELAY_HUB"] = prev


async def wait_for_status(
    stub: TaskRelayStub,
    master_jwt: str,
    task_id: str,
    statuses: set[str],
    timeout: float = 8.0,
) -> str:
    deadline = asyncio.get_event_loop().time() + timeout
    while asyncio.get_event_loop().time() < deadline:
        result = await stub.GetTaskResult(
            pb.TaskResultRequest(task_id=task_id),
            metadata=bearer_metadata(master_jwt),
        )
        name = _STATUS_TO_NAME.get(result.status, "")
        if name in statuses:
            return name
        await asyncio.sleep(0.05)
    raise AssertionError(
        f"task {task_id} did not reach any of {statuses} within {timeout}s"
    )
