"""Worker registry for the Task Relay Hub.

M1 scope:
- Announce / upsert workers.
- Basic eligibility checks for poll claims: mode-A support, ACL lists, toolsets.
- Resource scoring and Mode B/C scheduling are intentionally skipped (P3).
"""

import json
import time
from typing import Iterable

from extend.task_relay.hub.auth import WorkerClaims
from extend.task_relay.hub.db import Database
from extend.task_relay.hub.models import Task, Worker


class WorkerRegistry:
    """In-process facade over the workers table."""

    def __init__(self, db: Database):
        self._db = db

    async def announce(
        self,
        worker_id: str,
        *,
        session_modes: str = "A",
        toolsets: Iterable[str] = (),
        capabilities: dict | None = None,
        resources: dict | None = None,
        load: dict | None = None,
        max_concurrent: int = 1,
        wake_url: str | None = None,
        status: str = "idle",
        online_session_id: str | None = None,
    ) -> Worker:
        """Register or refresh a worker.

        Toolsets are folded into ``capabilities_json`` under the key
        ``"toolsets"`` so no schema migration is needed for M1.
        """
        caps = dict(capabilities) if capabilities is not None else {}
        caps["toolsets"] = list(toolsets)
        now = time.time()
        worker = Worker(
            worker_id=worker_id,
            wake_url=wake_url,
            session_modes=session_modes,
            capabilities_json=json.dumps(caps) if caps else None,
            resources_json=json.dumps(resources) if resources is not None else None,
            load_json=json.dumps(load) if load is not None else None,
            max_concurrent=max_concurrent,
            last_announce_at=now,
            last_heartbeat_at=now,
            status=status,
            online_session_id=online_session_id,
        )
        await self._db.upsert_worker(worker)
        return worker

    async def get_worker(self, worker_id: str) -> Worker | None:
        return await self._db.get_worker(worker_id)

    def toolsets(self, worker: Worker) -> set[str]:
        """Return the toolsets a worker advertised."""
        if not worker.capabilities_json:
            return set()
        try:
            caps = json.loads(worker.capabilities_json)
        except json.JSONDecodeError:
            return set()
        return set(caps.get("toolsets") or [])

    def supports_mode(self, worker: Worker, mode: str) -> bool:
        return mode.upper() in worker.session_modes.upper()

    def is_eligible_for_poll(
        self, worker: Worker, task: Task, claims: WorkerClaims | None = None
    ) -> bool:
        """Check ACL and capability requirements for a poll claim.

        Eligibility rules (M1):
        - Worker supports Mode A.
        - Worker status is not ``offline``, ``stale``, or ``draining``.
        - Worker is not denied by ``task.deny_worker_ids``.
        - If ``task.allowed_worker_ids`` is non-empty, worker must be in it.
        - Worker's advertised toolsets, optionally further restricted by the
          worker JWT ``allowed_toolsets`` scope, are a superset of task toolsets.
        """
        if not self.supports_mode(worker, "a"):
            return False
        if worker.status in {"offline", "stale", "draining"}:
            return False

        deny = _json_list(task.deny_worker_ids_json)
        if worker.worker_id in deny:
            return False

        allow = _json_list(task.allowed_worker_ids_json)
        if allow and worker.worker_id not in allow:
            return False

        task_toolsets = _json_list(task.toolsets_json)
        if task_toolsets:
            worker_toolsets = self.toolsets(worker)
            authorized_toolsets = worker_toolsets
            if claims is not None:
                authorized_toolsets = worker_toolsets & set(claims.allowed_toolsets)
            if not set(task_toolsets).issubset(authorized_toolsets):
                return False

        return True


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
