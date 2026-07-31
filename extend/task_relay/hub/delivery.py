"""Task delivery coordinator (M2): Mode C push and Mode B wake scheduling."""

from __future__ import annotations

import logging
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from extend.task_relay.hub.auth import Auth
    from extend.task_relay.hub.config import HubConfig
    from extend.task_relay.hub.db import Database
    from extend.task_relay.hub.task_router import TaskRouter
    from extend.task_relay.hub.wake_scheduler import WakeScheduler
    from extend.task_relay.hub.worker_registry import WorkerRegistry
    from extend.task_relay.hub.ws_server import WsHubServer

logger = logging.getLogger("task_relay.hub.delivery")


class DeliveryCoordinator:
    """Routes newly pending tasks to Mode C sessions or Mode B wake URLs."""

    def __init__(
        self,
        router: TaskRouter,
        registry: WorkerRegistry,
        db: Database,
        auth: Auth,
        config: HubConfig,
        wake: WakeScheduler | None = None,
    ):
        self._router = router
        self._registry = registry
        self._db = db
        self._auth = auth
        self._config = config
        self._wake = wake
        self._ws_hub: WsHubServer | None = None
        self._pushed_tasks: dict[str, str] = {}

    def attach_ws_hub(self, hub: WsHubServer) -> None:
        self._ws_hub = hub

    async def on_task_pending(self, task_id: str) -> None:
        task = await self._db.get_task(task_id)
        if task is None or task.status != "pending":
            return
        if task.target_worker:
            if await self._try_mode_c_push(task_id, task.target_worker):
                return
            if self._wake is not None:
                await self._wake.schedule_wake(task_id, task.target_worker)
            return

        workers = await self._db.list_workers()
        for worker in workers:
            if await self._try_mode_c_push(task_id, worker.worker_id):
                return
        if self._wake is not None:
            for worker in workers:
                if worker.wake_url and "B" in worker.session_modes.upper():
                    await self._wake.schedule_wake(task_id, worker.worker_id)
                    break

    async def on_credit_granted(self, worker_id: str) -> None:
        workers = await self._db.list_workers()
        for worker in workers:
            if worker.worker_id != worker_id:
                continue
            cursor = await self._db._conn.execute(
                "SELECT task_id FROM tasks WHERE status = 'pending'"
                " ORDER BY priority DESC, created_at ASC"
            )
            for row in await cursor.fetchall():
                if worker.credit_available <= 0:
                    break
                if await self._try_mode_c_push(row["task_id"], worker_id):
                    worker = await self._registry.get_worker(worker_id)
                    if worker is None or worker.credit_available <= 0:
                        break

    async def on_task_terminal(self, task_id: str, worker_id: str | None) -> None:
        if task_id not in self._pushed_tasks:
            return
        del self._pushed_tasks[task_id]
        if worker_id is None:
            return
        worker = await self._registry.get_worker(worker_id)
        if worker is None:
            return
        worker.credit_available = min(
            worker.max_concurrent,
            worker.credit_available + 1,
        )
        await self._db.upsert_worker(worker)
        await self.on_credit_granted(worker_id)

    async def _try_mode_c_push(self, task_id: str, worker_id: str) -> bool:
        if self._ws_hub is None:
            return False
        worker = await self._registry.get_worker(worker_id)
        if worker is None or "C" not in worker.session_modes.upper():
            return False
        if worker.status in {"offline", "stale", "draining"}:
            return False
        if worker.credit_available <= 0 or not worker.online_session_id:
            return False

        task = await self._db.get_task(task_id)
        if task is None or task.status != "pending":
            return False
        if not self._registry.is_eligible_for_poll(worker, task, None):
            return False

        claimed = await self._router.claim_task_for_worker(task_id, worker_id, None)
        if claimed is None:
            return False

        worker = await self._registry.get_worker(worker_id)
        if worker is None:
            return False
        worker.credit_available = max(0, worker.credit_available - 1)
        await self._db.upsert_worker(worker)

        run_payload = await self._ws_hub.build_run_payload(claimed.task_id, claimed)
        pushed = await self._ws_hub.push_task_run(
            worker_id,
            worker.online_session_id,
            run_payload,
        )
        if not pushed:
            worker.credit_available += 1
            await self._db.upsert_worker(worker)
            await self._router.on_complete(
                claimed.task_id,
                status="lost",
                summary="Mode C push delivery failed",
            )
            return False

        self._pushed_tasks[claimed.task_id] = worker_id
        return True
