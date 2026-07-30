"""Cancel and timeout attribution tests for the ACP task backend.

These tests mock the ACP ``SessionManager`` and ``AIAgent`` so they run
without a real model or ACP transport.
"""

from __future__ import annotations

import asyncio
import threading
import time
from typing import Any
from unittest.mock import AsyncMock

import jwt as pyjwt
import pytest

from extend.task_relay.worker.backends.acp_backend import AcpTaskBackend
from extend.task_relay.worker.task_executor import (
    TaskBackend,
    TaskCancelEvent,
    TaskCompletePayload,
    TaskExecutor,
    TaskRunPayload,
)
from extend.task_relay.worker.task_worker import TaskWorker


_TEST_JWT_SECRET = "s" * 32


def _worker_jwt(worker_id: str, max_concurrent: int = 1) -> str:
    return pyjwt.encode(
        {
            "sub": worker_id,
            "aud": "task-relay-hub",
            "iss": "hermes-relay-hub",
            "allowed_toolsets": [],
            "max_concurrent": max_concurrent,
            "exp": int(time.time()) + 3600,
        },
        _TEST_JWT_SECRET,
        algorithm="HS256",
    )


class FakeAgent:
    """Fake Hermes AIAgent for unit tests."""

    def __init__(self, block_until_interrupt: bool = True):
        self._interrupt_event = threading.Event()
        self._block_until_interrupt = block_until_interrupt
        self.interrupted = False
        self.run_count = 0
        self.step_callback: Any = None

    def interrupt(self) -> None:
        self.interrupted = True
        self._interrupt_event.set()

    def run_conversation(self, **kwargs: Any) -> dict[str, Any]:
        self.run_count += 1

        # Fire a synthetic step callback so progress throttling is exercised.
        if self.step_callback is not None:
            try:
                self.step_callback(1, [{"name": "fake_tool"}])
            except Exception:
                pass

        if self._block_until_interrupt:
            if not self._interrupt_event.wait(timeout=2.0):
                raise TimeoutError("fake agent was not interrupted")
            return {
                "final_response": "partial tool result after interrupt",
                "interrupted": True,
                "messages": [],
                "api_calls": 1,
                "session_id": "fake-hermes-session",
            }

        return {
            "final_response": "all done",
            "messages": [],
            "api_calls": 1,
            "session_id": "fake-hermes-session",
            "prompt_tokens": 10,
            "completion_tokens": 20,
            "total_tokens": 30,
        }


class FakeSessionState:
    """Fake ``SessionState`` returned by ``FakeSessionManager``."""

    def __init__(self, block_until_interrupt: bool = True):
        self.session_id = "fake-session-1"
        self.agent = FakeAgent(block_until_interrupt=block_until_interrupt)
        self.history: list[dict[str, Any]] = []
        self.cancel_event = threading.Event()


class FakeSessionManager:
    """Fake ``acp_adapter.session.SessionManager`` for tests."""

    def __init__(self, block_until_interrupt: bool = True):
        self.block_until_interrupt = block_until_interrupt
        self.sessions: list[FakeSessionState] = []
        self.saved: list[str] = []

    def create_session(self, cwd: str = ".") -> FakeSessionState:
        state = FakeSessionState(block_until_interrupt=self.block_until_interrupt)
        self.sessions.append(state)
        return state

    def save_session(self, session_id: str) -> None:
        self.saved.append(session_id)


def _run_payload(goal: str = "test goal") -> TaskRunPayload:
    return TaskRunPayload(
        task_id="t1",
        attempt=1,
        goal=goal,
        params=None,
        context=None,
        toolsets=[],
        timeout_seconds=60,
        first_progress_seconds=None,
        trace_context=None,
        resume_from_checkpoint=None,
    )


@pytest.mark.asyncio
async def test_cancel_during_tool_settles_cancelled():
    """A normal cancel during tool execution settles as cancelled and
    salvages the partial final_response."""
    manager = FakeSessionManager(block_until_interrupt=True)
    backend = AcpTaskBackend(
        session_manager=manager, progress_interval_seconds=0.0
    )
    run = _run_payload()
    cancel_event = asyncio.Event()
    progress_calls: list[str] = []

    async def _on_progress(summary: str) -> None:
        progress_calls.append(summary)

    on_checkpoint = AsyncMock()

    async def _delayed_cancel() -> None:
        await asyncio.sleep(0.05)
        cancel_event.set()

    cancel_task = asyncio.create_task(_delayed_cancel())
    result = await backend.run(run, _on_progress, on_checkpoint, cancel_event)
    await cancel_task
    # Let the event loop drain progress frames scheduled from the executor.
    await asyncio.sleep(0)

    assert result.status == "cancelled"
    assert result.summary is not None
    assert "partial tool result" in result.summary
    assert manager.sessions[0].agent.interrupted is True
    assert manager.sessions[0].cancel_event.is_set()
    assert progress_calls


@pytest.mark.asyncio
async def test_execution_timeout_attribution_is_failed():
    """A cancel pushed with reason ``timeout`` settles as failed, not cancelled."""
    manager = FakeSessionManager(block_until_interrupt=True)
    backend = AcpTaskBackend(
        session_manager=manager, progress_interval_seconds=0.0
    )
    run = _run_payload()
    cancel_event = TaskCancelEvent()
    on_progress = AsyncMock()
    on_checkpoint = AsyncMock()

    async def _delayed_timeout() -> None:
        await asyncio.sleep(0.05)
        cancel_event.set(reason="timeout")

    timeout_task = asyncio.create_task(_delayed_timeout())
    result = await backend.run(run, on_progress, on_checkpoint, cancel_event)
    await timeout_task

    assert result.status == "failed"
    assert result.summary is not None
    assert "timeout" in result.summary.lower()
    assert manager.sessions[0].agent.interrupted is True
    assert manager.sessions[0].cancel_event.is_set()


@pytest.mark.asyncio
async def test_acp_backend_completion_green_path():
    """A normal run without cancellation returns completed with usage/fields."""
    manager = FakeSessionManager(block_until_interrupt=False)
    backend = AcpTaskBackend(
        session_manager=manager, progress_interval_seconds=0.0
    )
    run = _run_payload(goal="say hello")
    cancel_event = asyncio.Event()
    on_progress = AsyncMock()
    on_checkpoint = AsyncMock()

    result = await backend.run(run, on_progress, on_checkpoint, cancel_event)

    assert result.status == "completed"
    assert result.summary == "all done"
    assert result.result_text == "all done"
    assert result.usage == {
        "prompt_tokens": 10,
        "completion_tokens": 20,
        "total_tokens": 30,
    }
    assert result.fields is not None
    assert result.fields.get("acp_session_id") == "fake-hermes-session"



class _ReasonCapturingBackend(TaskBackend):
    """Backend that records the cancel reason it observed."""

    def __init__(self):
        self.reason: str | None = None
        self.complete_status: str | None = None

    async def run(self, run, on_progress, on_checkpoint, cancel_event):
        await cancel_event.wait()
        self.reason = getattr(cancel_event, "reason", None)
        await on_progress(f"cancelled: {self.reason}")
        self.complete_status = "cancelled"
        return TaskCompletePayload(
            status="cancelled",
            summary=f"reason={self.reason}",
        )


class _FailingOnTimeoutBackend(TaskBackend):
    """Backend that returns failed when cancelled with a timeout reason."""

    async def run(self, run, on_progress, on_checkpoint, cancel_event):
        await cancel_event.wait()
        reason = getattr(cancel_event, "reason", None)
        await on_progress(f"timeout reason: {reason}")
        return TaskCompletePayload(
            status="failed",
            summary="execution timeout",
            error="execution timeout",
        )


class _MinimalFakeWorkerWs:
    """In-memory WebSocket stand-in for cancel-path unit tests."""

    def __init__(self, task_status: str = "cancelling", claim_token: str = "tok-1"):
        self.requests: list[tuple[str, dict[str, Any]]] = []
        self.handlers: dict[str, Any] = {}
        self.task_status = task_status
        self.claim_token = claim_token

    async def connect(self) -> None:
        pass

    async def close(self) -> None:
        pass

    def on_notification(self, method: str, handler: Any) -> None:
        self.handlers[method] = handler

    async def request(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        self.requests.append((method, params))
        if method == "worker.announce":
            return {"heartbeat_interval_ms": 1000}
        if method == "worker.poll":
            return {
                "offered": True,
                "tasks": [
                    {
                        "run": {
                            "task_id": "t1",
                            "goal": "hello",
                            "attempt": 1,
                            "toolsets": [],
                            "timeout_seconds": 60,
                            "claim_token": self.claim_token,
                        }
                    }
                ],
            }
        if method == "task.status":
            return {"status": self.task_status, "claim_token": self.claim_token}
        return {}


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


@pytest.mark.asyncio
async def test_worker_cancel_reason_reaches_backend(monkeypatch):
    """A Hub ``task.cancel`` with reason='timeout' is plumbed to the backend."""
    fake_ws = _MinimalFakeWorkerWs(task_status="cancelling", claim_token="tok-1")
    monkeypatch.setattr(
        "extend.task_relay.worker.task_worker.TaskWorkerWs",
        lambda *args, **kwargs: fake_ws,
    )

    backend = _ReasonCapturingBackend()
    worker = TaskWorker(
        worker_id="w1",
        relay_url="ws://x",
        jwt=_worker_jwt("w1"),
        backend=backend,
        poll_wait_ms=10_000,
    )

    async def cancel_after_claim():
        for _ in range(200):
            if "t1" in worker._cancel_events:
                break
            await asyncio.sleep(0.01)
        assert "t1" in worker._cancel_events
        await worker._on_cancel({"task_id": "t1", "reason": "timeout"})

    await asyncio.gather(
        _run_worker_until(
            worker,
            lambda: any(r[0] == "task.complete" for r in fake_ws.requests),
            timeout=2.0,
        ),
        cancel_after_claim(),
    )

    assert backend.reason == "timeout"
    completes = [r for r in fake_ws.requests if r[0] == "task.complete"]
    assert len(completes) == 1
    assert completes[0][1]["status"] == "cancelled"


@pytest.mark.asyncio
async def test_executor_drops_complete_when_hub_already_terminal():
    """A timeout-cancelled backend result is not sent if the Hub already settled."""

    class FakeWs:
        def __init__(self):
            self.requests: list[tuple[str, dict[str, Any]]] = []

        async def request(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
            self.requests.append((method, params))
            if method == "task.status":
                return {"status": "failed", "claim_token": "tok-1"}
            return {}

    ws = FakeWs()
    backend = _FailingOnTimeoutBackend()

    async def guard(task_id: str) -> bool:
        status_result = await ws.request("task.status", {"task_id": task_id})
        return status_result.get("status") not in {"completed", "failed", "lost", "cancelled"}

    executor = TaskExecutor(ws, backend, settlement_guard=guard)

    cancel_event = TaskCancelEvent()

    async def trigger_timeout():
        await asyncio.sleep(0.05)
        cancel_event.set(reason="timeout")

    await asyncio.gather(
        executor.execute(
            TaskRunPayload(
                task_id="t1",
                attempt=1,
                goal="hello",
                params=None,
                context=None,
                toolsets=[],
                timeout_seconds=60,
                first_progress_seconds=None,
                trace_context=None,
                resume_from_checkpoint=None,
                claim_token="tok-1",
            ),
            cancel_event,
        ),
        trigger_timeout(),
    )

    complete_calls = [r for r in ws.requests if r[0] == "task.complete"]
    assert len(complete_calls) == 0
    assert executor.completion_attempted is True


@pytest.mark.asyncio
async def test_progress_throttling_drops_rapid_callbacks():
    """Only one progress frame is forwarded per throttling interval."""
    manager = FakeSessionManager(block_until_interrupt=True)
    backend = AcpTaskBackend(
        session_manager=manager, progress_interval_seconds=0.15
    )
    run = _run_payload()
    cancel_event = asyncio.Event()
    progress_calls: list[str] = []

    async def _on_progress(summary: str) -> None:
        progress_calls.append(summary)

    on_checkpoint = AsyncMock()

    async def _delayed_cancel() -> None:
        await asyncio.sleep(0.3)
        cancel_event.set()

    cancel_task = asyncio.create_task(_delayed_cancel())
    await backend.run(run, _on_progress, on_checkpoint, cancel_event)
    await cancel_task
    await asyncio.sleep(0.05)

    # The fake agent fires one callback per run_conversation call. Because the
    # agent blocks until interrupt, run_conversation is called once, so exactly
    # one progress frame should be delivered regardless of the interval.
    assert progress_calls

    # Exercise the throttling path more directly: a second rapid callback should
    # be dropped when inside the interval window.
    progress_calls.clear()
    manager2 = FakeSessionManager(block_until_interrupt=True)
    backend2 = AcpTaskBackend(
        session_manager=manager2, progress_interval_seconds=0.2
    )
    run2 = _run_payload()
    cancel_event2 = asyncio.Event()

    delivered = 0

    async def _counting_progress(summary: str) -> None:
        nonlocal delivered
        delivered += 1

    async def _fire_two_quick_then_cancel() -> None:
        # Wait for the agent to be running and its callback installed.
        await asyncio.sleep(0.05)
        agent = manager2.sessions[0].agent
        agent.step_callback(1, [{"name": "first"}])
        agent.step_callback(2, [{"name": "second"}])
        await asyncio.sleep(0.25)
        cancel_event2.set()

    await asyncio.gather(
        backend2.run(run2, _counting_progress, AsyncMock(), cancel_event2),
        _fire_two_quick_then_cancel(),
    )

    # Both callbacks were attempted, but the second should have been throttled.
    assert delivered == 1
