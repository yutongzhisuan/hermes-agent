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
- Execution timeout currently maps to ``lost`` (design spec says ``failed`` for
  hard execution timeout; this refinement is deferred).
"""

import asyncio
import json
import time
import uuid
from dataclasses import dataclass
from typing import Iterable

from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import Database
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.models import Task, TaskSpec
from extend.task_relay.hub.worker_registry import WorkerRegistry


VALID_STATUSES = frozenset(
    {"pending", "running", "cancelling", "completed", "failed", "lost", "cancelled"}
)
TERMINAL_STATUSES = frozenset({"completed", "failed", "lost", "cancelled"})
REDISPATCHABLE_STATUSES = frozenset({"lost", "failed"})


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

        # M1: store policy/depends_on metadata on each task but treat every task
        # as independent for scheduling purposes.
        responses: list[DispatchTaskResponse] = []
        async with self._lock:
            for sp in specs:
                if not sp.callback_topic:
                    sp.callback_topic = callback_topic
                resp = await self._dispatch_single(
                    sp, master_session_id, allow_redispatch, batch_id=batch_id
                )
                responses.append(resp)

        return BatchDispatchResponse(
            batch_id=batch_id,
            callback_topic=callback_topic,
            tasks=responses,
            idempotent_hit=all(r.idempotent_hit for r in responses),
        )

    async def atomic_claim_for_poll(
        self, worker_id: str, max_tasks: int
    ) -> list[ClaimedTask]:
        if max_tasks <= 0:
            return []

        worker = await self._registry.get_worker(worker_id)
        if worker is None:
            return []
        if not self._registry.supports_mode(worker, "a"):
            return []
        if worker.status in {"offline", "stale", "draining"}:
            return []

        # Pending tasks ordered by priority desc, then creation time asc.
        cursor = await self._db._conn.execute(
            "SELECT * FROM tasks WHERE status = 'pending'"
            " ORDER BY priority DESC, created_at ASC"
        )
        rows = await cursor.fetchall()

        claimed: list[ClaimedTask] = []
        async with self._lock:
            for row in rows:
                if len(claimed) >= max_tasks:
                    break
                task = Task(**dict(row))
                if not self._registry.is_eligible_for_poll(worker, task):
                    continue
                claimed_task = await self._claim_task(task, worker_id)
                if claimed_task is not None:
                    claimed.append(claimed_task)

        return claimed

    async def on_progress(self, task_id: str, summary: str) -> None:
        async with self._lock:
            task = await self._get_task_or_raise(task_id)
            if task.status not in {"running", "cancelling"}:
                return
            now = time.time()
            timeout = self._effective_timeout(task)
            task.first_progress_deadline_at = None
            task.claim_expires_at = now + timeout
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
            task.claim_expires_at = time.time() + grace
            await self._persist_task(task)
            await self._emit_progress(task, f"cancel requested: {reason}")
            return self._response_from_task(task, idempotent_hit=False)

    async def tick_timeouts(self) -> None:
        """Evaluate queue / first-progress / lease / cancel-grace deadlines."""
        now = time.time()
        async with self._lock:
            cursor = await self._db._conn.execute(
                "SELECT * FROM tasks WHERE status IN ('pending', 'running', 'cancelling')"
            )
            rows = await cursor.fetchall()
            for row in rows:
                task = Task(**dict(row))
                await self._timeout_task(task, now)

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

        if (
            allow_redispatch
            and task.allow_redispatch
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
            deadline = task.first_progress_deadline_at or task.claim_expires_at
            if deadline is not None and now >= deadline:
                await self._settle_lost(task, now, "progress/lease timeout")
            return

        if task.status == "cancelling":
            if task.claim_expires_at is not None and now >= task.claim_expires_at:
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
            return

        task.status = "lost"
        task.summary = reason
        task.completed_at = now
        task.claim_expires_at = None
        task.first_progress_deadline_at = None
        await self._persist_task(task)
        await self._emit_terminal(task, "lost", reason)

    async def _settle_cancelled(self, task: Task, now: float) -> None:
        task.status = "cancelled"
        task.summary = task.summary or "cancel grace expired"
        task.completed_at = now
        task.claim_expires_at = None
        task.first_progress_deadline_at = None
        await self._persist_task(task)
        await self._emit_terminal(task, "cancelled", task.summary)

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
            "status": task.status,
            "summary": task.summary,
            "error": task.error,
            "attempt": task.attempt,
            "max_attempts": task.max_attempts,
            "worker_id": task.worker_id,
            "completed_at": task.completed_at,
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

    async def _persist_task(self, task: Task) -> None:
        """Full row update; ``db.update_task_status`` only touches status."""
        fields = [
            "batch_id", "master_session_id", "goal", "params_json", "context_json",
            "toolsets_json", "worker_id", "status", "result_json", "summary",
            "fields_json", "usage_json", "error", "callback_topic", "allow_redispatch",
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


def _json_list(value: str | None) -> list[str]:
    if not value:
        return []
    try:
        parsed = json.loads(value)
    except json.JSONDecodeError:
        return []
    if isinstance(parsed, list):
        return [str(x) for x in parsed]
    return []
