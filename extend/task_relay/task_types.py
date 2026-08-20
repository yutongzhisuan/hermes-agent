"""Minimal Task Relay payload types used by the XHermes-side ACP code.

These mirror the definitions in the swarm-network worker
(``extend.task_relay.worker.task_executor``) so the ACP sidecar stays
wire-compatible without importing the worker package.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from typing import Any, Awaitable, Callable, Protocol


@dataclass
class TaskRunPayload:
    """The ``task.run`` payload delivered by the Hub inside a poll result."""

    task_id: str
    attempt: int
    goal: str
    params: dict[str, Any] | None
    context: dict[str, Any] | None
    toolsets: list[str]
    timeout_seconds: int
    first_progress_seconds: int | None
    trace_context: dict[str, Any] | None
    resume_from_checkpoint: str | None
    resume_blob: str | bytes | None = None
    claim_token: str | None = None
    master_session_id: str | None = None
    hub_id: str | None = None


@dataclass
class TaskCompletePayload:
    """Return value from a backend; drives the ``task.complete`` frame."""

    status: str  # completed | failed | cancelled | lost
    summary: str | None = None
    result_text: str | None = None
    fields: dict[str, Any] | None = None
    usage: dict[str, Any] | None = None
    error: str | None = None


OnProgress = Callable[[str], Awaitable[None]]
OnCheckpoint = Callable[..., Awaitable[None]]


class TaskCancelEvent(asyncio.Event):
    """asyncio.Event carrying an optional cancel reason from the Hub.

    The Task Relay Hub may push ``task.cancel`` with a ``reason`` field
    (e.g. ``timeout``). Backends that need to distinguish a normal cancel
    from a timeout-induced cancel can inspect :attr:`reason` after the
    event is set.
    """

    def __init__(self) -> None:
        super().__init__()
        self.reason: str | None = None

    def set(self, reason: str | None = None) -> None:
        self.reason = reason
        super().set()

    def clear(self) -> None:
        self.reason = None
        super().clear()


class TaskBackend(Protocol):
    """Pluggable execution engine for a claimed task."""

    async def run(
        self,
        run: TaskRunPayload,
        on_progress: OnProgress,
        on_checkpoint: OnCheckpoint,
        cancel_event: asyncio.Event,
    ) -> TaskCompletePayload:
        """Execute the task and return a terminal payload.

        The backend should periodically check ``cancel_event`` and return
        promptly with ``status="cancelled"`` once it is set.
        """
        ...
