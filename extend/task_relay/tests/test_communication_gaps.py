"""Tests for minimal progress mode and checkpoint emission."""

from __future__ import annotations

import asyncio
from unittest.mock import AsyncMock

import pytest

from extend.task_relay.acp_backend import AcpTaskBackend, _resume_goal
from extend.task_relay.progress_policy import PROGRESS_MODE_MINIMAL, RelayRuntimeOptions
from extend.task_relay.tests.test_acp_backend import (
    FakeSessionManager,
    _run_payload,
)


@pytest.mark.asyncio
async def test_minimal_progress_hides_tool_names():
    manager = FakeSessionManager(block_until_interrupt=False)
    backend = AcpTaskBackend(
        session_manager=manager,
        relay_options=RelayRuntimeOptions(progress_mode=PROGRESS_MODE_MINIMAL),
    )
    progress_calls: list[str] = []

    async def _on_progress(summary: str) -> None:
        progress_calls.append(summary)

    await backend.run(_run_payload(), _on_progress, AsyncMock(), asyncio.Event())
    assert progress_calls
    assert all("completed tools" not in item for item in progress_calls)


@pytest.mark.asyncio
async def test_checkpoint_every_steps_emits():
    manager = FakeSessionManager(block_until_interrupt=False)
    backend = AcpTaskBackend(
        session_manager=manager,
        relay_options=RelayRuntimeOptions(
            progress_mode=PROGRESS_MODE_MINIMAL,
            checkpoint_every_steps=1,
        ),
    )
    on_checkpoint = AsyncMock()
    await backend.run(_run_payload(), AsyncMock(), on_checkpoint, asyncio.Event())
    assert on_checkpoint.await_count >= 1


def test_resume_goal_injects_summary_from_params():
    run = _run_payload()
    run.resume_from_checkpoint = "cp-9"
    run.params = {"resume_summary": "Prior findings about EU AI Act."}
    message = _resume_goal(run)
    assert "Prior findings about EU AI Act." in message
    assert "cp-9" in message
