"""Mode A worker main loop.

Connects to the Hub over WebSocket, announces itself, polls for tasks, and
runs each claimed task through a :class:`TaskBackend` with bounded concurrency.
"""

from __future__ import annotations

import asyncio
import base64
import logging
import signal
import traceback
from typing import Any

import jwt

from extend.task_relay.worker.task_executor import (
    TaskBackend,
    TaskCancelEvent,
    TaskCompletePayload,
    TaskExecutor,
    TaskRunPayload,
)
from extend.task_relay.worker.task_worker_ws import TaskWorkerWs, WsClientError

logger = logging.getLogger("task_relay.worker")

# M1: terminal Hub statuses used by the settlement-ownership guard.
_TERMINAL_STATUSES = frozenset({"completed", "failed", "lost", "cancelled"})


class TaskWorker:
    """Long-lived worker process that executes tasks from the Hub."""

    def __init__(
        self,
        worker_id: str,
        relay_url: str,
        jwt: str,
        backend: TaskBackend,
        *,
        max_concurrent: int | None = None,
        poll_wait_ms: int = 5_000,
        initial_backoff_seconds: float = 1.0,
        max_backoff_seconds: float = 30.0,
        session_modes: list[str] | None = None,
    ):
        self.worker_id = worker_id
        self.relay_url = relay_url
        self.jwt = jwt
        self.backend = backend
        self.session_modes = [str(m).lower() for m in (session_modes or ["a"])]
        self.poll_wait_ms = poll_wait_ms
        self.initial_backoff_seconds = initial_backoff_seconds
        self.max_backoff_seconds = max_backoff_seconds

        requested = max(1, max_concurrent or 1)
        jwt_max = self._extract_jwt_max_concurrent(jwt)
        if jwt_max is not None and requested > jwt_max:
            logger.warning(
                "requested max_concurrent=%d exceeds JWT limit=%d; capping at %d",
                requested,
                jwt_max,
                jwt_max,
            )
            self.max_concurrent = max(1, jwt_max)
        else:
            self.max_concurrent = requested

        self._ws: TaskWorkerWs | None = None
        self._semaphore = asyncio.Semaphore(self.max_concurrent)
        self._running_tasks: set[asyncio.Task] = set()
        self._cancel_events: dict[str, TaskCancelEvent] = {}
        self._shutdown = asyncio.Event()
        self._heartbeat_task: asyncio.Task | None = None
        self._heartbeat_interval_ms: int = 30_000

    @staticmethod
    def _extract_jwt_max_concurrent(token: str) -> int | None:
        """Decode the worker JWT payload (without verifying signature) for the max_concurrent claim."""
        try:
            payload = jwt.decode(
                token,
                options={"verify_signature": False},
            )
        except Exception:
            logger.debug("unable to decode worker JWT payload")
            return None
        try:
            return int(payload.get("max_concurrent"))
        except (TypeError, ValueError):
            return None

    async def run(self) -> None:
        """Run the worker until ``_shutdown`` is set."""
        self._ws = TaskWorkerWs(self.relay_url, self.jwt)
        await self._ws.connect()
        try:
            self._ws.on_notification("task.cancel", self._on_cancel)

            announce_result = await self._ws.request(
                "worker.announce",
                {
                    "worker_id": self.worker_id,
                    "session_modes": self.session_modes,
                    "max_concurrent": self.max_concurrent,
                },
            )
            self._heartbeat_interval_ms = announce_result.get(
                "heartbeat_interval_ms", self._heartbeat_interval_ms
            )
            logger.info(
                "announced worker %s (max_concurrent=%d)",
                self.worker_id,
                self.max_concurrent,
            )

            self._heartbeat_task = asyncio.create_task(self._heartbeat_loop())
            await self._poll_loop()
        finally:
            await self._shutdown_cleanup()

    async def shutdown(self) -> None:
        """Signal the worker to stop accepting new work."""
        self._shutdown.set()

    async def _poll_loop(self) -> None:
        """Poll for work and dispatch tasks while honoring concurrency."""
        backoff = self.initial_backoff_seconds
        while not self._shutdown.is_set():
            await self._reap_finished_tasks()

            free_slots = self.max_concurrent - len(self._running_tasks)
            if free_slots <= 0:
                try:
                    await asyncio.wait_for(
                        self._shutdown.wait(),
                        timeout=0.2,
                    )
                except asyncio.TimeoutError:
                    pass
                continue

            try:
                result = await self._ws.request(
                    "worker.poll",
                    {
                        "max_wait_ms": self.poll_wait_ms,
                        "max_tasks": free_slots,
                        "prefer_atomic_claim": True,
                    },
                )
            except WsClientError as exc:
                logger.warning("poll error: %s", exc)
                await self._backoff_or_shutdown(backoff)
                backoff = min(backoff * 2, self.max_backoff_seconds)
                continue
            except Exception:
                logger.exception("poll failed")
                await self._backoff_or_shutdown(backoff)
                backoff = min(backoff * 2, self.max_backoff_seconds)
                continue

            if not result.get("offered"):
                logger.debug("empty poll, backing off %.2fs", backoff)
                await self._backoff_or_shutdown(backoff)
                backoff = min(backoff * 2, self.max_backoff_seconds)
                continue

            backoff = self.initial_backoff_seconds
            for task_info in result.get("tasks", []):
                run_payload = _run_payload_from_dict(task_info.get("run", {}))
                cancel_event = TaskCancelEvent()
                self._cancel_events[run_payload.task_id] = cancel_event
                task = asyncio.create_task(
                    self._execute_one(run_payload, cancel_event)
                )
                self._running_tasks.add(task)
                task.add_done_callback(self._running_tasks.discard)

    async def _execute_one(
        self,
        run: TaskRunPayload,
        cancel_event: TaskCancelEvent,
    ) -> None:
        """Execute a single task under the concurrency semaphore."""
        async with self._semaphore:

            async def settlement_guard(task_id: str) -> bool:
                return await self._guard_settlement(task_id, run.claim_token)

            executor = TaskExecutor(
                self._ws, self.backend, settlement_guard=settlement_guard
            )
            try:
                await executor.execute(run, cancel_event)
            except asyncio.CancelledError:
                logger.info("task %s cancelled/shutdown", run.task_id)
                raise
            except Exception:
                logger.exception("task %s execution failed", run.task_id)
                if not executor.completion_attempted:
                    try:
                        await executor._complete_once(
                            run.task_id,
                            TaskCompletePayload(
                                status="failed",
                                summary="worker execution error",
                                error=traceback.format_exc(),
                            ),
                        )
                    except Exception:
                        logger.exception(
                            "failed to send error completion for %s", run.task_id
                        )
            finally:
                self._cancel_events.pop(run.task_id, None)

    async def _on_cancel(self, params: dict[str, Any]) -> None:
        """Handle a server-pushed ``task.cancel`` notification."""
        task_id = params.get("task_id")
        reason = params.get("reason", "cancel requested")
        hard_deadline_at = params.get("hard_deadline_at")
        logger.info(
            "received cancel for task %s (reason=%s, deadline=%s)",
            task_id,
            reason,
            hard_deadline_at,
        )
        event = self._cancel_events.get(task_id)
        if event is not None:
            event.set(reason)
        # Acknowledge the cancel so the Hub knows the worker is aware.
        try:
            await self._ws.request("cancel.ack", {"task_id": task_id})
        except Exception:
            logger.exception("cancel.ack failed for %s", task_id)

    async def _guard_settlement(
        self, task_id: str, claim_token: str | None
    ) -> bool:
        """Return True if this worker still owns settlement for the task.

        The M1 guard queries the Hub before sending ``task.complete``. If the
        Hub has already marked the task terminal (e.g. lease timeout) or the
        claim token no longer matches, the worker drops its completion to avoid
        a double-settle race. Query failures fail open so a transient Hub
        connectivity blip does not lose a legitimate result.
        """
        try:
            result = await self._ws.request("task.status", {"task_id": task_id})
        except Exception:
            logger.exception("task.status query failed for %s", task_id)
            return True

        status = result.get("status")
        if status in _TERMINAL_STATUSES:
            logger.info(
                "task %s already terminal in Hub (%s); skipping complete",
                task_id,
                status,
            )
            return False

        returned_token = result.get("claim_token")
        if (
            claim_token is not None
            and returned_token is not None
            and returned_token != claim_token
        ):
            logger.info(
                "task %s claim token mismatch (worker=%s, hub=%s); skipping complete",
                task_id,
                claim_token,
                returned_token,
            )
            return False

        return True

    async def _heartbeat_loop(self) -> None:
        """Send periodic worker.heartbeat frames."""
        while not self._shutdown.is_set():
            try:
                await asyncio.wait_for(
                    self._shutdown.wait(),
                    timeout=self._heartbeat_interval_ms / 1000.0,
                )
            except asyncio.TimeoutError:
                pass
            else:
                break
            if self._shutdown.is_set():
                break
            try:
                await self._ws.request("worker.heartbeat", {})
            except asyncio.CancelledError:
                raise
            except Exception:
                logger.exception("heartbeat failed")

    async def _shutdown_cleanup(self) -> None:
        """Close the socket and wait for running tasks to finish."""
        if self._heartbeat_task is not None and not self._heartbeat_task.done():
            self._heartbeat_task.cancel()
            try:
                await self._heartbeat_task
            except asyncio.CancelledError:
                pass

        # Give backends a chance to observe cancellation.
        for event in self._cancel_events.values():
            event.set()

        if self._running_tasks:
            logger.info("waiting for %d running task(s) to finish", len(self._running_tasks))
            await asyncio.gather(*self._running_tasks, return_exceptions=True)

        if self._ws is not None:
            try:
                await self._ws.request("worker.close", {})
            except Exception:
                pass
            await self._ws.close()

    async def _reap_finished_tasks(self) -> None:
        """Remove already-finished tasks from the running set."""
        done = [t for t in self._running_tasks if t.done()]
        for t in done:
            self._running_tasks.discard(t)
            if (exc := t.exception()) is not None and not isinstance(
                exc, asyncio.CancelledError
            ):
                logger.warning("running task raised: %s", exc)

    async def _backoff_or_shutdown(self, seconds: float) -> None:
        """Sleep for ``seconds`` unless shutdown is requested."""
        try:
            await asyncio.wait_for(self._shutdown.wait(), timeout=seconds)
        except asyncio.TimeoutError:
            pass


def _run_payload_from_dict(run: dict[str, Any]) -> TaskRunPayload:
    """Convert the Hub's ``task.run`` dict into a typed payload."""
    context = _decode_inline_gzip(run.get("context"))
    return TaskRunPayload(
        task_id=run.get("task_id", ""),
        attempt=int(run.get("attempt", 1)),
        goal=run.get("goal", ""),
        params=run.get("params"),
        context=context,
        toolsets=list(run.get("toolsets") or []),
        timeout_seconds=int(run.get("timeout_seconds", 600)),
        first_progress_seconds=run.get("first_progress_seconds"),
        trace_context=run.get("trace_context"),
        resume_from_checkpoint=run.get("resume_from_checkpoint"),
        resume_blob=run.get("resume_blob"),
        claim_token=run.get("claim_token"),
    )


def _decode_inline_gzip(context: Any) -> Any:
    """Decode base64-encoded ``inline_gzip.gzip_data`` back to bytes.

    The Hub stores binary gzip payloads as base64 inside JSON; the worker
    backend expects the original bytes for decompression.
    """
    if not isinstance(context, dict):
        return context
    inline_gzip = context.get("inline_gzip")
    if not isinstance(inline_gzip, dict):
        return context
    encoded = inline_gzip.get("gzip_data")
    if isinstance(encoded, str):
        try:
            inline_gzip["gzip_data"] = base64.b64decode(encoded, validate=True)
        except Exception:
            # Leave malformed payloads as-is so the backend can fail cleanly.
            pass
    return context


def install_signal_handlers(worker: TaskWorker) -> None:
    """Install SIGINT/SIGTERM handlers that trigger graceful shutdown."""
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(sig, lambda s=sig: _on_signal(worker, s))


def _on_signal(worker: TaskWorker, signum: int) -> None:
    logger.info("received signal %s, initiating graceful shutdown", signal.Signals(signum).name)
    asyncio.create_task(worker.shutdown())
