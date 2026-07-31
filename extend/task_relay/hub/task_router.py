"""Task Relay Hub core state machine: dispatch, claim, progress, complete, cancel.

M1 state machine:

    pending → running → completed|failed|lost|cancelled

Implementation notes:
- ``cancelling`` is an internal row status used while a running task is inside
  the cancel-grace window. It is not part of the public proto vocabulary; the
  emitted event when entering ``cancelling`` is a PROGRESS frame carrying the
  cancel reason, and the final event is TERMINAL with ``cancelled``.
- ``claim_expires_at`` serves double duty as the execution lease deadline and
  the cancel-grace deadline. The router distinguishes the two cases by the row
  status (``running`` vs ``cancelling``).
- First-progress deadline expiry maps to ``lost``; execution/lease timeout maps
  to ``failed`` per the design spec Global Constraints.
"""

import asyncio
import hashlib
import json
import time
import uuid
from dataclasses import asdict, dataclass
from typing import Iterable

from extend.task_relay.constants import CANCEL_REASON_TIMEOUT
from extend.task_relay.hub.auth import WorkerClaims
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import Database
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.models import Batch, Task, TaskSpec, Worker, _json_list
from extend.task_relay.hub.worker_registry import WorkerRegistry


VALID_STATUSES = frozenset(
    {"pending", "running", "cancelling", "completed", "failed", "lost", "cancelled"}
)
TERMINAL_STATUSES = frozenset({"completed", "failed", "lost", "cancelled"})
REDISPATCHABLE_STATUSES = frozenset({"lost", "failed"})
PRUNE_INTERVAL_SECONDS = 3600


class TaskRouterError(Exception):
    """Any router-level validation or state-machine violation."""


@dataclass(frozen=True)
class DispatchTaskResponse:
    task_id: str
    batch_id: str | None
    callback_topic: str
    status: str
    idempotent_hit: bool
    attempt: int
    existing_result: dict | None = None


@dataclass(frozen=True)
class ClaimedTask:
    task_id: str
    goal: str
    params_json: str | None
    context_json: str | None
    toolsets_json: str | None
    timeout_seconds: int
    callback_topic: str
    attempt: int
    claim_token: str


@dataclass(frozen=True)
class BatchDispatchResponse:
    batch_id: str
    callback_topic: str
    tasks: list[DispatchTaskResponse]
    idempotent_hit: bool


class TaskRouter:
    def __init__(
        self,
        db: Database,
        bus: EventBus,
        config: HubConfig,
        registry: WorkerRegistry,
    ):
        self._db = db
        self._bus = bus
        self._config = config
        self._registry = registry
        self._lock = asyncio.Lock()
        self._last_prune_at = 0.0

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    async def dispatch_task(
        self,
        spec: TaskSpec,
        master_session_id: str,
        allow_redispatch: bool = False,
    ) -> DispatchTaskResponse:
        if not spec.task_id:
            raise TaskRouterError("task_id is required")
        if not spec.goal:
            raise TaskRouterError("goal is required")

        async with self._lock:
            existing = await self._db.get_task(spec.task_id)
            if existing is not None:
                return await self._handle_existing(existing, allow_redispatch)

            task = self._task_from_spec(spec, master_session_id, allow_redispatch)
            await self._db.insert_task(task)
            await self._emit_status(task, "pending")
            return self._response_from_task(task, idempotent_hit=False)

    async def dispatch_task_batch(
        self,
        specs: Iterable[TaskSpec],
        batch_id: str,
        master_session_id: str,
        callback_topic: str,
        allow_redispatch: bool = False,
        policy_json: str | None = None,
        depends_on_json: str | None = None,
    ) -> BatchDispatchResponse:
        if not batch_id:
            raise TaskRouterError("batch_id is required")

        specs = list(specs)
        self._check_dependency_cycles(specs)

        # Normalize specs so the batch hash is deterministic.
        for sp in specs:
            if not sp.callback_topic:
                sp.callback_topic = callback_topic

        batch_spec_hash = _batch_spec_hash(batch_id, specs)

        async with self._lock:
            existing_batch = await self._db.get_batch(batch_id)
            if existing_batch is not None:
                if existing_batch.batch_spec_hash != batch_spec_hash:
                    raise TaskRouterError(
                        f"batch_id {batch_id} already dispatched with different spec"
                    )
                tasks = await self._db.list_tasks_by_batch(batch_id)
                return BatchDispatchResponse(
                    batch_id=batch_id,
                    callback_topic=callback_topic,
                    tasks=[
                        self._response_from_task(t, idempotent_hit=True)
                        for t in tasks
                    ],
                    idempotent_hit=True,
                )

            await self._db.insert_batch(
                Batch(
                    batch_id=batch_id,
                    callback_topic=callback_topic,
                    batch_spec_hash=batch_spec_hash,
                    created_at=time.time(),
                    master_session_id=master_session_id,
                    policy_json=policy_json,
                )
            )

            responses: list[DispatchTaskResponse] = []
            for sp in specs:
                resp = await self._dispatch_single(
                    sp, master_session_id, allow_redispatch, batch_id=batch_id
                )
                responses.append(resp)

        return BatchDispatchResponse(
            batch_id=batch_id,
            callback_topic=callback_topic,
            tasks=responses,
            idempotent_hit=False,
        )

    async def atomic_claim_for_poll(
        self,
        worker_id: str,
        max_tasks: int,
        worker_claims: WorkerClaims | None = None,
    ) -> list[ClaimedTask]:
        if max_tasks <= 0:
            return []

        claimed: list[ClaimedTask] = []
        async with self._lock:
            worker = await self._registry.get_worker(worker_id)
            if worker is None:
                return []
            if not self._registry.supports_mode(worker, "a"):
                return []
            if worker.status in {"offline", "stale", "draining"}:
                return []

            capacity = max(0, worker.max_concurrent - worker.running_tasks)
            if capacity <= 0:
                return []
            max_tasks = min(max_tasks, capacity)

            # Pending tasks ordered by priority desc, then creation time asc.
            # Read inside the lock so capacity and queue are checked atomically.
            cursor = await self._db._conn.execute(
                "SELECT * FROM tasks WHERE status = 'pending'"
                " ORDER BY priority DESC, created_at ASC"
            )
            rows = await cursor.fetchall()

            for row in rows:
                if len(claimed) >= max_tasks:
                    break
                task = Task(**dict(row))
                if not self._registry.is_eligible_for_poll(worker, task, worker_claims):
                    continue
                claimed_task = await self._claim_task(task, worker_id)
                if claimed_task is not None:
                    claimed.append(claimed_task)
                    worker.running_tasks += 1

            now = time.time()
            worker.last_heartbeat_at = now
            worker.last_seen_at = now
            await self._db.upsert_worker(worker)

        return claimed

    async def on_progress(self, task_id: str, summary: str) -> None:
        async with self._lock:
            task = await self._get_task_or_raise(task_id)
            if task.status not in {"running", "cancelling"}:
                return
            now = time.time()
            task.first_progress_deadline_at = None
            # Do not extend the cancel-grace window when a task is cancelling.
            if task.status == "running":
                task.claim_expires_at = now + self._effective_timeout(task)
            await self._persist_task(task)
            await self._emit_progress(task, summary)

    async def on_complete(
        self,
        task_id: str,
        *,
        status: str,
        summary: str | None = None,
        result_json: str | None = None,
        fields_json: str | None = None,
        usage_json: str | None = None,
        error: str | None = None,
    ) -> DispatchTaskResponse:
        if status not in TERMINAL_STATUSES:
            raise TaskRouterError(f"on_complete requires a terminal status, got {status}")

        async with self._lock:
            task = await self._get_task_or_raise(task_id)
            if task.status in TERMINAL_STATUSES:
                # Terminal monotonic: first wins.
                return self._response_from_task(task, idempotent_hit=True)
            previous_status = task.status
            self._validate_transition(task.status, status)

            now = time.time()
            task.status = status
            task.summary = summary
            task.result_json = result_json
            task.fields_json = fields_json
            task.usage_json = usage_json
            task.error = error
            task.completed_at = now
            task.first_progress_deadline_at = None
            task.claim_expires_at = None
            await self._persist_task(task)
            await self._emit_terminal(task, status, summary, error)
            if previous_status in {"running", "cancelling"}:
                await self._release_task_slot(task.worker_id)
            return self._response_from_task(task, idempotent_hit=False)

    async def on_cancel(
        self, task_id: str, *, reason: str, grace_seconds: int | None = None
    ) -> DispatchTaskResponse:
        async with self._lock:
            task = await self._get_task_or_raise(task_id)
            if task.status in TERMINAL_STATUSES:
                return self._response_from_task(task, idempotent_hit=True)

            if task.status == "pending":
                task.status = "cancelled"
                task.summary = reason
                task.completed_at = time.time()
                await self._persist_task(task)
                await self._emit_terminal(task, "cancelled", reason)
                return self._response_from_task(task, idempotent_hit=False)

            # running (or already cancelling) -> enter cancelling grace window.
            grace = grace_seconds if grace_seconds is not None else self._config.cancel_grace_seconds
            task.status = "cancelling"
            task.cancel_reason = reason
            task.summary = reason
            task.claim_expires_at = time.time() + grace
            await self._persist_task(task)
            await self._emit_progress(task, f"cancel requested: {reason}")
            return self._response_from_task(task, idempotent_hit=False)

    async def tick_timeouts(self) -> None:
        """Evaluate queue / first-progress / lease / cancel-grace deadlines.

        Also mark workers stale when they have not announced or heartbeated
        within ``worker_stale_seconds``.
        """
        now = time.time()
        async with self._lock:
            cursor = await self._db._conn.execute(
                "SELECT * FROM tasks WHERE status IN ('pending', 'running', 'cancelling')"
            )
            rows = await cursor.fetchall()
            for row in rows:
                task = Task(**dict(row))
                await self._timeout_task(task, now)

            stale_deadline = now - self._config.worker_stale_seconds
            cursor = await self._db._conn.execute(
                "SELECT * FROM workers WHERE status NOT IN ('offline', 'stale')"
                " AND last_seen_at < ?",
                (stale_deadline,),
            )
            for row in await cursor.fetchall():
                worker = Worker(**dict(row))
                worker.status = "stale"
                await self._db.upsert_worker(worker)
        if now - self._last_prune_at >= PRUNE_INTERVAL_SECONDS:
            await self.prune_old_data()

    async def prune_old_data(self) -> None:
        """Delete events, checkpoints, and terminal tasks older than retention_days.

        M1 retention policy: prune only terminal tasks so in-flight work is never
        deleted. Events and checkpoints are pruned by their own timestamps,
        independent of task lifecycle. Rows are deleted in an order safe for
        databases both with and without foreign-key cascade support.
        """
        retention_seconds = self._config.retention_days * 86400
        cutoff = time.time() - retention_seconds
        async with self._lock:
            await self._db._conn.execute(
                "DELETE FROM checkpoints WHERE checkpoint_at < ?", (cutoff,)
            )
            await self._db._conn.execute(
                "DELETE FROM task_events WHERE event_at < ?", (cutoff,)
            )
            await self._db._conn.execute(
                "DELETE FROM tasks WHERE status IN (?, ?, ?, ?) AND completed_at < ?",
                ("completed", "failed", "lost", "cancelled", cutoff),
            )
            await self._db._conn.commit()
        self._last_prune_at = time.time()

    async def get_status(self, task_id: str) -> str:
        task = await self._db.get_task(task_id)
        if task is None:
            raise TaskRouterError(f"task {task_id} not found")
        return task.status

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    async def _handle_existing(
        self, task: Task, allow_redispatch: bool
    ) -> DispatchTaskResponse:
        if task.status not in TERMINAL_STATUSES:
            return self._response_from_task(task, idempotent_hit=True)

        requested_allow_redispatch = 1 if allow_redispatch else 0
        if task.allow_redispatch != requested_allow_redispatch:
            task.allow_redispatch = requested_allow_redispatch
            await self._persist_task(task)

        if (
            allow_redispatch
            and task.status in REDISPATCHABLE_STATUSES
            and task.attempt < task.max_attempts
        ):
            # Reopen for a new attempt.
            now = time.time()
            queue_timeout = self._effective_int(
                task.queue_timeout_seconds, self._config.queue_timeout_seconds
            )
            task.status = "pending"
            task.worker_id = None
            task.claim_token = None
            task.claim_expires_at = None
            task.first_progress_deadline_at = None
            task.queue_deadline_at = now + queue_timeout
            task.started_at = None
            task.completed_at = None
            task.summary = None
            task.cancel_reason = None
            task.result_json = None
            task.error = None
            await self._persist_task(task)
            await self._emit_status(task, "pending")
            return self._response_from_task(task, idempotent_hit=False)

        return self._response_from_task(
            task,
            idempotent_hit=True,
            existing_result=self._existing_result(task),
        )

    async def _dispatch_single(
        self,
        spec: TaskSpec,
        master_session_id: str,
        allow_redispatch: bool,
        batch_id: str | None = None,
    ) -> DispatchTaskResponse:
        """Internal dispatch helper, must be called under ``self._lock``."""
        existing = await self._db.get_task(spec.task_id)
        if existing is not None:
            resp = await self._handle_existing(existing, allow_redispatch)
            if batch_id and not existing.batch_id:
                existing.batch_id = batch_id
                await self._persist_task(existing)
            return resp

        task = self._task_from_spec(spec, master_session_id, allow_redispatch)
        task.batch_id = batch_id
        await self._db.insert_task(task)
        await self._emit_status(task, "pending")
        return self._response_from_task(task, idempotent_hit=False)

    async def _claim_task(self, task: Task, worker_id: str) -> ClaimedTask | None:
        """Claim one pending task atomically (caller holds ``self._lock``)."""
        # Re-read the row to avoid racing with another claim.
        fresh = await self._db.get_task(task.task_id)
        if fresh is None or fresh.status != "pending":
            return None

        now = time.time()
        first_progress = self._effective_int(
            fresh.first_progress_seconds, self._config.first_progress_seconds
        )
        timeout = self._effective_int(fresh.timeout_seconds, self._config.timeout_seconds)

        fresh.status = "running"
        fresh.worker_id = worker_id
        fresh.attempt += 1
        fresh.started_at = fresh.started_at or now
        fresh.first_progress_deadline_at = now + first_progress
        fresh.claim_expires_at = now + timeout
        fresh.claim_token = str(uuid.uuid4())

        await self._persist_task(fresh)
        await self._emit_status(fresh, "running")
        await self._emit_progress(
            fresh, f"claimed, starting {fresh.goal[:40]}"
        )
        return ClaimedTask(
            task_id=fresh.task_id,
            goal=fresh.goal,
            params_json=fresh.params_json,
            context_json=fresh.context_json,
            toolsets_json=fresh.toolsets_json,
            timeout_seconds=timeout,
            callback_topic=fresh.callback_topic,
            attempt=fresh.attempt,
            claim_token=fresh.claim_token,
        )

    async def _timeout_task(self, task: Task, now: float) -> None:
        if task.status == "pending":
            if task.queue_deadline_at is not None and now >= task.queue_deadline_at:
                await self._settle_lost(task, now, "queue timeout")
            return

        if task.status == "running":
            if (
                task.first_progress_deadline_at is not None
                and now >= task.first_progress_deadline_at
            ):
                await self._settle_lost(task, now, "first progress timeout")
                return
            if task.claim_expires_at is not None and now >= task.claim_expires_at:
                # Give the worker a cancel-grace window to settle before failing.
                await self._enter_cancelling(task, now, CANCEL_REASON_TIMEOUT)
                return
            return

        if task.status == "cancelling":
            if task.claim_expires_at is not None and now >= task.claim_expires_at:
                # Timeout-induced cancelling must eventually settle as failed.
                if task.cancel_reason == CANCEL_REASON_TIMEOUT:
                    await self._settle_failed(task, now, "execution/lease timeout")
                else:
                    await self._settle_cancelled(task, now)
            return

    async def _settle_lost(self, task: Task, now: float, reason: str) -> None:
        if task.allow_redispatch and task.attempt < task.max_attempts:
            # Requeue for another attempt instead of marking terminal.
            queue_timeout = self._effective_int(
                task.queue_timeout_seconds, self._config.queue_timeout_seconds
            )
            task.status = "pending"
            task.worker_id = None
            task.claim_token = None
            task.claim_expires_at = None
            task.first_progress_deadline_at = None
            task.queue_deadline_at = now + queue_timeout
            task.summary = f"{reason}; requeuing for attempt {task.attempt + 1}"
            await self._persist_task(task)
            await self._emit_progress(task, task.summary)
            await self._emit_status(task, "pending")
            return

        task.status = "lost"
        task.summary = reason
        task.completed_at = now
        task.claim_expires_at = None
        task.first_progress_deadline_at = None
        await self._persist_task(task)
        await self._emit_terminal(task, "lost", reason)
        await self._release_task_slot(task.worker_id)

    async def _settle_failed(self, task: Task, now: float, reason: str) -> None:
        if task.allow_redispatch and task.attempt < task.max_attempts:
            # Requeue for another attempt instead of marking terminal.
            queue_timeout = self._effective_int(
                task.queue_timeout_seconds, self._config.queue_timeout_seconds
            )
            task.status = "pending"
            task.worker_id = None
            task.claim_token = None
            task.claim_expires_at = None
            task.first_progress_deadline_at = None
            task.queue_deadline_at = now + queue_timeout
            task.summary = f"{reason}; requeuing for attempt {task.attempt + 1}"
            await self._persist_task(task)
            await self._emit_progress(task, task.summary)
            await self._emit_status(task, "pending")
            return

        task.status = "failed"
        task.summary = reason
        task.completed_at = now
        task.claim_expires_at = None
        task.first_progress_deadline_at = None
        await self._persist_task(task)
        await self._emit_terminal(task, "failed", reason)
        await self._release_task_slot(task.worker_id)

    async def _settle_cancelled(self, task: Task, now: float) -> None:
        task.status = "cancelled"
        task.summary = task.summary or "cancel grace expired"
        task.completed_at = now
        task.claim_expires_at = None
        task.first_progress_deadline_at = None
        await self._persist_task(task)
        await self._emit_terminal(task, "cancelled", task.summary)
        await self._release_task_slot(task.worker_id)

    async def _enter_cancelling(
        self, task: Task, now: float, reason: str
    ) -> None:
        """Move a running task into the cancel-grace window.

        Caller must hold ``self._lock``. The WS cancel monitor is responsible
        for pushing ``task.cancel`` to the worker.
        """
        grace = self._config.cancel_grace_seconds
        task.status = "cancelling"
        task.cancel_reason = reason
        task.summary = reason
        task.claim_expires_at = now + grace
        task.first_progress_deadline_at = None
        await self._persist_task(task)
        await self._emit_progress(task, f"cancel requested: {reason}")

    def _task_from_spec(
        self, spec: TaskSpec, master_session_id: str, allow_redispatch: bool
    ) -> Task:
        now = time.time()
        queue_timeout = self._effective_int(
            spec.queue_timeout_seconds, self._config.queue_timeout_seconds
        )
        first_progress = self._effective_int(
            spec.first_progress_seconds, self._config.first_progress_seconds
        )
        timeout = self._effective_int(spec.timeout_seconds, self._config.timeout_seconds)
        max_attempts = self._effective_int(spec.max_attempts, self._config.max_attempts)

        return Task(
            task_id=spec.task_id,
            goal=spec.goal,
            callback_topic=spec.callback_topic or "default",
            created_at=now,
            master_session_id=master_session_id,
            params_json=spec.params_json,
            context_json=spec.context_json,
            toolsets_json=spec.toolsets_json,
            target_worker=spec.target_worker,
            timeout_seconds=timeout,
            queue_timeout_seconds=queue_timeout,
            first_progress_seconds=first_progress,
            queue_deadline_at=now + queue_timeout,
            allow_redispatch=1 if allow_redispatch else 0,
            max_attempts=max_attempts,
            priority=spec.priority,
            depends_on_json=spec.depends_on_json,
            aggregate_key=spec.aggregate_key,
            min_resources_json=spec.min_resources_json,
            trace_context_json=spec.trace_context_json,
            allowed_worker_ids_json=spec.allowed_worker_ids_json,
            deny_worker_ids_json=spec.deny_worker_ids_json,
        )

    def _response_from_task(
        self,
        task: Task,
        *,
        idempotent_hit: bool,
        existing_result: dict | None = None,
    ) -> DispatchTaskResponse:
        return DispatchTaskResponse(
            task_id=task.task_id,
            batch_id=task.batch_id,
            callback_topic=task.callback_topic,
            status=task.status,
            idempotent_hit=idempotent_hit,
            attempt=task.attempt,
            existing_result=existing_result,
        )

    def _existing_result(self, task: Task) -> dict:
        return {
            "task_id": task.task_id,
            "status": task.status,
            "summary": task.summary,
            "result_text": task.result_json,
            "error": task.error,
            "worker_id": task.worker_id,
            "attempt": task.attempt,
            "max_attempts": task.max_attempts,
            "batch_id": task.batch_id,
            "latest_checkpoint_id": task.resume_from_checkpoint,
            "started_at": task.started_at,
            "completed_at": task.completed_at,
            "fields_json": task.fields_json,
            "usage_json": task.usage_json,
        }

    async def _get_task_or_raise(self, task_id: str) -> Task:
        task = await self._db.get_task(task_id)
        if task is None:
            raise TaskRouterError(f"task {task_id} not found")
        return task

    def _validate_transition(self, from_status: str, to_status: str) -> None:
        if from_status not in VALID_STATUSES or to_status not in VALID_STATUSES:
            raise TaskRouterError(f"invalid status in transition {from_status} -> {to_status}")
        allowed = {
            "pending": {"running", "cancelled", "lost"},
            "running": TERMINAL_STATUSES,
            "cancelling": {"cancelled", "completed", "failed", "lost"},
        }
        if to_status not in allowed.get(from_status, set()):
            raise TaskRouterError(f"invalid transition {from_status} -> {to_status}")

    def _effective_int(self, value: int | None, default: int) -> int:
        return default if value is None else value

    def _effective_timeout(self, task: Task) -> int:
        return self._effective_int(task.timeout_seconds, self._config.timeout_seconds)

    async def _release_task_slot(self, worker_id: str | None) -> None:
        """Decrement ``worker.running_tasks`` when a task leaves the worker."""
        if worker_id is None:
            return
        worker = await self._registry.get_worker(worker_id)
        if worker is None:
            return
        worker.running_tasks = max(0, worker.running_tasks - 1)
        await self._db.upsert_worker(worker)

    async def _persist_task(self, task: Task) -> None:
        """Full row update; ``db.update_task_status`` only touches status."""
        fields = [
            "batch_id", "master_session_id", "goal", "params_json", "context_json",
            "toolsets_json", "target_worker", "worker_id", "status", "result_json", "summary",
            "cancel_reason", "fields_json", "usage_json", "error", "callback_topic", "allow_redispatch",
            "claim_token", "claim_expires_at", "first_progress_deadline_at",
            "queue_deadline_at", "attempt", "max_attempts", "priority",
            "depends_on_json", "aggregate_key", "min_resources_json",
            "trace_context_json", "allowed_worker_ids_json", "deny_worker_ids_json",
            "resume_from_checkpoint", "timeout_seconds", "queue_timeout_seconds",
            "first_progress_seconds", "started_at", "completed_at",
        ]
        set_clause = ", ".join(f"{f} = :{f}" for f in fields)
        values = {f: getattr(task, f) for f in fields}
        values["task_id"] = task.task_id
        await self._db._conn.execute(
            f"UPDATE tasks SET {set_clause} WHERE task_id = :task_id", values
        )
        await self._db._conn.commit()

    async def _emit_status(self, task: Task, status: str) -> None:
        await self._bus.publish(
            callback_topic=task.callback_topic,
            task_id=task.task_id,
            batch_id=task.batch_id,
            kind="STATUS",
            payload={"status": status, "attempt": task.attempt},
        )

    async def _emit_progress(self, task: Task, summary: str) -> None:
        await self._bus.publish(
            callback_topic=task.callback_topic,
            task_id=task.task_id,
            batch_id=task.batch_id,
            kind="PROGRESS",
            payload={"summary": summary, "attempt": task.attempt},
        )

    async def _emit_terminal(
        self, task: Task, status: str, summary: str | None, error: str | None = None
    ) -> None:
        await self._bus.publish(
            callback_topic=task.callback_topic,
            task_id=task.task_id,
            batch_id=task.batch_id,
            kind="TERMINAL",
            payload={
                "status": status,
                "summary": summary,
                "error": error,
                "attempt": task.attempt,
            },
        )

    def _check_dependency_cycles(self, specs: list[TaskSpec]) -> None:
        """Simple DFS cycle detection over ``depends_on_json``.

        M1: only used for validation during batch dispatch; scheduling still
        treats every task as independent.
        """
        graph: dict[str, list[str]] = {}
        for sp in specs:
            deps = _json_list(sp.depends_on_json)
            graph[sp.task_id] = deps

        visiting: set[str] = set()
        visited: set[str] = set()

        def visit(node: str, stack: list[str]) -> None:
            if node in visiting:
                cycle = " -> ".join(stack + [node])
                raise TaskRouterError(f"dependency cycle detected: {cycle}")
            if node in visited:
                return
            visiting.add(node)
            for dep in graph.get(node, []):
                visit(dep, stack + [node])
            visiting.remove(node)
            visited.add(node)

        for node in list(graph):
            if node not in visited:
                visit(node, [])


# Fields that participate in the batch idempotency hash. They mirror the
# TaskSpec proto fields that affect task identity and execution semantics.
_BATCH_SPEC_FIELDS = (
    "task_id",
    "goal",
    "params_json",
    "context_json",
    "toolsets_json",
    "target_worker",
    "timeout_seconds",
    "callback_topic",
    "priority",
    "depends_on_json",
    "aggregate_key",
    "min_resources_json",
    "trace_context_json",
    "allowed_worker_ids_json",
    "deny_worker_ids_json",
    "queue_timeout_seconds",
    "max_attempts",
    "first_progress_seconds",
)


def _batch_spec_hash(batch_id: str, specs: list[TaskSpec]) -> str:
    """Deterministic SHA-256 over the batch identity and normalized specs."""
    ordered = sorted(specs, key=lambda s: s.task_id)
    payload = {
        "batch_id": batch_id,
        "specs": [
            {f: getattr(sp, f) for f in _BATCH_SPEC_FIELDS} for sp in ordered
        ],
    }
    canonical = json.dumps(payload, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()
