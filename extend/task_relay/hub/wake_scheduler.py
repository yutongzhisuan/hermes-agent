"""Mode B HTTP wake scheduler (M2)."""

from __future__ import annotations

import hashlib
import hmac
import logging
import time
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from extend.task_relay.hub.auth import Auth
    from extend.task_relay.hub.config import HubConfig
    from extend.task_relay.hub.db import Database
    from extend.task_relay.hub.worker_registry import WorkerRegistry

logger = logging.getLogger("task_relay.hub.wake")

DEFAULT_WAKE_TTL_SECONDS = 60


class WakeScheduler:
    """POST wake URLs for workers that advertise Mode B."""

    def __init__(
        self,
        db: Database,
        registry: WorkerRegistry,
        auth: Auth,
        config: HubConfig,
        *,
        relay_ws_url: str = "ws://127.0.0.1:9000",
        wake_ttl_seconds: int = DEFAULT_WAKE_TTL_SECONDS,
    ):
        self._db = db
        self._registry = registry
        self._secret = auth._secret  # same HMAC key as JWT signing
        self._relay_ws_url = relay_ws_url
        self._wake_ttl_seconds = wake_ttl_seconds
        self._consumed_tokens: set[str] = set()

    def _sign_wake(self, task_id: str, worker_id: str, expires_at: int) -> str:
        payload = f"{task_id}:{worker_id}:{expires_at}"
        return hmac.new(
            self._secret.encode(),
            payload.encode(),
            hashlib.sha256,
        ).hexdigest()

    def issue_wake_token(self, task_id: str, worker_id: str) -> tuple[str, float]:
        expires_at = time.time() + self._wake_ttl_seconds
        exp_int = int(expires_at)
        token = self._sign_wake(task_id, worker_id, exp_int)
        return token, expires_at

    def verify_wake_token(self, task_id: str, worker_id: str, token: str, expires_at: float) -> bool:
        if time.time() > expires_at:
            return False
        if token in self._consumed_tokens:
            return False
        expected = self._sign_wake(task_id, worker_id, int(expires_at))
        if not hmac.compare_digest(expected, token):
            return False
        self._consumed_tokens.add(token)
        return True

    async def schedule_wake(self, task_id: str, worker_id: str) -> bool:
        worker = await self._registry.get_worker(worker_id)
        if worker is None or not worker.wake_url:
            return False
        if "B" not in worker.session_modes.upper():
            return False

        token, expires_at = self.issue_wake_token(task_id, worker_id)
        body = {
            "task_id": task_id,
            "relay_url": self._relay_ws_url,
            "token": token,
            "expires_at": int(expires_at),
        }
        try:
            import aiohttp

            async with aiohttp.ClientSession() as session:
                async with session.post(worker.wake_url, json=body, timeout=30) as resp:
                    if resp.status not in (200, 202):
                        logger.debug(
                            "wake POST to %s returned %s for task %s",
                            worker.wake_url,
                            resp.status,
                            task_id,
                        )
                        return False
        except Exception:
            logger.debug("wake POST failed for task %s worker %s", task_id, worker_id, exc_info=True)
            return False
        return True
