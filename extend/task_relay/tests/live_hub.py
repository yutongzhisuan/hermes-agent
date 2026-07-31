"""Start a live Task Relay Hub (Python in-process or Go binary) for conformance tests."""

from __future__ import annotations

import asyncio
import os
import socket
import sqlite3
import subprocess
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import pytest_asyncio
from grpclib.client import Channel

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.hub.auth import Auth
from extend.task_relay.hub.bootstrap import start_ws_server, wire_orchestration
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.grpc_server import serve_grpc
from extend.task_relay.hub.models import TaskSpec
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.tests.conftest import AUDIENCE, ISSUER, SECRET, hub_backend, make_auth


def is_go_hub() -> bool:
    return hub_backend() == "go"


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


def bearer_metadata(token: str) -> dict[str, str]:
    return {"authorization": f"Bearer {token}"}


@dataclass
class LiveHub:
    """Unified live Hub handle for E2E and integration tests."""

    backend: str
    grpc_channel: Channel
    ws_url: str
    master_jwt: str
    auth: Auth
    db_path: Path | None = None
    router: Any | None = None
    db: Any | None = None
    registry: Any | None = None
    http_url: str | None = None
    metrics_url: str | None = None
    _cleanup: Any = field(default=None, repr=False)


@dataclass(frozen=True)
class HubLaunchConfig:
    queue_timeout_seconds: int = 900
    first_progress_seconds: int = 120
    timeout_seconds: int = 600
    cancel_grace_seconds: int = 60
    max_attempts: int = 1
    watch_stream_buffer_events: int = 1024
    encrypt_inline_context: bool = False
    require_signed_context_ref: bool = False
    bootstrap_tokens: str = ""
    metrics_port: int = 0
    extra_go_args: list[str] | None = None


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


def _start_go_hub(db_path: Path, cfg: HubLaunchConfig) -> LiveHub:
    _build_go_hub()
    grpc_port = _free_port()
    ws_port = _free_port()
    http_port = _free_port()
    args = [
        str(HUB_BIN),
        "--host=127.0.0.1",
        f"--grpc-port={grpc_port}",
        f"--ws-port={ws_port}",
        f"--http-port={http_port}",
        f"--db={db_path}",
        f"--jwt-secret={SECRET}",
        f"--queue-timeout-seconds={cfg.queue_timeout_seconds}",
        f"--first-progress-seconds={cfg.first_progress_seconds}",
        f"--timeout-seconds={cfg.timeout_seconds}",
        f"--cancel-grace-seconds={cfg.cancel_grace_seconds}",
        f"--max-attempts={cfg.max_attempts}",
        f"--watch-stream-buffer-events={cfg.watch_stream_buffer_events}",
    ]
    if cfg.encrypt_inline_context:
        args.append("--encrypt-inline-context")
    if cfg.require_signed_context_ref:
        args.append("--require-signed-context-ref")
    if cfg.bootstrap_tokens:
        args.append(f"--bootstrap-tokens={cfg.bootstrap_tokens}")
    if cfg.metrics_port > 0:
        args.append(f"--metrics-port={cfg.metrics_port}")
    if cfg.extra_go_args:
        args.extend(cfg.extra_go_args)
    proc = subprocess.Popen(
        args,
        cwd=HUB_GO,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
        text=True,
    )
    deadline = time.monotonic() + 8.0
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
    master_jwt = auth.issue_master_jwt("live-hub-master", ttl_s=3600)

    def cleanup() -> None:
        proc.terminate()
        try:
            proc.wait(timeout=3.0)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=2.0)

    metrics_url = None
    if cfg.metrics_port > 0:
        metrics_url = f"http://127.0.0.1:{cfg.metrics_port}"

    return LiveHub(
        backend="go",
        grpc_channel=Channel(host="127.0.0.1", port=grpc_port),
        ws_url=f"ws://127.0.0.1:{ws_port}",
        master_jwt=master_jwt,
        auth=auth,
        db_path=db_path,
        http_url=f"http://127.0.0.1:{http_port}",
        metrics_url=metrics_url,
        _cleanup=cleanup,
    )


async def _start_python_hub(db_path: Path, cfg: HubLaunchConfig) -> LiveHub:
    conn = await open_db(str(db_path))
    hub_cfg = HubConfig(
        jwt_secret=SECRET,
        queue_timeout_seconds=cfg.queue_timeout_seconds,
        first_progress_seconds=cfg.first_progress_seconds,
        timeout_seconds=cfg.timeout_seconds,
        cancel_grace_seconds=cfg.cancel_grace_seconds,
        max_attempts=cfg.max_attempts,
        watch_stream_buffer_events=cfg.watch_stream_buffer_events,
        encrypt_inline_context_at_rest=cfg.encrypt_inline_context,
        require_signed_context_ref=cfg.require_signed_context_ref,
    )
    bus = EventBus(conn, hub_cfg)
    registry = WorkerRegistry(conn)
    router = TaskRouter(conn, bus, hub_cfg, registry)
    wire_orchestration(router, conn, bus)
    auth = make_auth()

    grpc_server = await serve_grpc(
        router, auth, hub_cfg, conn, bus, registry, host="127.0.0.1", port=0
    )
    ws_server = await start_ws_server(
        router, auth, registry, conn, hub_cfg, host="127.0.0.1", port=0
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
    master_jwt = auth.issue_master_jwt("live-hub-master", ttl_s=3600)

    async def cleanup_async() -> None:
        shutdown.set()
        await ticker_task
        channel.close()
        ws_server.close()
        grpc_server.close()
        await ws_server.wait_closed()
        await grpc_server.wait_closed()
        await conn.close()

    def cleanup() -> None:
        asyncio.get_event_loop().run_until_complete(cleanup_async())

    return LiveHub(
        backend="python",
        grpc_channel=channel,
        ws_url=f"ws://127.0.0.1:{ws_port}",
        master_jwt=master_jwt,
        auth=auth,
        db_path=db_path,
        router=router,
        db=conn,
        registry=registry,
        _cleanup=cleanup_async,
    )


async def start_live_hub(tmp_path: Path, cfg: HubLaunchConfig | None = None) -> LiveHub:
    """Start Python or Go Hub based on TASK_RELAY_HUB / HUB env."""
    launch = cfg or HubLaunchConfig()
    db_path = tmp_path / "relay.db"
    if is_go_hub():
        return _start_go_hub(db_path, launch)
    return await _start_python_hub(db_path, launch)


async def stop_live_hub(hub: LiveHub) -> None:
    hub.grpc_channel.close()
    cleanup = hub._cleanup
    if cleanup is None:
        return
    if asyncio.iscoroutinefunction(cleanup):
        await cleanup()
    else:
        cleanup()


async def wait_for_task_status(
    hub: LiveHub,
    task_id: str,
    statuses: set[str],
    timeout: float = 8.0,
) -> str:
    """Poll task status via router (python) or gRPC (go)."""
    if hub.router is not None:
        deadline = asyncio.get_event_loop().time() + timeout
        while asyncio.get_event_loop().time() < deadline:
            status = await hub.router.get_status(task_id)
            if status in statuses:
                return status
            await asyncio.sleep(0.05)
        raise AssertionError(
            f"task {task_id} did not reach any of {statuses} within {timeout}s"
        )

    stub = TaskRelayStub(hub.grpc_channel)
    deadline = asyncio.get_event_loop().time() + timeout
    while asyncio.get_event_loop().time() < deadline:
        result = await stub.GetTaskResult(
            pb.TaskResultRequest(task_id=task_id),
            metadata=bearer_metadata(hub.master_jwt),
        )
        name = _STATUS_TO_NAME.get(result.status, "")
        if name in statuses:
            return name
        await asyncio.sleep(0.05)
    raise AssertionError(
        f"task {task_id} did not reach any of {statuses} within {timeout}s"
    )


async def dispatch_task_spec(
    hub: LiveHub,
    spec: TaskSpec,
    master_session_id: str = "m1",
) -> None:
    """Dispatch a Python TaskSpec via router or gRPC."""
    if hub.router is not None:
        await hub.router.dispatch_task(spec, master_session_id)
        return
    stub = TaskRelayStub(hub.grpc_channel)
    proto = pb.TaskSpec(
        task_id=spec.task_id,
        goal=spec.goal,
        callback_topic=spec.callback_topic or "default",
    )
    if spec.toolsets_json:
        import json

        for toolset in json.loads(spec.toolsets_json):
            proto.toolsets.append(toolset)
    if spec.timeout_seconds is not None:
        proto.timeout_seconds = spec.timeout_seconds
    if spec.queue_timeout_seconds is not None:
        proto.queue_timeout_seconds = spec.queue_timeout_seconds
    if spec.first_progress_seconds is not None:
        proto.first_progress_seconds = spec.first_progress_seconds
    if spec.max_attempts is not None:
        proto.max_attempts = spec.max_attempts
    if spec.allowed_worker_ids_json:
        proto.allowed_worker_ids_json = spec.allowed_worker_ids_json
    if spec.deny_worker_ids_json:
        proto.deny_worker_ids_json = spec.deny_worker_ids_json
    if spec.min_resources_json:
        proto.min_resources_json = spec.min_resources_json
    if spec.params_json:
        proto.params_json = spec.params_json
    if spec.context_json:
        proto.context_json = spec.context_json
    await stub.DispatchTask(
        pb.DispatchTaskRequest(spec=proto, master_session_id=master_session_id),
        metadata=bearer_metadata(hub.master_jwt),
    )


def delete_task_events_for_topic(hub: LiveHub, topic: str) -> None:
    """Delete persisted events for a callback topic (cursor out-of-range tests)."""
    if hub.db is not None:
        raise RuntimeError("use hub.db directly for python hub")
    if hub.db_path is None:
        raise RuntimeError("go hub db path unavailable")
    conn = sqlite3.connect(hub.db_path)
    try:
        conn.execute("DELETE FROM task_events WHERE callback_topic = ?", (topic,))
        conn.commit()
    finally:
        conn.close()


def read_audit_log_count(hub: LiveHub, task_id: str) -> int:
    if hub.db_path is None:
        raise RuntimeError("db path unavailable")
    conn = sqlite3.connect(hub.db_path)
    try:
        row = conn.execute(
            "SELECT COUNT(*) FROM audit_log WHERE task_id = ?", (task_id,)
        ).fetchone()
        return int(row[0]) if row else 0
    finally:
        conn.close()


def query_sql(hub: LiveHub, sql: str, params: tuple = ()) -> list[dict[str, Any]]:
    if hub.db_path is None:
        raise RuntimeError("db path unavailable")
    conn = sqlite3.connect(hub.db_path)
    try:
        conn.row_factory = sqlite3.Row
        rows = conn.execute(sql, params).fetchall()
        return [dict(row) for row in rows]
    finally:
        conn.close()


def read_task_row(hub: LiveHub, task_id: str) -> dict[str, Any]:
    """Read a task row from SQLite (Go hub tests)."""
    if hub.db is not None:
        raise RuntimeError("use hub.db.get_task for python hub")
    if hub.db_path is None:
        raise RuntimeError("go hub db path unavailable")
    conn = sqlite3.connect(hub.db_path)
    try:
        conn.row_factory = sqlite3.Row
        row = conn.execute("SELECT * FROM tasks WHERE task_id = ?", (task_id,)).fetchone()
        if row is None:
            raise KeyError(task_id)
        return dict(row)
    finally:
        conn.close()


@pytest_asyncio.fixture
async def live_hub(tmp_path):
    hub = await start_live_hub(tmp_path)
    try:
        yield hub
    finally:
        await stop_live_hub(hub)
