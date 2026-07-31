"""Shared pytest helpers for running E2E tests against the Go Task Relay Hub."""

from __future__ import annotations

import asyncio
import socket
import subprocess
import time
from dataclasses import dataclass
from pathlib import Path

import pytest_asyncio
from grpclib.client import Channel

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.hub.auth import Auth
from extend.task_relay.tests.conftest import AUDIENCE, ISSUER, SECRET

REPO_ROOT = Path(__file__).resolve().parents[3]
HUB_GO = REPO_ROOT / "extend" / "task_relay" / "hub" / "go"
HUB_BIN = HUB_GO / "task-relay-hub"

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
    hub_proc: subprocess.Popen[str]


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _build_go_hub() -> None:
    build = subprocess.run(
        ["go", "build", "-o", "task-relay-hub", "./cmd/task-relay-hub"],
        cwd=HUB_GO,
        check=False,
        capture_output=True,
        text=True,
    )
    if build.returncode != 0:
        raise RuntimeError(build.stderr or build.stdout)


def _start_go_hub(db_path: Path, extra_args: list[str] | None = None) -> GoHubRunner:
    _build_go_hub()
    grpc_port = _free_port()
    ws_port = _free_port()
    args = [
        str(HUB_BIN),
        f"--host=127.0.0.1",
        f"--grpc-port={grpc_port}",
        f"--ws-port={ws_port}",
        f"--db={db_path}",
        f"--jwt-secret={SECRET}",
    ]
    if extra_args:
        args.extend(extra_args)
    proc = subprocess.Popen(
        args,
        cwd=HUB_GO,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
        text=True,
    )
    deadline = time.monotonic() + 5.0
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            err = proc.stderr.read() if proc.stderr else ""
            raise RuntimeError(f"go hub exited early: {err}")
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
            if sock.connect_ex(("127.0.0.1", grpc_port)) == 0:
                break
        time.sleep(0.05)
    else:
        proc.kill()
        raise RuntimeError("go hub did not become ready")

    auth = Auth(secret=SECRET, issuer=ISSUER, audience=AUDIENCE)
    master_jwt = auth.issue_master_jwt("go-hub-e2e-master", ttl_s=3600)
    return GoHubRunner(
        grpc_channel=Channel(host="127.0.0.1", port=grpc_port),
        ws_url=f"ws://127.0.0.1:{ws_port}",
        master_jwt=master_jwt,
        hub_proc=proc,
    )


def _stop_go_hub(runner: GoHubRunner) -> None:
    runner.grpc_channel.close()
    runner.hub_proc.terminate()
    try:
        runner.hub_proc.wait(timeout=3.0)
    except subprocess.TimeoutExpired:
        runner.hub_proc.kill()
        runner.hub_proc.wait(timeout=2.0)


def bearer_metadata(token: str) -> dict[str, str]:
    return {"authorization": f"Bearer {token}"}


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


@pytest_asyncio.fixture
async def go_hub(tmp_path):
    db_path = tmp_path / "relay.db"
    runner = _start_go_hub(db_path)
    try:
        yield runner
    finally:
        _stop_go_hub(runner)
