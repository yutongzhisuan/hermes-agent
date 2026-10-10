"""Cancel and timeout attribution tests for the ACP task backend.

Migrated from swarm-network ``tests/test_cancel.py``. These tests mock the
ACP ``SessionManager`` and ``AIAgent`` so they run without a real model or
ACP transport.
"""

from __future__ import annotations

import asyncio
import threading
from typing import Any
from unittest.mock import AsyncMock

import pytest

from extend.sub_agent.acp_backend import AcpTaskBackend
from extend.sub_agent.constants import CANCEL_REASON_TIMEOUT
from extend.sub_agent.task_types import TaskCancelEvent, TaskRunPayload


class FakeAgent:
    """Fake XHermes AIAgent for unit tests."""

    def __init__(self, block_until_interrupt: bool = True):
        self._interrupt_event = threading.Event()
        self._block_until_interrupt = block_until_interrupt
        self.interrupted = False
        self.run_count = 0
        self.step_callback: Any = None
        self.result_messages: list[dict[str, Any]] = []

    def interrupt(self) -> None:
        self.interrupted = True
        self._interrupt_event.set()

    def run_conversation(self, **kwargs: Any) -> dict[str, Any]:
        self.run_count += 1
        self.last_kwargs = kwargs

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
                "session_id": "fake-xhermes-session",
            }

        return {
            "final_response": "all done",
            "messages": self.result_messages,
            "api_calls": 1,
            "session_id": "fake-xhermes-session",
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
    backend = AcpTaskBackend(session_manager=manager, progress_interval_seconds=0.0)
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
async def test_execution_timeout_marker_settles_failed():
    """A cancel pushed with the dedicated timeout marker settles as failed."""
    manager = FakeSessionManager(block_until_interrupt=True)
    backend = AcpTaskBackend(session_manager=manager, progress_interval_seconds=0.0)
    run = _run_payload()
    cancel_event = TaskCancelEvent()
    on_progress = AsyncMock()
    on_checkpoint = AsyncMock()

    async def _delayed_timeout() -> None:
        await asyncio.sleep(0.05)
        cancel_event.set(reason=CANCEL_REASON_TIMEOUT)

    timeout_task = asyncio.create_task(_delayed_timeout())
    result = await backend.run(run, on_progress, on_checkpoint, cancel_event)
    await timeout_task

    assert result.status == "failed"
    assert result.summary is not None
    assert "timeout" in result.summary.lower()
    assert manager.sessions[0].agent.interrupted is True
    assert manager.sessions[0].cancel_event.is_set()


@pytest.mark.asyncio
async def test_cancel_reason_containing_timeout_is_not_failed():
    """A master cancel whose reason contains 'timeout' must still settle cancelled."""
    manager = FakeSessionManager(block_until_interrupt=True)
    backend = AcpTaskBackend(session_manager=manager, progress_interval_seconds=0.0)
    run = _run_payload()
    cancel_event = TaskCancelEvent()
    on_progress = AsyncMock()
    on_checkpoint = AsyncMock()

    async def _delayed_cancel() -> None:
        await asyncio.sleep(0.05)
        cancel_event.set(reason="user requested timeout")

    cancel_task = asyncio.create_task(_delayed_cancel())
    result = await backend.run(run, on_progress, on_checkpoint, cancel_event)
    await cancel_task

    assert result.status == "cancelled"
    assert manager.sessions[0].agent.interrupted is True


# ---------------------------------------------------------------------------
# Responses API envelope integration (POST /v1/responses)
# ---------------------------------------------------------------------------


class _RecordingSessionManager:
    """Stateless-capable fake that records the toolsets handed to it."""

    def __init__(self) -> None:
        self.recorded_toolsets: list[list[str]] = []
        self.state = FakeSessionState(block_until_interrupt=False)
        self.state.cancel_event = threading.Event()

    def create_session(self, cwd: str = ".", **kwargs: Any) -> FakeSessionState:
        self.recorded_toolsets.append(list(kwargs.get("toolsets") or []))
        return self.state

    def remove_session(self, session_id: str) -> None:  # pragma: no cover
        pass


def _responses_params(
    *,
    response_id="resp_test1",
    model="qwen38-27b-fp4",
    input="hello",
    instructions="be brief",
    tools=None,
):
    import json

    from extend.sub_agent.responses_payload import PROTOCOL

    envelope = {
        "protocol": PROTOCOL,
        "response_id": response_id,
        "request": {
            "model": model,
            "input": input,
            "instructions": instructions,
            "tools": tools or [],
            "max_output_tokens": 0,
            "text": None,
            "metadata": {},
        },
        "limits": {"max_result_bytes": 262144},
    }
    return {"responses.v1": json.dumps(envelope)}


@pytest.mark.asyncio
async def test_responses_envelope_completed_wraps_result_text():
    import json as _json

    manager = _RecordingSessionManager()
    backend = AcpTaskBackend(
        session_manager=manager, progress_interval_seconds=0.0, stateless=True
    )
    run = TaskRunPayload(
        task_id="resp_test1",
        attempt=1,
        goal="ignored",
        params=_responses_params(),
        context=None,
        toolsets=["web"],
        timeout_seconds=60,
        first_progress_seconds=None,
        trace_context=None,
        resume_from_checkpoint=None,
    )
    cancel_event = asyncio.Event()
    on_progress = AsyncMock()
    on_checkpoint = AsyncMock()

    result = await backend.run(run, on_progress, on_checkpoint, cancel_event)

    assert result.status == "completed"
    obj = _json.loads(result.result_text)
    assert obj["object"] == "response"
    assert obj["id"] == "resp_test1"
    assert isinstance(obj["created_at"], int)
    assert obj["output"][0]["content"][0]["text"] == "all done"
    assert obj["usage"]["input_tokens"] == 10
    assert obj["usage"]["output_tokens"] == 20


@pytest.mark.asyncio
async def test_responses_structured_replay_passes_history():
    """v4: input items replay as structured conversation history."""
    manager = _RecordingSessionManager()
    backend = AcpTaskBackend(
        session_manager=manager, progress_interval_seconds=0.0, stateless=True
    )
    run = TaskRunPayload(
        task_id="resp_hist",
        attempt=1,
        goal="g",
        params=_responses_params(
            input=[
                {"role": "user", "content": "q1"},
                {"role": "assistant", "content": "a1"},
                {"role": "user", "content": "q2"},
            ]
        ),
        context=None,
        toolsets=[],
        timeout_seconds=60,
        first_progress_seconds=None,
        trace_context=None,
        resume_from_checkpoint=None,
    )
    await backend.run(run, AsyncMock(), AsyncMock(), asyncio.Event())
    kwargs = manager.state.agent.last_kwargs
    assert kwargs["user_message"] == "q2"
    # instructions become the leading system message (DeepSeek semantics)
    assert kwargs["conversation_history"] == [
        {"role": "system", "content": "be brief"},
        {"role": "user", "content": "q1"},
        {"role": "assistant", "content": "a1"},
    ]


@pytest.mark.asyncio
async def test_responses_no_trailing_user_falls_back_to_flatten():
    """A transcript not ending with a user message keeps the flatten path."""
    manager = _RecordingSessionManager()
    backend = AcpTaskBackend(
        session_manager=manager, progress_interval_seconds=0.0, stateless=True
    )
    run = TaskRunPayload(
        task_id="resp_flat",
        attempt=1,
        goal="g",
        params=_responses_params(
            instructions="",
            input=[
                {"role": "user", "content": "q"},
                {
                    "type": "function_call",
                    "call_id": "c1",
                    "name": "f",
                    "arguments": "{}",
                },
                {"type": "function_call_output", "call_id": "c1", "output": "r"},
            ],
        ),
        context=None,
        toolsets=[],
        timeout_seconds=60,
        first_progress_seconds=None,
        trace_context=None,
        resume_from_checkpoint=None,
    )
    await backend.run(run, AsyncMock(), AsyncMock(), asyncio.Event())
    kwargs = manager.state.agent.last_kwargs
    # flatten path: user_message is the transcript text, history is the
    # session's own (empty for stateless).
    assert kwargs["user_message"]
    assert kwargs["conversation_history"] == []


@pytest.mark.asyncio
async def test_responses_output_carries_replayable_items():
    """v4: terminal Response output carries this turn's replayable items
    (function_call / function_call_output / message), not just a single
    message."""
    import json as _json

    manager = _RecordingSessionManager()
    manager.state.agent.result_messages = [
        {"role": "user", "content": "hello"},
        {
            "role": "assistant",
            "content": None,
            "tool_calls": [
                {
                    "id": "call_1",
                    "type": "function",
                    "function": {"name": "web", "arguments": "{}"},
                }
            ],
        },
        {"role": "tool", "tool_call_id": "call_1", "content": "search result"},
        {"role": "assistant", "content": "all done"},
    ]
    backend = AcpTaskBackend(
        session_manager=manager, progress_interval_seconds=0.0, stateless=True
    )
    run = TaskRunPayload(
        task_id="resp_items",
        attempt=1,
        goal="g",
        params=_responses_params(),
        context=None,
        toolsets=["web"],
        timeout_seconds=60,
        first_progress_seconds=None,
        trace_context=None,
        resume_from_checkpoint=None,
    )
    result = await backend.run(run, AsyncMock(), AsyncMock(), asyncio.Event())
    obj = _json.loads(result.result_text)
    types = [i["type"] for i in obj["output"]]
    assert types == ["function_call", "function_call_output", "message"]
    assert obj["output"][0]["call_id"] == "call_1"
    assert obj["output"][1]["output"] == "search result"
    assert obj["output"][2]["content"][0]["text"] == "all done"


@pytest.mark.asyncio
async def test_responses_empty_toolsets_means_no_tools():
    manager = _RecordingSessionManager()
    backend = AcpTaskBackend(
        session_manager=manager, progress_interval_seconds=0.0, stateless=True
    )
    run = TaskRunPayload(
        task_id="resp_empty",
        attempt=1,
        goal="g",
        params=_responses_params(),
        context=None,
        toolsets=[],
        timeout_seconds=60,
        first_progress_seconds=None,
        trace_context=None,
        resume_from_checkpoint=None,
    )
    cancel_event = asyncio.Event()
    await backend.run(run, AsyncMock(), AsyncMock(), cancel_event)
    # tools: [] must resolve to NO tools, not the default whitelist.
    assert manager.recorded_toolsets == [[]]


@pytest.mark.asyncio
async def test_goal_path_keeps_default_toolsets():
    from extend.sub_agent.executor_profile import DEFAULT_EXECUTOR_TOOLSETS

    manager = _RecordingSessionManager()
    backend = AcpTaskBackend(
        session_manager=manager, progress_interval_seconds=0.0, stateless=True
    )
    run = TaskRunPayload(
        task_id="goal-task",
        attempt=1,
        goal="do the thing",
        params=None,
        context=None,
        toolsets=[],
        timeout_seconds=60,
        first_progress_seconds=None,
        trace_context=None,
        resume_from_checkpoint=None,
    )
    cancel_event = asyncio.Event()
    await backend.run(run, AsyncMock(), AsyncMock(), cancel_event)
    # No envelope + empty toolsets -> full planner whitelist (unchanged).
    assert manager.recorded_toolsets == [list(DEFAULT_EXECUTOR_TOOLSETS)]


@pytest.mark.asyncio
async def test_malformed_envelope_fails_fast():
    manager = _RecordingSessionManager()
    backend = AcpTaskBackend(
        session_manager=manager, progress_interval_seconds=0.0, stateless=True
    )
    run = TaskRunPayload(
        task_id="resp_bad",
        attempt=1,
        goal="g",
        params={"responses.v1": "{not json"},
        context=None,
        toolsets=[],
        timeout_seconds=60,
        first_progress_seconds=None,
        trace_context=None,
        resume_from_checkpoint=None,
    )
    cancel_event = asyncio.Event()
    result = await backend.run(run, AsyncMock(), AsyncMock(), cancel_event)
    assert result.status == "failed"
    assert result.error_code == "invalid_responses_payload"
    # No session was created for a fast-failed task.
    assert manager.recorded_toolsets == []


@pytest.mark.asyncio
async def test_acp_backend_completion_green_path():
    """A normal run without cancellation returns completed with usage/fields."""
    manager = FakeSessionManager(block_until_interrupt=False)
    backend = AcpTaskBackend(session_manager=manager, progress_interval_seconds=0.0)
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
    assert result.fields.get("acp_session_id") == "fake-xhermes-session"


@pytest.mark.asyncio
async def test_progress_throttling_drops_rapid_callbacks():
    """Only one progress frame is forwarded per throttling interval."""
    manager = FakeSessionManager(block_until_interrupt=True)
    backend = AcpTaskBackend(session_manager=manager, progress_interval_seconds=0.15)
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
    backend2 = AcpTaskBackend(session_manager=manager2, progress_interval_seconds=0.2)
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


@pytest.mark.asyncio
async def test_responses_envelope_emits_atresponses_progress():
    """P1: a responses task emits an @responses item-level progress
    envelope (not the legacy 'step N' text), and the envelope is full
    JSON — never sliced by the 240-char summary cap."""
    manager = _RecordingSessionManager()
    backend = AcpTaskBackend(
        session_manager=manager, progress_interval_seconds=0.0, stateless=True
    )
    run = TaskRunPayload(
        task_id="resp_p1",
        attempt=1,
        goal="g",
        params=_responses_params(),
        context=None,
        toolsets=["web"],
        timeout_seconds=60,
        first_progress_seconds=None,
        trace_context=None,
        resume_from_checkpoint=None,
    )
    cancel_event = asyncio.Event()
    progress: list[str] = []

    async def _on_progress(summary: str) -> None:
        progress.append(summary)

    await backend.run(run, _on_progress, AsyncMock(), cancel_event)
    await asyncio.sleep(0)  # drain scheduled progress frames

    import json as _json

    added = [p for p in progress if p.startswith('{"@responses":')]
    assert added, f"expected an @responses progress envelope, got {progress}"
    obj = _json.loads(added[0])  # must be valid JSON (not sliced)
    assert obj["@responses"] is True
    assert obj["type"] == "response.output_item.added"
    assert obj["item"]["type"] == "message"
    assert obj["item"]["status"] == "in_progress"
    # No legacy 'step N' text for responses tasks.
    assert not any(p.startswith("step ") for p in progress)
