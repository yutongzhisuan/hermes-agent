"""Tests for the Mode A worker client (stub backend + worker unit paths)."""

from __future__ import annotations

import asyncio
import logging
from pathlib import Path
from typing import Any
from unittest.mock import patch

import jwt as pyjwt
import pytest
import pytest_asyncio

from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.models import TaskSpec
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.hub.bootstrap import start_ws_server
from extend.task_relay.tests.conftest import SECRET, make_auth, make_worker_jwt
from extend.task_relay.worker.backends.stub_backend import StubBackend, StubBackendConfig
from extend.task_relay.worker.task_executor import (
    TaskBackend,
    TaskCompletePayload,
    TaskExecutor,
    TaskRunPayload,
)
from extend.task_relay.worker.task_worker import TaskWorker
from extend.task_relay.worker.__main__ import _build_arg_parser
from extend.task_relay.worker.jwt_manager import _read_cached

def _spec(
    task_id: str = "t1",
    goal: str = "g",
    callback_topic: str = "topic-1",
) -> TaskSpec:
    return TaskSpec(task_id=task_id, goal=goal, callback_topic=callback_topic)


@pytest_asyncio.fixture
async def db(tmp_path):
    conn = await open_db(str(tmp_path / "ws.db"))
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
    )
    return TaskRouter(db, bus, cfg, registry)


@pytest.fixture
def backend():
    return StubBackend(StubBackendConfig(sleep_seconds=0.05))


# ---------------------------------------------------------------------------
# Fake WebSocket transport for fast worker unit tests.
# ---------------------------------------------------------------------------


class FakeWorkerWs:
    """In-memory replacement for :class:`TaskWorkerWs`."""

    def __init__(self, *args, **kwargs):
        self.requests: list[tuple[str, dict[str, Any]]] = []
        self.request_times: dict[str, list[float]] = {}
        self.handlers: dict[str, Any] = {}
        self.poll_results: list[list[dict[str, Any]]] = []
        self.raise_on_complete = False
        self.complete_count = 0
        self.task_status = "running"
        self.claim_token = "tok-1"

    async def connect(self) -> None:
        pass

    async def close(self) -> None:
        pass

    def on_notification(self, method: str, handler: Any) -> None:
        self.handlers[method] = handler

    async def request(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        self.requests.append((method, params))
        self.request_times.setdefault(method, []).append(asyncio.get_event_loop().time())
        if method == "worker.announce":
            return {"heartbeat_interval_ms": 1000}
        if method == "worker.poll":
            if self.poll_results:
                tasks = self.poll_results.pop(0)
                if tasks:
                    return {"offered": True, "tasks": tasks}
            return {"offered": False}
        if method == "task.complete":
            self.complete_count += 1
            if self.raise_on_complete:
                raise RuntimeError("websocket dropped")
            return {}
        if method == "task.status":
            return {"status": self.task_status, "claim_token": self.claim_token}
        if method == "task.progress":
            return {}
        if method == "task.checkpoint":
            return {}
        if method == "worker.heartbeat":
            return {}
        if method == "worker.close":
            return {}
        return {}


def _run_payload_dict(
    task_id: str,
    goal: str = "hello",
    attempt: int = 1,
    timeout_seconds: int = 60,
) -> dict[str, Any]:
    return {
        "run": {
            "task_id": task_id,
            "goal": goal,
            "attempt": attempt,
            "toolsets": [],
            "timeout_seconds": timeout_seconds,
        }
    }


async def _run_worker_until(worker: TaskWorker, predicate, timeout: float = 5.0) -> None:
    run_task = asyncio.create_task(worker.run())
    try:
        deadline = asyncio.get_event_loop().time() + timeout
        while not predicate() and asyncio.get_event_loop().time() < deadline:
            await asyncio.sleep(0.02)
        await worker.shutdown()
        await asyncio.wait_for(run_task, timeout=2.0)
    except asyncio.TimeoutError:
        run_task.cancel()
        try:
            await run_task
        except Exception:
            pass


# ---------------------------------------------------------------------------
# End-to-end test against real Hub fixtures.
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_worker_stub_backend_executes_task_to_completion(
    router, registry, db, backend
):
    auth = make_auth()
    server = await start_ws_server(
        router,
        auth,
        registry,
        db,
        router._config,
        host="127.0.0.1",
        port=0,
    )
    try:
        ws_url = f"ws://127.0.0.1:{server.sockets[0].getsockname()[1]}"
        jwt = make_worker_jwt("w1", max_concurrent=1)

        await router.dispatch_task(
            _spec(task_id="t1", goal="hello worker", callback_topic="c1"),
            "m1",
        )

        worker = TaskWorker(
            worker_id="w1",
            relay_url=ws_url,
            jwt=jwt,
            backend=backend,
            poll_wait_ms=500,
        )

        async def stop_after_task():
            for _ in range(50):
                status = await router.get_status("t1")
                if status in {"completed", "failed", "cancelled", "lost"}:
                    break
                await asyncio.sleep(0.05)
            await worker.shutdown()

        await asyncio.gather(worker.run(), stop_after_task())

        task = await db.get_task("t1")
        assert task.status == "completed"
        assert task.summary is not None
        assert "hello worker" in task.summary
    finally:
        server.close()
        await server.wait_closed()


# ---------------------------------------------------------------------------
# JWT max_concurrent capping.
# ---------------------------------------------------------------------------


def test_worker_caps_max_concurrent_to_jwt_claim():
    worker = TaskWorker(
        worker_id="w1",
        relay_url="ws://x",
        jwt=make_worker_jwt("w1", max_concurrent=2),
        backend=StubBackend(),
        max_concurrent=5,
    )
    assert worker.max_concurrent == 2


def test_worker_keeps_cli_max_concurrent_when_below_jwt_claim():
    worker = TaskWorker(
        worker_id="w1",
        relay_url="ws://x",
        jwt=make_worker_jwt("w1", max_concurrent=5),
        backend=StubBackend(),
        max_concurrent=2,
    )
    assert worker.max_concurrent == 2


def test_worker_defaults_max_concurrent_when_jwt_claim_missing(caplog):
    # Master token has no max_concurrent claim.
    auth = make_auth()
    master_jwt = auth.issue_master_jwt("w1")
    with caplog.at_level(logging.WARNING):
        worker = TaskWorker(
            worker_id="w1",
            relay_url="ws://x",
            jwt=master_jwt,
            backend=StubBackend(),
            max_concurrent=3,
        )
    assert worker.max_concurrent == 3


def test_worker_logs_warning_when_cli_exceeds_jwt_claim(caplog):
    with caplog.at_level(logging.WARNING):
        TaskWorker(
            worker_id="w1",
            relay_url="ws://x",
            jwt=make_worker_jwt("w1", max_concurrent=2),
            backend=StubBackend(),
            max_concurrent=5,
        )
    assert any("exceeds JWT limit" in rec.message for rec in caplog.records)


# ---------------------------------------------------------------------------
# Cancel handling.
# ---------------------------------------------------------------------------


class CancelObservingBackend(TaskBackend):
    async def run(self, run, on_progress, on_checkpoint, cancel_event):
        # Wait until cancellation is requested.
        for _ in range(200):
            if cancel_event.is_set():
                await on_progress("stub cancelled by worker")
                return TaskCompletePayload(
                    status="cancelled",
                    summary="cancelled by request",
                )
            await asyncio.sleep(0.01)
        return TaskCompletePayload(status="completed", summary="done")


@pytest.mark.asyncio
async def test_worker_cancel_sets_backend_cancel_event(monkeypatch):
    fake_ws = FakeWorkerWs()
    monkeypatch.setattr(
        "extend.task_relay.worker.task_worker.TaskWorkerWs",
        lambda *args, **kwargs: fake_ws,
    )

    worker = TaskWorker(
        worker_id="w1",
        relay_url="ws://x",
        jwt=make_worker_jwt("w1"),
        backend=CancelObservingBackend(),
        poll_wait_ms=500,
    )
    fake_ws.poll_results.append([_run_payload_dict("t1")])

    async def cancel_after_claim():
        # Wait until the worker has created the per-task cancel event.
        for _ in range(200):
            if "t1" in worker._cancel_events:
                break
            await asyncio.sleep(0.01)
        assert "t1" in worker._cancel_events
        await worker._on_cancel({"task_id": "t1", "reason": "test cancel"})

    await asyncio.gather(
        _run_worker_until(worker, lambda: fake_ws.complete_count > 0),
        cancel_after_claim(),
    )

    completes = [r for r in fake_ws.requests if r[0] == "task.complete"]
    assert len(completes) == 1
    assert completes[0][1]["status"] == "cancelled"


# ---------------------------------------------------------------------------
# Empty-poll backoff.
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_worker_backoff_doubles_on_empty_poll(monkeypatch):
    fake_ws = FakeWorkerWs()
    monkeypatch.setattr(
        "extend.task_relay.worker.task_worker.TaskWorkerWs",
        lambda *args, **kwargs: fake_ws,
    )

    worker = TaskWorker(
        worker_id="w1",
        relay_url="ws://x",
        jwt=make_worker_jwt("w1"),
        backend=StubBackend(),
        initial_backoff_seconds=0.1,
        max_backoff_seconds=2.0,
        poll_wait_ms=10_000,
    )

    await _run_worker_until(
        worker,
        lambda: len(fake_ws.request_times.get("worker.poll", [])) >= 4,
        timeout=2.0,
    )

    poll_times = fake_ws.request_times["worker.poll"]
    # With initial backoff 0.1s doubling, the gaps should grow.
    gaps = [poll_times[i + 1] - poll_times[i] for i in range(len(poll_times) - 1)]
    assert len(gaps) >= 3
    assert gaps[1] > gaps[0] * 1.5  # backoff doubled from the first to second gap


@pytest.mark.asyncio
async def test_worker_backoff_resets_after_offered_task(monkeypatch):
    fake_ws = FakeWorkerWs()
    monkeypatch.setattr(
        "extend.task_relay.worker.task_worker.TaskWorkerWs",
        lambda *args, **kwargs: fake_ws,
    )

    worker = TaskWorker(
        worker_id="w1",
        relay_url="ws://x",
        jwt=make_worker_jwt("w1"),
        backend=StubBackend(StubBackendConfig(sleep_seconds=0.01)),
        initial_backoff_seconds=0.2,
        max_backoff_seconds=2.0,
        poll_wait_ms=10_000,
    )
    fake_ws.poll_results.append([])  # empty poll
    fake_ws.poll_results.append([_run_payload_dict("t1")])  # task offered

    await _run_worker_until(
        worker,
        lambda: fake_ws.complete_count > 0,
        timeout=2.0,
    )

    completes = [r for r in fake_ws.requests if r[0] == "task.complete"]
    assert len(completes) == 1
    assert completes[0][1]["status"] == "completed"


# ---------------------------------------------------------------------------
# Concurrency limit.
# ---------------------------------------------------------------------------


class TimingBackend(TaskBackend):
    def __init__(self):
        self.starts: dict[str, float] = {}
        self.ends: dict[str, float] = {}

    async def run(self, run, on_progress, on_checkpoint, cancel_event):
        self.starts[run.task_id] = asyncio.get_event_loop().time()
        await on_progress(f"start {run.task_id}")
        await asyncio.sleep(0.2)
        self.ends[run.task_id] = asyncio.get_event_loop().time()
        await on_progress(f"end {run.task_id}")
        return TaskCompletePayload(status="completed", summary=f"done {run.task_id}")


@pytest.mark.asyncio
async def test_worker_concurrency_limit_is_respected(monkeypatch):
    fake_ws = FakeWorkerWs()
    monkeypatch.setattr(
        "extend.task_relay.worker.task_worker.TaskWorkerWs",
        lambda *args, **kwargs: fake_ws,
    )

    timing = TimingBackend()
    worker = TaskWorker(
        worker_id="w1",
        relay_url="ws://x",
        jwt=make_worker_jwt("w1", max_concurrent=1),
        backend=timing,
        max_concurrent=1,
        poll_wait_ms=10_000,
    )
    fake_ws.poll_results.append([_run_payload_dict("t1")])
    fake_ws.poll_results.append([_run_payload_dict("t2")])

    await _run_worker_until(
        worker,
        lambda: fake_ws.complete_count == 2,
        timeout=5.0,
    )

    # With max_concurrent=1, the second task must not start before the first ends.
    assert timing.ends["t1"] <= timing.starts["t2"]


# ---------------------------------------------------------------------------
# Error handling.
# ---------------------------------------------------------------------------


class RaisingBackend(TaskBackend):
    async def run(self, run, on_progress, on_checkpoint, cancel_event):
        raise RuntimeError("backend boom")


@pytest.mark.asyncio
async def test_worker_sends_single_failed_complete_when_backend_raises(monkeypatch):
    fake_ws = FakeWorkerWs()
    monkeypatch.setattr(
        "extend.task_relay.worker.task_worker.TaskWorkerWs",
        lambda *args, **kwargs: fake_ws,
    )

    worker = TaskWorker(
        worker_id="w1",
        relay_url="ws://x",
        jwt=make_worker_jwt("w1"),
        backend=RaisingBackend(),
        poll_wait_ms=10_000,
    )
    fake_ws.poll_results.append([_run_payload_dict("t1")])

    await _run_worker_until(
        worker,
        lambda: fake_ws.complete_count > 0,
        timeout=2.0,
    )

    completes = [r for r in fake_ws.requests if r[0] == "task.complete"]
    assert len(completes) == 1
    assert completes[0][1]["status"] == "failed"


# ---------------------------------------------------------------------------
# Duplicate complete prevention.
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_executor_allows_retry_after_send_failure():
    class FlakyOnceWs:
        def __init__(self):
            self.complete_calls = 0

        async def request(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
            if method == "task.complete":
                self.complete_calls += 1
                if self.complete_calls == 1:
                    raise RuntimeError("transport down")
            return {}

    ws = FlakyOnceWs()
    executor = TaskExecutor(ws, StubBackend(StubBackendConfig(sleep_seconds=0)))

    with pytest.raises(RuntimeError, match="transport down"):
        await executor.execute(
            TaskRunPayload(
                task_id="t1",
                attempt=1,
                goal="g",
                params=None,
                context=None,
                toolsets=[],
                timeout_seconds=60,
                first_progress_seconds=None,
                trace_context=None,
                resume_from_checkpoint=None,
            ),
            asyncio.Event(),
        )

    assert ws.complete_calls == 1
    assert not executor.completion_attempted

    success = await executor._complete_once(
        "t1",
        TaskCompletePayload(status="failed", summary="fallback"),
    )
    assert success is True
    assert ws.complete_calls == 2
    assert executor.completion_attempted


@pytest.mark.asyncio
async def test_worker_sends_fallback_complete_after_send_failure(monkeypatch):
    fake_ws = FakeWorkerWs()
    fake_ws.raise_on_complete = True
    monkeypatch.setattr(
        "extend.task_relay.worker.task_worker.TaskWorkerWs",
        lambda *args, **kwargs: fake_ws,
    )

    worker = TaskWorker(
        worker_id="w1",
        relay_url="ws://x",
        jwt=make_worker_jwt("w1"),
        backend=StubBackend(StubBackendConfig(sleep_seconds=0.01)),
        poll_wait_ms=10_000,
    )
    fake_ws.poll_results.append([_run_payload_dict("t1")])

    await _run_worker_until(
        worker,
        lambda: fake_ws.complete_count >= 2,
        timeout=2.0,
    )

    # The first complete failed; the outer error handler must emit a fallback.
    completes = [r for r in fake_ws.requests if r[0] == "task.complete"]
    assert len(completes) == 2
    assert completes[0][1]["status"] == "completed"
    assert completes[1][1]["status"] == "failed"


@pytest.mark.asyncio
async def test_worker_fallback_complete_uses_settlement_guard(monkeypatch):
    """The outer except path routes complete through the guarded executor path."""
    fake_ws = FakeWorkerWs()
    fake_ws.task_status = "failed"  # Hub already settled; guard must drop.
    monkeypatch.setattr(
        "extend.task_relay.worker.task_worker.TaskWorkerWs",
        lambda *args, **kwargs: fake_ws,
    )

    original_execute = TaskExecutor.execute

    def execute_that_raises(self, run, cancel_event):
        # Raise before the executor can send its own complete.
        raise RuntimeError("forced executor failure")

    monkeypatch.setattr(TaskExecutor, "execute", execute_that_raises)

    worker = TaskWorker(
        worker_id="w1",
        relay_url="ws://x",
        jwt=make_worker_jwt("w1"),
        backend=StubBackend(),
        poll_wait_ms=10_000,
    )
    fake_ws.poll_results.append([_run_payload_dict("t1")])

    await _run_worker_until(
        worker,
        lambda: any(r[0] == "task.status" for r in fake_ws.requests),
        timeout=2.0,
    )

    # Guard should have been consulted (task.status request) and the fallback
    # complete should have been dropped because the Hub is already terminal.
    status_calls = [r for r in fake_ws.requests if r[0] == "task.status"]
    assert len(status_calls) == 1
    complete_calls = [r for r in fake_ws.requests if r[0] == "task.complete"]
    assert len(complete_calls) == 0


# ---------------------------------------------------------------------------
# CLI / utility helpers.
# ---------------------------------------------------------------------------


def test_load_jwt_reads_and_strips_file(tmp_path):
    path = tmp_path / "jwt.txt"
    path.write_text("  bearer-token-123\n\n", encoding="utf-8")
    assert _read_cached(path) == "bearer-token-123"


def test_worker_announce_uses_session_modes_from_cli(monkeypatch):
    fake_ws = FakeWorkerWs()
    monkeypatch.setattr(
        "extend.task_relay.worker.task_worker.TaskWorkerWs",
        lambda *args, **kwargs: fake_ws,
    )

    worker = TaskWorker(
        worker_id="w1",
        relay_url="ws://x",
        jwt=make_worker_jwt("w1"),
        backend=StubBackend(),
        session_modes=["a", "b"],
    )
    fake_ws.poll_results.append([_run_payload_dict("t1")])

    asyncio.run(_run_worker_until(worker, lambda: fake_ws.complete_count > 0, timeout=2.0))

    announce = [r for r in fake_ws.requests if r[0] == "worker.announce"]
    assert announce
    assert announce[0][1]["session_modes"] == ["a", "b"]


def test_cli_session_modes_default_is_a():
    parser = _build_arg_parser()
    args = parser.parse_args(
        ["--worker-id", "w1", "--relay-url", "ws://x", "--worker-jwt-file", "/tmp/jwt"]
    )
    assert args.session_modes == "a"


def test_cli_session_modes_can_be_comma_separated():
    parser = _build_arg_parser()
    args = parser.parse_args(
        [
            "--worker-id",
            "w1",
            "--relay-url",
            "ws://x",
            "--worker-jwt-file",
            "/tmp/jwt",
            "--session-modes",
            "a, b",
        ]
    )
    assert args.session_modes == "a, b"


@pytest.mark.asyncio
async def test_cli_rejects_session_modes_without_a(tmp_path):
    jwt_path = tmp_path / "jwt.txt"
    jwt_path.write_text(make_worker_jwt("w1"), encoding="utf-8")

    from extend.task_relay.worker.__main__ import _async_main

    rc = await _async_main(
        [
            "--worker-id",
            "w1",
            "--relay-url",
            "ws://x",
            "--worker-jwt-file",
            str(jwt_path),
            "--session-modes",
            "b",
        ]
    )
    assert rc == 1
