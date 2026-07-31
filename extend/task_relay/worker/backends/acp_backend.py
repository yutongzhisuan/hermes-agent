"""ACP execution backend for the Task Relay worker.

Runs a relay task through an in-process Hermes ACP session managed by
:mod:`acp_adapter.session`. Progress is throttled and forwarded as
``task.progress`` frames; the final agent result drives ``task.complete``.

Cancel semantics:
- A normal ``task.cancel`` calls ``agent.interrupt()`` and, once the agent
  returns, settles the task as ``cancelled`` while salvaging any partial
  ``final_response`` as the summary.
- A timeout cancel (``task.cancel`` with ``reason`` equal to
  ``CANCEL_REASON_TIMEOUT``) still interrupts the agent, but the worker settles
  ``failed`` if it still owns settlement. The Hub will mark ``failed`` otherwise.

M1 scope: no L2 resume; cancelled tasks are settled immediately.
"""

from __future__ import annotations

import asyncio
import logging
import threading
import time
from typing import Any

from extend.task_relay.constants import CANCEL_REASON_TIMEOUT
from extend.task_relay.worker.task_executor import (
    OnCheckpoint,
    OnProgress,
    TaskBackend,
    TaskCompletePayload,
    TaskRunPayload,
)

logger = logging.getLogger("task_relay.worker.backends.acp")


def _import_session_manager() -> Any:
    """Lazy import so the backend module is importable without ACP deps."""
    from acp_adapter.session import SessionManager

    return SessionManager


class AcpTaskBackend(TaskBackend):
    """Backend that executes relay tasks via a Hermes ACP session."""

    def __init__(
        self,
        session_manager: Any | None = None,
        progress_interval_seconds: float = 5.0,
    ):
        """Initialize the backend.

        Args:
            session_manager: Optional ``acp_adapter.session.SessionManager``
                instance. When omitted, a fresh ``SessionManager`` is created
                on first use. Tests can inject a fake manager here.
            progress_interval_seconds: Minimum seconds between ``task.progress``
                frames forwarded from the agent.
        """
        self._session_manager = session_manager
        self._progress_interval_seconds = progress_interval_seconds

    async def run(
        self,
        run: TaskRunPayload,
        on_progress: OnProgress,
        on_checkpoint: OnCheckpoint,
        cancel_event: asyncio.Event,
    ) -> TaskCompletePayload:
        """Create an ACP session, run the goal, and return a terminal payload."""
        manager = self._session_manager
        if manager is None:
            manager = _import_session_manager()()

        user_message = _resume_goal(run)
        state = manager.create_session(cwd=".")
        session_id = state.session_id
        agent = state.agent

        loop = asyncio.get_running_loop()

        # Throttled progress relay from the executor thread to the event loop.
        last_progress_at = 0.0
        progress_lock = threading.Lock()

        def _step_callback(api_call_count: int, prev_tools: Any = None) -> None:
            nonlocal last_progress_at
            summary = f"step {api_call_count}"
            if prev_tools:
                names: list[str] = []
                for tool in prev_tools:
                    if isinstance(tool, dict):
                        name = tool.get("name") or tool.get("function_name")
                        if name:
                            names.append(str(name))
                    elif isinstance(tool, str):
                        names.append(tool)
                if names:
                    summary = f"completed tools: {', '.join(names)}"

            with progress_lock:
                now = time.time()
                if now - last_progress_at < self._progress_interval_seconds:
                    return
                last_progress_at = now

            try:
                fut = asyncio.run_coroutine_threadsafe(
                    on_progress(summary[:240]), loop
                )
            except RuntimeError:
                logger.warning(
                    "progress frame dropped for session %s: event loop closed",
                    session_id,
                )
            else:

                def _log_progress_failure(f: asyncio.Future[Any]) -> None:
                    try:
                        f.result()
                    except Exception:
                        logger.exception(
                            "progress frame failed for session %s", session_id
                        )

                fut.add_done_callback(_log_progress_failure)

        agent.step_callback = _step_callback

        def _run_agent() -> dict[str, Any]:
            return agent.run_conversation(
                user_message=user_message,
                conversation_history=state.history,
                task_id=session_id,
                persist_user_message=user_message,
            )

        async def _watch_cancel() -> None:
            """Set the ACP session cancel_event and interrupt the agent."""
            try:
                await cancel_event.wait()
            except asyncio.CancelledError:
                return
            try:
                if state.cancel_event is not None:
                    state.cancel_event.set()
                if hasattr(agent, "interrupt"):
                    agent.interrupt()
            except Exception:
                logger.debug(
                    "ACP cancel failed for session %s", session_id, exc_info=True
                )

        watch_task = asyncio.create_task(_watch_cancel())
        result: dict[str, Any] | None = None

        try:
            result = await loop.run_in_executor(None, _run_agent)
        except asyncio.CancelledError:
            # Worker is shutting down or the task itself was cancelled.
            # Ensure the agent notices by re-raising after cleanup in finally.
            raise
        except Exception as exc:
            logger.exception("ACP backend run failed for task %s", run.task_id)
            return TaskCompletePayload(
                status="failed",
                summary="ACP execution error",
                error=f"{type(exc).__name__}: {exc}",
            )
        finally:
            watch_task.cancel()
            try:
                await watch_task
            except asyncio.CancelledError:
                pass
            if cancel_event.is_set():
                # Defensive cancel in case the watch task lost the race.
                try:
                    if state.cancel_event is not None:
                        state.cancel_event.set()
                    if hasattr(agent, "interrupt"):
                        agent.interrupt()
                except Exception:
                    logger.debug(
                        "Defensive ACP cancel failed for session %s",
                        session_id,
                        exc_info=True,
                    )

        # Persist any updated history so salvageable context is not lost.
        if result and result.get("messages"):
            state.history = result["messages"]
            try:
                manager.save_session(session_id)
            except Exception:
                logger.debug(
                    "Failed to save ACP session %s", session_id, exc_info=True
                )

        cancelled = cancel_event.is_set()
        reason = getattr(cancel_event, "reason", None)

        # Timeout attribution: a Hub timeout cancel must settle as failed.
        if cancelled and reason == CANCEL_REASON_TIMEOUT:
            return TaskCompletePayload(
                status="failed",
                summary="execution timeout",
                error="execution timeout",
            )

        if cancelled:
            summary = result.get("final_response") if result else None
            if not summary:
                summary = reason or "cancelled"
            return TaskCompletePayload(
                status="cancelled",
                summary=str(summary)[:500],
                fields=self._extract_fields(result, run),
            )

        # Completed / failed path.
        if result and result.get("failed"):
            return TaskCompletePayload(
                status="failed",
                summary=result.get("final_response") or "ACP reported failure",
                error=result.get("error"),
                fields=self._extract_fields(result, run),
            )

        return TaskCompletePayload(
            status="completed",
            summary=result.get("final_response") if result else "",
            result_text=result.get("final_response") if result else "",
            fields=self._extract_fields(result, run),
            usage=self._extract_usage(result),
        )

    @staticmethod
    def _extract_usage(result: dict[str, Any] | None) -> dict[str, Any] | None:
        """Build a relay usage dict from token fields in the agent result."""
        if not result:
            return None
        keys = (
            "prompt_tokens",
            "completion_tokens",
            "total_tokens",
            "reasoning_tokens",
            "cache_read_tokens",
        )
        usage = {k: result[k] for k in keys if result.get(k) is not None}
        return usage if usage else None

    @staticmethod
    def _extract_fields(
        result: dict[str, Any] | None, run: TaskRunPayload
    ) -> dict[str, Any] | None:
        """Build relay result fields from agent result metadata."""
        if not result:
            return None
        fields: dict[str, Any] = {}
        if "api_calls" in result:
            fields["api_calls"] = result["api_calls"]
        if "session_id" in result:
            fields["acp_session_id"] = result["session_id"]
        if "interrupted" in result:
            fields["interrupted"] = result["interrupted"]
        if run.params:
            fields["params"] = run.params
        return fields if fields else None


def _resume_goal(run: TaskRunPayload) -> str:
    """Build the user message, injecting L1 summary when L2 blob is unavailable."""
    goal = run.goal or ""
    if not run.resume_from_checkpoint:
        return goal
    if isinstance(run.resume_blob, bytes):
        return f"[Resuming from checkpoint {run.resume_from_checkpoint}]\n{goal}"
    if isinstance(run.resume_blob, str) and run.resume_blob.strip():
        return f"[Resuming from checkpoint {run.resume_from_checkpoint}]\n{run.resume_blob}\n{goal}"
    return f"[Resuming from checkpoint {run.resume_from_checkpoint}]\n{goal}"
