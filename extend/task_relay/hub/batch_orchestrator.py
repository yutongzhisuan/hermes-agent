"""Batch DAG, BatchPolicy, and AGGREGATE orchestration (M3)."""

from __future__ import annotations

import json
import logging
import time
from typing import TYPE_CHECKING, Any

from extend.task_relay.hub.batch_policy import (
    completion_threshold_met,
    normalize_completion_mode,
)
from extend.task_relay.hub.json_util import safe_json_loads
from extend.task_relay.hub.metrics import inc, observe
from extend.task_relay.hub.models import Batch, Task, TaskSpec, _json_list
from extend.task_relay.hub.task_router import TaskRouterError

if TYPE_CHECKING:
    from extend.task_relay.hub.db import Database
    from extend.task_relay.hub.event_bus import EventBus
    from extend.task_relay.hub.task_router import TaskRouter

logger = logging.getLogger("task_relay.hub.batch")

TERMINAL_STATUSES = frozenset({"completed", "failed", "lost", "cancelled"})
FAILED_DEPENDENCY_STATUSES = frozenset({"failed", "lost", "cancelled"})


class BatchOrchestrator:
    """Optional Hub helpers: depends_on, BatchPolicy, aggregate_key."""

    def __init__(self, router: TaskRouter, db: Database, bus: EventBus):
        self._router = router
        self._db = db
        self._bus = bus
        self._batch_completion_recorded: set[str] = set()

    async def is_task_ready(self, task: Task) -> bool:
        """True when all depends_on tasks are completed."""
        deps = _json_list(task.depends_on_json)
        if not deps:
            return True
        for dep_id in deps:
            dep = await self._db.get_task(dep_id)
            if dep is None or dep.status != "completed":
                return False
        return True

    async def on_task_terminal(self, task: Task, status: str) -> list[str]:
        """Propagate dependency failures, apply batch policy, emit AGGREGATE."""
        ready: list[str] = []
        if status != "completed":
            await self._cancel_dependents(task.task_id, status)
        else:
            ready = await self._collect_newly_ready(task.task_id)

        if task.batch_id:
            await self._apply_batch_policy(task, status)
            if task.aggregate_key:
                await self._maybe_emit_aggregate(task)
        inc("relay_tasks_terminal_total", status=status)
        return ready

    async def enforce_batch_deadlines(self, now: float) -> None:
        cursor = await self._db._conn.execute(
            "SELECT * FROM batches WHERE batch_deadline_at IS NOT NULL AND batch_deadline_at <= ?",
            (now,),
        )
        for row in await cursor.fetchall():
            batch = Batch(**dict(row))
            await self._expire_batch(batch, now)

    @staticmethod
    def batch_deadline_from_policy(policy_json: str | None, created_at: float) -> float | None:
        if not policy_json:
            return None
        policy = safe_json_loads(policy_json)
        if not isinstance(policy, dict):
            return None
        timeout_ms = policy.get("batch_timeout_ms") or policy.get("batchTimeoutMs")
        if not timeout_ms:
            return None
        return created_at + float(timeout_ms) / 1000.0

    @staticmethod
    def check_dependency_cycles(specs: list[TaskSpec]) -> None:
        graph = {sp.task_id: _json_list(sp.depends_on_json) for sp in specs}
        visiting: set[str] = set()
        visited: set[str] = set()

        def visit(node: str, stack: list[str]) -> None:
            if node in visiting:
                raise TaskRouterError(f"dependency cycle detected: {' -> '.join(stack + [node])}")
            if node in visited:
                return
            visiting.add(node)
            for dep in graph.get(node, []):
                visit(dep, stack + [node])
            visiting.remove(node)
            visited.add(node)

        for node in graph:
            if node not in visited:
                visit(node, [])

    async def _collect_newly_ready(self, completed_task_id: str) -> list[str]:
        ready: list[str] = []
        for task in await self._db.list_pending_tasks():
            deps = _json_list(task.depends_on_json)
            if completed_task_id not in deps:
                continue
            if await self.is_task_ready(task):
                ready.append(task.task_id)
        return ready

    async def _cancel_dependents(self, failed_task_id: str, failed_status: str) -> None:
        queue = [failed_task_id]
        seen = {failed_task_id}
        while queue:
            dep_id = queue.pop(0)
            for task in await self._db.list_pending_tasks():
                if task.task_id in seen:
                    continue
                deps = _json_list(task.depends_on_json)
                if dep_id not in deps:
                    continue
                if not await self._depends_on_failed(task):
                    continue
                await self._router.cancel_as_dependency(task.task_id, dep_id, failed_status)
                seen.add(task.task_id)
                queue.append(task.task_id)

    async def _depends_on_failed(self, task: Task) -> bool:
        for dep_id in _json_list(task.depends_on_json):
            dep = await self._db.get_task(dep_id)
            if dep is None:
                continue
            if dep.status in FAILED_DEPENDENCY_STATUSES:
                return True
            if dep.status != "completed":
                return False
        return False

    async def _apply_batch_policy(self, task: Task, status: str) -> None:
        batch = await self._db.get_batch(task.batch_id)
        if batch is None:
            return

        policy = safe_json_loads(batch.policy_json) if batch.policy_json else None
        if isinstance(policy, dict):
            if policy.get("fail_fast") and status in FAILED_DEPENDENCY_STATUSES:
                await self._cancel_batch_siblings(
                    task.batch_id, exclude=task.task_id, reason="fail_fast"
                )
            if status == "completed":
                members = await self._db.list_tasks_by_batch(task.batch_id)
                if completion_threshold_met(members, policy):
                    mode = normalize_completion_mode(policy)
                    await self._cancel_batch_siblings(
                        task.batch_id,
                        exclude=task.task_id,
                        reason=f"batch {mode} threshold met",
                    )

        await self._maybe_record_batch_completion(task.batch_id)

    async def _cancel_batch_siblings(self, batch_id: str, *, exclude: str, reason: str) -> None:
        for member in await self._db.list_tasks_by_batch(batch_id):
            if member.task_id == exclude or member.status in TERMINAL_STATUSES:
                continue
            await self._router.on_cancel(member.task_id, reason=reason)

    async def _expire_batch(self, batch: Batch, now: float) -> None:
        for member in await self._db.list_tasks_by_batch(batch.batch_id):
            if member.status in TERMINAL_STATUSES:
                continue
            await self._router.on_cancel(member.task_id, reason="batch timeout")
        batch.batch_deadline_at = None
        await self._db.update_batch(batch)

    async def _maybe_emit_aggregate(self, task: Task) -> None:
        if not task.batch_id or not task.aggregate_key:
            return
        if await self._db.aggregate_event_exists(task.batch_id, task.aggregate_key):
            return
        members = [
            t
            for t in await self._db.list_tasks_by_batch(task.batch_id)
            if t.aggregate_key == task.aggregate_key
        ]
        if not members or any(t.status not in TERMINAL_STATUSES for t in members):
            return

        status_counts: dict[str, int] = {}
        metrics: list[dict[str, Any]] = []
        summaries: list[str] = []
        task_ids = [t.task_id for t in sorted(members, key=lambda t: t.created_at)]
        for member in members:
            status_counts[member.status] = status_counts.get(member.status, 0) + 1
            if member.summary:
                summaries.append(member.summary)
            fields = safe_json_loads(member.fields_json) or {}
            for metric in fields.get("metrics") or []:
                if isinstance(metric, dict):
                    stamped = dict(metric)
                    stamped.setdefault("origin_task_id", member.task_id)
                    metrics.append(stamped)

        payload = {
            "batch_id": task.batch_id,
            "aggregate_key": task.aggregate_key,
            "task_ids": task_ids,
            "status_counts": status_counts,
            "summary": " | ".join(summaries),
            "metrics": metrics,
            "schema_version": 1,
        }
        await self._bus.publish(
            callback_topic=task.callback_topic,
            task_id=None,
            batch_id=task.batch_id,
            kind="AGGREGATE",
            payload=payload,
        )
        inc("relay_aggregate_emitted_total", batch_id=task.batch_id)

    async def _maybe_record_batch_completion(self, batch_id: str) -> None:
        if batch_id in self._batch_completion_recorded:
            return
        members = await self._db.list_tasks_by_batch(batch_id)
        if not members or any(t.status not in TERMINAL_STATUSES for t in members):
            return
        batch = await self._db.get_batch(batch_id)
        if batch is None:
            return
        self._batch_completion_recorded.add(batch_id)
        mode = "ALL"
        if batch.policy_json:
            policy = safe_json_loads(batch.policy_json)
            if isinstance(policy, dict):
                mode = normalize_completion_mode(policy)
        observe(
            "relay_batch_completion_seconds",
            max(0.0, time.time() - batch.created_at),
            completion_mode=mode,
        )
