"""Active sub-agent task context for executor-only tools (e.g. report_progress)."""

from __future__ import annotations

import asyncio
import contextvars
import time
import uuid
from dataclasses import dataclass, field
from typing import Any, Awaitable, Callable, Mapping

from extend.sub_agent.progress_policy import SubAgentRuntimeOptions

OnCheckpoint = Callable[..., Awaitable[None]]


@dataclass
class TaskRunContext:
    """Holds per-task callbacks while an ACP backend run is active."""

    task_id: str
    on_checkpoint: OnCheckpoint
    options: SubAgentRuntimeOptions
    checkpoint_seq: int = 0
    _last_report_at: float = field(default=0.0, repr=False)

    async def emit_checkpoint(
        self,
        summary: str,
        *,
        fields: Mapping[str, Any] | None = None,
        resume_blob: str = "",
        force: bool = False,
        min_interval_s: float | None = None,
    ) -> bool:
        """Emit an L1 checkpoint; returns False when rate-limited."""
        summary = (summary or "").strip()
        if not summary:
            return False
        now = time.time()
        interval = (
            float(min_interval_s)
            if min_interval_s is not None
            else self.options.report_progress_interval_s
        )
        if not force and self._last_report_at and now - self._last_report_at < interval:
            return False
        self.checkpoint_seq += 1
        checkpoint_id = f"cp-{self.task_id}-{self.checkpoint_seq}"
        await self.on_checkpoint(
            checkpoint_id=checkpoint_id,
            summary=summary[:500],
            fields=dict(fields or {}),
            resume_blob=resume_blob or "",
        )
        self._last_report_at = now
        return True


_active_context: contextvars.ContextVar[TaskRunContext | None] = contextvars.ContextVar(
    "sub_agent_active_context", default=None
)


def bind_task_context(ctx: TaskRunContext) -> contextvars.Token:
    return _active_context.set(ctx)


def reset_task_context(token: contextvars.Token) -> None:
    _active_context.reset(token)


def get_task_context() -> TaskRunContext | None:
    return _active_context.get()


def new_checkpoint_id(task_id: str, seq: int) -> str:
    suffix = uuid.uuid4().hex[:8]
    return f"cp-{task_id}-{seq}-{suffix}"
