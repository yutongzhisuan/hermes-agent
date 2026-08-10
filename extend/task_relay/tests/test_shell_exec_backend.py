"""Tests for the shell-exec worker backend."""

from __future__ import annotations

import asyncio
import json
import time

import pytest

from extend.task_relay.worker.backends.shell_exec_backend import ShellExecBackend
from extend.task_relay.worker.task_executor import TaskRunPayload


def _run_payload(params: dict | None, timeout_seconds: int = 60) -> TaskRunPayload:
    return TaskRunPayload(
        task_id="t-shell",
        attempt=1,
        goal="shell exec",
        params=params,
        context=None,
        toolsets=["shell"],
        timeout_seconds=timeout_seconds,
        first_progress_seconds=None,
        trace_context=None,
        resume_from_checkpoint=None,
    )


async def _noop_progress(_summary: str) -> None:
    return None


async def _noop_checkpoint(*_args, **_kwargs) -> None:
    return None


async def _exec(params: dict | None, cancel_event: asyncio.Event | None = None):
    backend = ShellExecBackend()
    return await backend.run(
        _run_payload(params),
        _noop_progress,
        _noop_checkpoint,
        cancel_event or asyncio.Event(),
    )


def _exec_fields(payload) -> dict:
    return json.loads(payload.fields["extensions"]["exec"])


@pytest.mark.asyncio
async def test_echo_success():
    payload = await _exec({"cmd": "echo hello"})
    assert payload.status == "completed"
    assert payload.error is None
    ext = _exec_fields(payload)
    assert ext["exit_code"] == 0
    assert "hello" in ext["stdout"]
    assert ext["timed_out"] is False
    assert ext["canceled"] is False
    assert "hello" in payload.result_text


@pytest.mark.asyncio
async def test_nonzero_exit():
    payload = await _exec({"cmd": "echo oops >&2; exit 3"})
    assert payload.status == "failed"
    ext = _exec_fields(payload)
    assert ext["exit_code"] == 3
    assert "oops" in ext["stderr"]
    assert payload.error


@pytest.mark.asyncio
async def test_timeout_kill():
    start = time.monotonic()
    payload = await _exec({"cmd": "sleep 60", "timeout_seconds": 1})
    elapsed = time.monotonic() - start
    assert payload.status == "failed"
    assert elapsed < 5
    ext = _exec_fields(payload)
    assert ext["timed_out"] is True
    assert ext["canceled"] is False


@pytest.mark.asyncio
async def test_cancel():
    cancel_event = asyncio.Event()
    backend = ShellExecBackend()

    async def _cancel_soon():
        await asyncio.sleep(0.2)
        cancel_event.set()

    task = asyncio.create_task(
        backend.run(
            _run_payload({"cmd": "sleep 60"}),
            _noop_progress,
            _noop_checkpoint,
            cancel_event,
        )
    )
    cancels = asyncio.create_task(_cancel_soon())
    payload = await asyncio.wait_for(task, timeout=10)
    await cancels
    assert payload.status == "cancelled"
    ext = _exec_fields(payload)
    assert ext["canceled"] is True
    assert ext["timed_out"] is False


@pytest.mark.asyncio
async def test_output_tail_truncation():
    over = ShellExecBackend.MAX_OUTPUT + 4096
    payload = await _exec({"cmd": f"head -c {over} /dev/zero | tr '\\0' 'y'"})
    assert payload.status == "completed"
    ext = _exec_fields(payload)
    assert len(ext["stdout"]) <= ShellExecBackend.MAX_OUTPUT


@pytest.mark.asyncio
async def test_missing_cmd():
    payload = await _exec({})
    assert payload.status == "failed"
    assert "cmd" in (payload.error or "")
