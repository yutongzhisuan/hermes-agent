"""Stub task backend for testing the Mode A worker without an LLM.

The stub echoes the task goal into a short result, optionally sleeps for a
configurable duration, and cooperatively checks ``cancel_event`` so cancel
paths can be exercised end-to-end.
"""

from __future__ import annotations

import asyncio
import time
from dataclasses import dataclass
from typing import Any, Awaitable, Callable

from extend.task_relay.worker.task_executor import OnCheckpoint, OnProgress, TaskBackend, TaskCompletePayload, TaskRunPayload


@dataclass
class StubBackendConfig:
    sleep_seconds: float = 0.1
    fail_after_seconds: float | None = None


class StubBackend(TaskBackend):
    """Backend that pretends to execute a task for integration tests."""

    def __init__(self, config: StubBackendConfig | None = None):
        self.config = config or StubBackendConfig()

    async def run(
        self,
        run: TaskRunPayload,
        on_progress: OnProgress,
        on_checkpoint: OnCheckpoint,
        cancel_event: asyncio.Event,
    ) -> TaskCompletePayload:
        goal = run.goal or ""
        await on_progress(f"stub started: {goal[:80]}")

        sleep_total = self.config.sleep_seconds
        sleep_chunk = 0.05
        slept = 0.0
        while slept < sleep_total:
            if cancel_event.is_set():
                await on_progress("stub cancelled by worker")
                return TaskCompletePayload(
                    status="cancelled",
                    summary=f"cancelled while echoing: {goal[:80]}",
                )
            await asyncio.sleep(min(sleep_chunk, sleep_total - slept))
            slept += sleep_chunk

            if (
                self.config.fail_after_seconds is not None
                and slept >= self.config.fail_after_seconds
            ):
                return TaskCompletePayload(
                    status="failed",
                    summary=f"stub failure after {slept:.2f}s",
                    error="configured stub failure",
                )

        summary = f"stub completed: {goal}"
        result_text = f"echo({goal})"
        await on_progress(summary)
        return TaskCompletePayload(
            status="completed",
            summary=summary,
            result_text=result_text,
            fields={"stub": True, "duration_seconds": round(slept, 3)},
        )
