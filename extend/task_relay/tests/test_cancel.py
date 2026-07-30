"""Cancel and timeout attribution tests for the ACP task backend.

These tests mock the ACP ``SessionManager`` and ``AIAgent`` so they run
without a real model or ACP transport.
"""

from __future__ import annotations

import asyncio
import threading
from typing import Any
from unittest.mock import AsyncMock

import pytest

from extend.task_relay.worker.backends.acp_backend import AcpTaskBackend
from extend.task_relay.worker.task_executor import TaskCancelEvent, TaskRunPayload


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
