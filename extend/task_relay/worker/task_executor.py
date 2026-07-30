"""Task execution orchestration for the Mode A worker.

Defines the :class:`TaskBackend` protocol and the :class:`TaskExecutor` that
glue a backend to the WebSocket lifecycle frames (``task.progress``,
``task.checkpoint``, ``task.complete``).
"""

from __future__ import annotations

import asyncio
import logging
import traceback
from dataclasses import dataclass
from typing import Any, Awaitable, Callable, Protocol

logger = logging.getLogger("task_relay.worker.executor")


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


class TaskExecutor:
    """Runs one backend task and forwards lifecycle frames to the Hub."""

    def __init__(
        self,
        ws_client: Any,
        backend: TaskBackend,
        settlement_guard: Callable[[str], Awaitable[bool]] | None = None,
    ):
        self.ws_client = ws_client
        self.backend = backend
        self.settlement_guard = settlement_guard
        self._completion_state: str | None = None  # None | "pending" | "sent"

    @property
    def completion_attempted(self) -> bool:
        """True if ``task.complete`` has already been requested or dropped."""
        return self._completion_state in ("pending", "sent", "dropped")

    async def execute(
        self,
        run: TaskRunPayload,
        cancel_event: asyncio.Event,
    ) -> None:
        """Send claimed progress, run the backend, then send ``task.complete``."""
        task_id = run.task_id
        await self._progress(task_id, f"claimed, starting {run.goal[:80]}")

        async def on_progress(summary: str) -> None:
            await self._progress(task_id, summary)

        async def on_checkpoint(
            checkpoint_id: str,
            summary: str | None = None,
            fields: dict[str, Any] | None = None,
            resume_blob: str | bytes | None = None,
            lease_until: float | None = None,
        ) -> None:
            params: dict[str, Any] = {
                "task_id": task_id,
                "checkpoint_id": checkpoint_id,
            }
            if summary is not None:
                params["summary"] = summary
            if fields is not None:
                params["fields"] = fields
            if resume_blob is not None:
                params["resume_blob"] = resume_blob
            if lease_until is not None:
                params["lease_until"] = lease_until
            await self.ws_client.request("task.checkpoint", params)

        complete: TaskCompletePayload | None = None
        try:
            complete = await self.backend.run(run, on_progress, on_checkpoint, cancel_event)
        except asyncio.CancelledError:
            # Worker is shutting down or task was cancelled and the backend
            # propagated the cancellation. Record cancelled if we can.
            await self._complete_once(
                task_id,
                TaskCompletePayload(
                    status="cancelled",
                    summary="backend received cancellation",
                ),
            )
            raise
        except Exception:
            logger.exception("backend raised for task %s", task_id)
            complete = TaskCompletePayload(
                status="failed",
                summary="backend execution error",
                error=traceback.format_exc(),
            )

        await self._complete_once(task_id, complete)

    async def _progress(self, task_id: str, summary: str) -> None:
        try:
            await self.ws_client.request("task.progress", {"task_id": task_id, "summary": summary})
        except Exception:
            logger.exception("task.progress failed for %s", task_id)

    async def _complete_once(
        self,
        task_id: str,
        payload: TaskCompletePayload,
    ) -> bool:
        """Send ``task.complete`` exactly once. Returns True on success."""
        if self._completion_state is not None:
            return False
        if self.settlement_guard is not None:
            if not await self.settlement_guard(task_id):
                logger.info(
                    "task %s settlement guard rejected; dropping complete", task_id
                )
                self._completion_state = "dropped"
                return False
        self._completion_state = "pending"
        params: dict[str, Any] = {"task_id": task_id, "status": payload.status}
        if payload.summary is not None:
            params["summary"] = payload.summary
        if payload.result_text is not None:
            params["result_text"] = payload.result_text
        if payload.fields is not None:
            params["fields"] = payload.fields
        if payload.usage is not None:
            params["usage"] = payload.usage
        if payload.error is not None:
            params["error"] = payload.error
        try:
            await self.ws_client.request("task.complete", params)
        except Exception:
            logger.exception("task.complete failed for %s", task_id)
            raise
        self._completion_state = "sent"
        return True
