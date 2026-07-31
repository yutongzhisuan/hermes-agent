"""Mode A WebSocket JSON-RPC server for the Task Relay Hub (M1).

A worker connects, authenticates via bearer JWT on the WS upgrade, announces
itself, then polls for tasks. The Hub atomically claims pending tasks and
returns them inside ``worker.poll_result`` frames. Lifecycle frames
(``task.progress``, ``task.checkpoint``, ``task.complete``) are accepted on the
same socket.

This module exposes :func:`serve_ws`, a factory that can be passed to
``websockets.asyncio.server.serve`` (or awaited directly) so Task 8 can wire it
to a real port.
"""

from __future__ import annotations

import asyncio
import base64
import json
import logging
import time
import uuid
from dataclasses import asdict
from typing import TYPE_CHECKING, Any, Awaitable, Callable

if TYPE_CHECKING:
    from extend.task_relay.hub.delivery import DeliveryCoordinator
    from extend.task_relay.hub.wake_scheduler import WakeScheduler

logger = logging.getLogger("task_relay.hub.ws")

from websockets.asyncio.server import Response, ServerConnection, serve
from websockets.datastructures import Headers

from extend.task_relay.constants import CANCEL_REASON_TIMEOUT
from extend.task_relay.hub.auth import Auth, AuthError, WorkerClaims
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import Database
from extend.task_relay.hub.json_util import safe_json_dict_loads
from extend.task_relay.hub.metrics import inc
from extend.task_relay.hub.models import Checkpoint, Task
from extend.task_relay.hub.run_payload import (
    build_preview_payload,
    build_run_payload as build_task_run_payload,
)
from extend.task_relay.hub.task_router import TaskRouter, TaskRouterError
from extend.task_relay.hub.worker_registry import WorkerRegistry

JSONRPC_PARSE_ERROR = -32700
JSONRPC_INVALID_REQUEST = -32600
JSONRPC_METHOD_NOT_FOUND = -32601
JSONRPC_INVALID_PARAMS = -32602
JSONRPC_INTERNAL_ERROR = -32603
JSONRPC_DOMAIN_ERROR = -32000

_RESULT_METHOD_LABELS = {
    "worker.announce": "worker.announce_ok",
    "worker.poll": "worker.poll_result",
    "worker.claim": "worker.claim_ok",
    "worker.heartbeat": "worker.heartbeat_ok",
    "worker.drain": "worker.drain_ok",
    "worker.credit": "worker.credit_ok",
    "task.checkpoint": "checkpoint.ack",
}

DEFAULT_HEARTBEAT_INTERVAL_MS = 30_000
DEFAULT_POLL_MAX_WAIT_MS = 60_000
DEFAULT_RESUME_BLOB_MAX_BYTES = 1_048_576


class WsServerError(Exception):
    """Domain error inside the WS server; mapped to a JSON-RPC error response."""

    def __init__(self, message: str, code: int = JSONRPC_DOMAIN_ERROR):
        super().__init__(message)
        self.message = message
        self.code = code


def _jsonrpc_response(msg_id: Any, result: dict | None = None) -> dict:
    return {"jsonrpc": "2.0", "id": msg_id, "result": result or {}}


def _jsonrpc_error(msg_id: Any, code: int, message: str) -> dict:
    return {"jsonrpc": "2.0", "id": msg_id, "error": {"code": code, "message": message}}


def _parse_authorization(headers: Headers) -> str | None:
    """Extract a Bearer token from the Authorization header, if present."""
    auth = headers.get("Authorization", "")
    if not auth:
        return None
    parts = auth.split()
    if len(parts) != 2 or parts[0].lower() != "bearer":
        return None
    return parts[1]


def _safe_json_loads(data: bytes | str | None) -> dict | None:
    return safe_json_dict_loads(data)


def _json_dumps(payload: dict) -> str:
    return json.dumps(payload, separators=(",", ":"))


class WsHubServer:
    """Shared state and handler factory for worker WS sessions."""

    def __init__(
        self,
        router: TaskRouter,
        auth: Auth,
        registry: WorkerRegistry,
        db: Database,
        config: HubConfig,
        delivery: DeliveryCoordinator | None = None,
        wake: WakeScheduler | None = None,
    ):
        self.router = router
        self.auth = auth
        self.registry = registry
        self.db = db
        self.config = config
        self.delivery = delivery
        self.wake = wake
        self._resume_blob_max_bytes = config.resume_blob_max_bytes
        self._sessions: set[WsServerSession] = set()

    async def _process_request(
        self, connection: ServerConnection, request: Any
    ) -> Response | None:
        """Pre-upgrade hook: reject WS upgrade without a valid worker JWT."""
        token = _parse_authorization(request.headers)
        if token is None:
            return Response(
                401,
                "Unauthorized",
                Headers([("WWW-Authenticate", 'Bearer realm="task-relay-hub"')]),
                b'{"error":"missing Authorization header"}',
            )
        try:
            self.auth.verify_worker_jwt(token)
        except AuthError as exc:
            logger.debug("worker JWT verification failed: %s", exc)
            return Response(
                401,
                "Unauthorized",
                Headers([("WWW-Authenticate", 'Bearer realm="task-relay-hub"')]),
                _json_dumps({"error": "Invalid or missing token"}).encode(),
            )
        return None

    async def _ws_handler(self, connection: ServerConnection) -> None:
        """Handle one authenticated WebSocket connection."""
        token = _parse_authorization(connection.request.headers)
        # process_request already validated the token; this cannot fail.
        claims = self.auth.verify_worker_jwt(token)  # type: ignore[arg-type]
        session = WsServerSession(self, connection, claims)
        self._sessions.add(session)
        try:
            await session.run()
        finally:
            self._sessions.discard(session)

    def serve(
        self,
        host: str = "127.0.0.1",
        port: int = 0,
        **kwargs: Any,
    ) -> Awaitable:
        """Return a ``websockets.serve`` awaitable bound to this hub."""
        # Allow larger inbound frames than the default 1 MiB so the Hub can
        # apply its own checkpoint resume_blob limit and return a clean error.
        if "max_size" not in kwargs:
            kwargs["max_size"] = 10 * 1024 * 1024
        return serve(
            self._ws_handler,
            host,
            port,
            process_request=self._process_request,
            **kwargs,
        )

    async def push_cancel(self, task_id: str, reason: str, hard_deadline_at: float) -> None:
        """Best-effort push of a ``task.cancel`` frame to the worker that owns it.

        Looks up the worker's active ``online_session_id`` and sends only to the
        matching session, so a stale connection cannot receive cancel frames
        intended for the current one.
        """
        task = await self.db.get_task(task_id)
        if task is None or task.worker_id is None:
            return
        worker = await self.registry.get_worker(task.worker_id)
        if worker is None or worker.online_session_id is None:
            return
        for session in list(self._sessions):
            if session.session_id == worker.online_session_id:
                await session.send_notification(
                    "task.cancel",
                    {
                        "task_id": task_id,
                        "reason": reason,
                        "hard_deadline_at": hard_deadline_at,
                    },
                )
                break

    async def push_task_run(
        self,
        worker_id: str,
        session_id: str,
        run_payload: dict[str, Any],
    ) -> bool:
        for session in list(self._sessions):
            if (
                session.worker_id == worker_id
                and session.session_id == session_id
                and session.announced
            ):
                await session.send_notification("task.run", run_payload)
                return True
        return False

    async def build_run_payload(self, task_id: str, claimed: Any) -> dict[str, Any]:
        return await build_task_run_payload(
            self.db,
            task_id,
            claimed,
            decrypt_secret=self.config.jwt_secret,
            encrypt_at_rest=self.config.encrypt_inline_context_at_rest,
        )


class WsServerSession:
    """One authenticated worker WebSocket session."""

    def __init__(
        self,
        hub: WsHubServer,
        connection: ServerConnection,
        claims: WorkerClaims,
    ):
        self.hub = hub
        self.connection = connection
        self.claims = claims
        self.worker_id: str | None = None
        self.session_id: str | None = None
        self.announced = False
        self.draining = False
        self._mode_c = False
        self._closed = False
        self._close_after_response = False
        self._cancel_monitor_task: asyncio.Task | None = None
        self._notified_cancelling: set[str] = set()

    # ------------------------------------------------------------------
    # Lifecycle
    # ------------------------------------------------------------------

    async def run(self) -> None:
        """Main message loop for this connection."""
        try:
            async for raw in self.connection:
                await self._handle_raw(raw)
        except Exception as exc:  # pragma: no cover - defensive logging surface
            if not self._closed:
                try:
                    await self.connection.close(
                        code=1011,
                        reason=f"internal error: {exc}",
                    )
                except Exception:
                    pass
        finally:
            self._closed = True
            if self._cancel_monitor_task is not None:
                self._cancel_monitor_task.cancel()
                try:
                    await self._cancel_monitor_task
                except asyncio.CancelledError:
                    pass
            await self._set_worker_offline_if_still_owned()

    async def _is_current_session_for_worker(self) -> bool:
        """Return True if this session is still the active session for the worker."""
        if self.worker_id is None or self.session_id is None:
            return False
        worker = await self.hub.registry.get_worker(self.worker_id)
        return worker is not None and worker.online_session_id == self.session_id

    async def _set_worker_offline_if_still_owned(self) -> None:
        """Mark the worker offline when its WS drops without a graceful close.

        Only writes ``offline`` when this session is still the active session
        for the worker. A newer session would have replaced
        ``worker.online_session_id``, so leave the worker alone in that case.
        """
        if not await self._is_current_session_for_worker():
            return
        worker = await self.hub.registry.get_worker(self.worker_id)
        if worker is None:
            return
        if worker.status != "draining":
            worker.status = "offline"
        worker.online_session_id = None
        await self.hub.db.upsert_worker(worker)

    async def _handle_raw(self, raw: str | bytes) -> None:
        """Parse and dispatch one JSON-RPC request."""
        if isinstance(raw, bytes):
            try:
                raw = raw.decode("utf-8")
            except UnicodeDecodeError:
                await self.send(_jsonrpc_error(None, JSONRPC_PARSE_ERROR, "invalid utf-8"))
                return

        try:
            payload = json.loads(raw)
        except json.JSONDecodeError as exc:
            await self.send(_jsonrpc_error(None, JSONRPC_PARSE_ERROR, f"parse error: {exc}"))
            return

        if not isinstance(payload, dict) or payload.get("jsonrpc") != "2.0":
            msg_id = payload.get("id") if isinstance(payload, dict) else None
            await self.send(_jsonrpc_error(msg_id, JSONRPC_INVALID_REQUEST, "not JSON-RPC 2.0"))
            return

        msg_id = payload.get("id")
        method = payload.get("method")
        if not isinstance(method, str):
            await self.send(
                _jsonrpc_error(msg_id, JSONRPC_INVALID_REQUEST, "method must be a string")
            )
            return

        params = payload.get("params") or {}
        if not isinstance(params, dict):
            await self.send(_jsonrpc_error(msg_id, JSONRPC_INVALID_PARAMS, "params must be object"))
            return

        handler = getattr(self, f"_handle_{method.replace('.', '_')}", None)
        if handler is None:
            await self.send(_jsonrpc_error(msg_id, JSONRPC_METHOD_NOT_FOUND, f"unknown method {method}"))
            return

        try:
            result = await handler(params)
        except WsServerError as exc:
            await self.send(_jsonrpc_error(msg_id, exc.code, exc.message))
        except TaskRouterError as exc:
            await self.send(_jsonrpc_error(msg_id, JSONRPC_DOMAIN_ERROR, str(exc)))
        except Exception as exc:  # pragma: no cover - defensive
            await self.send(_jsonrpc_error(msg_id, JSONRPC_INTERNAL_ERROR, f"internal error: {exc}"))
        else:
            result = dict(result) if result else {}
            result["_method"] = _RESULT_METHOD_LABELS.get(method, method)
            await self.send(_jsonrpc_response(msg_id, result))
            if self._close_after_response:
                await self.connection.close(code=1000, reason="worker.close")

    async def send(self, payload: dict) -> None:
        """Send a JSON-RPC response or notification."""
        if self._closed:
            return
        await self.connection.send(_json_dumps(payload))

    async def send_notification(self, method: str, params: dict) -> None:
        """Send a server-initiated JSON-RPC notification (no id)."""
        await self.send({"jsonrpc": "2.0", "method": method, "params": params})

    # ------------------------------------------------------------------
    # Method handlers
    # ------------------------------------------------------------------

    async def _handle_worker_announce(self, params: dict) -> dict:
        """Register or refresh the worker announced on this session."""
        worker_id = params.get("worker_id")
        if not worker_id:
            raise WsServerError("worker_id is required", JSONRPC_INVALID_PARAMS)
        if worker_id != self.claims.sub:
            raise WsServerError("worker_id does not match JWT sub", JSONRPC_INVALID_PARAMS)

        session_modes = params.get("session_modes", ["A"])
        if isinstance(session_modes, str):
            session_modes = [session_modes]
        modes_upper = [str(m).upper() for m in session_modes]
        if "A" not in modes_upper:
            raise WsServerError("Mode A is mandatory for all workers", JSONRPC_INVALID_PARAMS)

        max_concurrent = int(params.get("max_concurrent", self.claims.max_concurrent))
        max_concurrent = min(max_concurrent, self.claims.max_concurrent)
        if max_concurrent <= 0:
            raise WsServerError("max_concurrent must be > 0", JSONRPC_INVALID_PARAMS)

        toolsets = params.get("toolsets") or params.get("capabilities", {}).get("toolsets") or []
        capabilities = params.get("capabilities", {})
        if "toolsets" not in capabilities and toolsets:
            capabilities["toolsets"] = list(toolsets)

        self.session_id = str(uuid.uuid4())
        initial_credit = params.get("credit")
        credit_available = 0
        if "C" in modes_upper and initial_credit is not None:
            credit_available = max(0, min(int(initial_credit), max_concurrent))

        await self.hub.registry.announce(
            worker_id,
            session_modes="".join(modes_upper),
            toolsets=toolsets,
            capabilities=capabilities or None,
            resources=params.get("resources"),
            load=params.get("load"),
            max_concurrent=max_concurrent,
            wake_url=params.get("wake_url"),
            status="idle",
            online_session_id=self.session_id,
            drain_requested=False,
        )
        if credit_available:
            worker = await self.hub.registry.get_worker(worker_id)
            if worker is not None:
                worker.credit_available = credit_available
                await self.hub.db.upsert_worker(worker)

        self.worker_id = worker_id
        self.announced = True
        self._mode_c = "C" in modes_upper
        self._cancel_monitor_task = asyncio.create_task(self._cancel_monitor_loop())

        if self._mode_c and self.hub.delivery is not None:
            await self.hub.delivery.on_credit_granted(worker_id)

        return {
            "session_id": self.session_id,
            "heartbeat_interval_ms": DEFAULT_HEARTBEAT_INTERVAL_MS,
            "server_time": int(time.time() * 1000),
        }

    async def _handle_worker_poll(self, params: dict) -> dict:
        """Atomically claim up to ``max_tasks`` and return them in a poll_result."""
        self._require_announced()
        max_wait_ms = int(params.get("max_wait_ms", DEFAULT_POLL_MAX_WAIT_MS))
        max_tasks = int(params.get("max_tasks", 1))
        prefer_atomic_claim = params.get("prefer_atomic_claim", True)
        if max_tasks <= 0:
            raise WsServerError("max_tasks must be > 0", JSONRPC_INVALID_PARAMS)

        # Bound the long-poll wait to a sane maximum.
        wait_s = min(max_wait_ms / 1000.0, DEFAULT_POLL_MAX_WAIT_MS / 1000.0)
        if prefer_atomic_claim:
            claimed = await self._wait_for_claims(wait_s, max_tasks)
            if not claimed:
                return {"offered": False}

            tasks_out = []
            for claimed_task in claimed:
                task = await self.hub.db.get_task(claimed_task.task_id)
                run_payload = await self.hub.build_run_payload(claimed_task.task_id, claimed_task)
                tasks_out.append(
                    {
                        "claimed": True,
                        "task_id": claimed_task.task_id,
                        "attempt": claimed_task.attempt,
                        "claim_token": claimed_task.claim_token,
                        "claim_expires_at": task.claim_expires_at if task else None,
                        "run": run_payload,
                    }
                )
            return {"offered": True, "tasks": tasks_out}

        offered = await self._wait_for_offers(wait_s, max_tasks)
        if not offered:
            return {"offered": False}

        tasks_out = []
        for offered_task in offered:
            task = await self.hub.db.get_task(offered_task.task_id)
            if task is None:
                continue
            tasks_out.append(
                {
                    "claimed": False,
                    "task_id": offered_task.task_id,
                    "claim_token": offered_task.claim_token,
                    "claim_expires_at": int(offered_task.claim_expires_at),
                    "preview": build_preview_payload(task, offered_task),
                }
            )
        if not tasks_out:
            return {"offered": False}
        return {"offered": True, "tasks": tasks_out}

    async def _wait_for_claims(self, wait_s: float, max_tasks: int) -> list:
        """Short-poll for work; returns as soon as at least one task is claimed."""
        deadline = time.time() + wait_s
        while True:
            claimed = await self.hub.router.atomic_claim_for_poll(
                self.worker_id, max_tasks, self.claims
            )
            if claimed:
                return claimed
            if time.time() >= deadline:
                return []
            await asyncio.sleep(min(0.2, deadline - time.time()))

    async def _wait_for_offers(self, wait_s: float, max_tasks: int) -> list:
        """Short-poll for two-step offers without atomic claim."""
        deadline = time.time() + wait_s
        while True:
            offered = await self.hub.router.offer_tasks_for_poll(
                self.worker_id, max_tasks, self.claims
            )
            if offered:
                return offered
            if time.time() >= deadline:
                return []
            await asyncio.sleep(min(0.2, deadline - time.time()))

    async def _handle_task_progress(self, params: dict) -> dict:
        """Extend the task lease and emit a progress event."""
        self._require_announced()
        task_id = params.get("task_id")
        summary = params.get("summary", "")
        if not task_id:
            raise WsServerError("task_id is required", JSONRPC_INVALID_PARAMS)
        await self._verify_task_owner(task_id)
        await self.hub.router.on_progress(task_id, summary)
        return {"acknowledged": True}

    async def _handle_task_checkpoint(self, params: dict) -> dict:
        """Persist an L1 checkpoint and optional L2 resume_blob."""
        self._require_announced()
        task_id = params.get("task_id")
        checkpoint_id = params.get("checkpoint_id")
        if not task_id or not checkpoint_id:
            raise WsServerError("task_id and checkpoint_id are required", JSONRPC_INVALID_PARAMS)
        await self._verify_task_owner(task_id)

        resume_blob_raw = params.get("resume_blob")
        resume_blob: bytes | None = None
        if resume_blob_raw is not None:
            if isinstance(resume_blob_raw, str):
                # The worker base64-encodes binary blobs for JSON transport; decode
                # back to the original bytes. Fall back to UTF-8 for plain strings.
                try:
                    resume_blob = base64.b64decode(resume_blob_raw, validate=True)
                except Exception:
                    resume_blob = resume_blob_raw.encode("utf-8")
            elif isinstance(resume_blob_raw, bytes):
                resume_blob = resume_blob_raw
            else:
                raise WsServerError("resume_blob must be string or bytes", JSONRPC_INVALID_PARAMS)
            if len(resume_blob) > self.hub._resume_blob_max_bytes:
                raise WsServerError(
                    f"resume_blob exceeds {self.hub._resume_blob_max_bytes} bytes",
                    JSONRPC_INVALID_PARAMS,
                )

        # Persist L1 fields. L2 resume_blob is stored opaquely for worker resume.
        fields = params.get("fields")
        fields_json = json.dumps(fields) if fields is not None else None
        summary = params.get("summary")
        lease_until = params.get("lease_until")
        checkpoint_at = time.time()

        # Extend the task lease so the checkpoint also counts as progress.
        await self.hub.router.on_progress(task_id, summary or "checkpoint")

        event = await self.hub.db.append_event(
            callback_topic=(await self.hub.db.get_task(task_id)).callback_topic,
            task_id=task_id,
            kind="CHECKPOINT",
            payload={
                "checkpoint_id": checkpoint_id,
                "summary": summary,
                "fields_json": fields_json,
            },
            event_at=checkpoint_at,
        )

        checkpoint = Checkpoint(
            checkpoint_id=checkpoint_id,
            task_id=task_id,
            event_id=event.event_id,
            checkpoint_at=checkpoint_at,
            summary=summary,
            fields_json=fields_json,
            resume_blob=resume_blob,
            lease_until=lease_until,
        )
        await self.hub.db.insert_checkpoint(checkpoint)
        inc("relay_checkpoint_count", worker_id=self.worker_id)

        # Update task.resume_from_checkpoint so redispatch can attach it.
        task = await self.hub.db.get_task(task_id)
        task.resume_from_checkpoint = checkpoint_id
        await self.hub.router._persist_task(task)

        return {
            "event_id": event.event_id,
            "checkpoint_id": checkpoint_id,
            "lease_until": lease_until,
        }

    async def _handle_task_complete(self, params: dict) -> dict:
        """Record a terminal result for the task."""
        self._require_announced()
        task_id = params.get("task_id")
        status = params.get("status")
        if not task_id or not status:
            raise WsServerError("task_id and status are required", JSONRPC_INVALID_PARAMS)
        await self._verify_task_owner(task_id)

        fields = params.get("fields")
        usage = params.get("usage")
        resp = await self.hub.router.on_complete(
            task_id,
            status=status,
            summary=params.get("summary"),
            result_json=params.get("result_text"),
            fields_json=json.dumps(fields) if fields is not None else None,
            usage_json=json.dumps(usage) if usage is not None else None,
            error=params.get("error"),
        )
        return {
            "task_id": resp.task_id,
            "status": resp.status,
            "attempt": resp.attempt,
        }

    async def _handle_task_status(self, params: dict) -> dict:
        """Return the current Hub status and claim token for a worker's task."""
        self._require_announced()
        task_id = params.get("task_id")
        if not task_id:
            raise WsServerError("task_id is required", JSONRPC_INVALID_PARAMS)
        task = await self._verify_task_owner(task_id)
        return {"status": task.status, "claim_token": task.claim_token}

    async def _handle_cancel_ack(self, params: dict) -> dict:
        """Worker acknowledges a pushed ``task.cancel``."""
        self._require_announced()
        task_id = params.get("task_id")
        if not task_id:
            raise WsServerError("task_id is required", JSONRPC_INVALID_PARAMS)
        await self._verify_task_owner(task_id)
        return {"acknowledged": True}

    async def _handle_worker_credit(self, params: dict) -> dict:
        """Refresh Mode C push credit and attempt to deliver pending tasks."""
        self._require_announced()
        if not await self._is_current_session_for_worker():
            return {}
        available = int(params.get("available", 0))
        worker = await self.hub.registry.get_worker(self.worker_id)
        if worker is None:
            return {}
        free = max(0, worker.max_concurrent - worker.running_tasks)
        worker.credit_available = max(0, min(available, free))
        await self.hub.db.upsert_worker(worker)
        if self.hub.delivery is not None:
            await self.hub.delivery.on_credit_granted(self.worker_id)
        return {"accepted": worker.credit_available}

    async def _handle_worker_claim(self, params: dict) -> dict:
        """Claim a task after Mode B wake or optional two-step poll."""
        self._require_announced()
        task_id = params.get("task_id")
        wake_token = params.get("wake_token")
        claim_token = params.get("claim_token")
        if not task_id:
            raise WsServerError("task_id is required", JSONRPC_INVALID_PARAMS)

        if claim_token:
            claimed = await self.hub.router.claim_offered_task(
                task_id, self.worker_id, str(claim_token), self.claims
            )
            if claimed is None:
                raise WsServerError("claim failed", JSONRPC_DOMAIN_ERROR)
            run_payload = await self.hub.build_run_payload(task_id, claimed)
            return {
                "claimed": True,
                "task_id": task_id,
                "claim_token": claimed.claim_token,
                "run": run_payload,
            }

        task = await self.hub.db.get_task(task_id)
        if task is None or task.status != "pending":
            raise WsServerError("task not claimable", JSONRPC_DOMAIN_ERROR)

        if wake_token:
            expires_at = float(params.get("expires_at", 0))
            if self.hub.wake is None or not self.hub.wake.verify_wake_token(
                task_id, self.worker_id, wake_token, expires_at
            ):
                raise WsServerError("invalid wake_token", JSONRPC_DOMAIN_ERROR)

        claimed = await self.hub.router.claim_task_for_worker(
            task_id, self.worker_id, self.claims
        )
        if claimed is None:
            raise WsServerError("claim failed", JSONRPC_DOMAIN_ERROR)
        run_payload = await self.hub.build_run_payload(task_id, claimed)
        return {"claimed": True, "task_id": task_id, "run": run_payload}

    async def _handle_worker_heartbeat(self, params: dict) -> dict:
        """Refresh the worker heartbeat timestamp.

        Only the current active session for a worker may update its heartbeat
        or status. A stale worker that resumes heartbeating on the active
        session transitions back to ``idle`` (or ``draining`` if drain was
        requested before staleness) so it can receive work again without a
        fresh announce.
        """
        self._require_announced()
        if not await self._is_current_session_for_worker():
            logger.debug(
                "heartbeat from superseded session %s for worker %s ignored",
                self.session_id,
                self.worker_id,
            )
            return {}
        worker = await self.hub.registry.get_worker(self.worker_id)
        if worker is not None:
            now = time.time()
            worker.last_heartbeat_at = now
            worker.last_seen_at = now
            if worker.status == "stale":
                worker.status = "draining" if worker.drain_requested else "idle"
            await self.hub.db.upsert_worker(worker)
        return {}

    async def _handle_worker_drain(self, params: dict) -> dict:
        """Set the worker to draining and return its running task ids."""
        self._require_announced()
        if await self._is_current_session_for_worker():
            worker = await self.hub.registry.get_worker(self.worker_id)
            if worker is not None:
                worker.status = "draining"
                worker.drain_requested = True
                await self.hub.db.upsert_worker(worker)
        self.draining = True
        running_ids = await self._running_task_ids_for_worker()
        return {"running_task_ids": running_ids}

    async def _handle_worker_close(self, params: dict) -> dict:
        """Graceful close: mark worker offline and end the session."""
        self._require_announced()
        if await self._is_current_session_for_worker():
            worker = await self.hub.registry.get_worker(self.worker_id)
            if worker is not None:
                worker.status = "offline"
                worker.online_session_id = None
                await self.hub.db.upsert_worker(worker)
        self._close_after_response = True
        return {}

    async def _handle_worker_nack(self, params: dict) -> dict:
        """Worker declines a two-step offer or a pushed task."""
        self._require_announced()
        task_id = params.get("task_id")
        reason = params.get("reason", "worker nack")
        claim_token = params.get("claim_token")
        if not task_id:
            raise WsServerError("task_id is required", JSONRPC_INVALID_PARAMS)

        task = await self.hub.db.get_task(task_id)
        if task is not None and task.status == "pending":
            released = await self.hub.router.release_offer(
                task_id, str(claim_token) if claim_token else None
            )
            if released:
                return {"released": True}

        await self._verify_task_owner(task_id)
        await self.hub.router.on_complete(task_id, status="lost", summary=reason)
        return {"released": True}

    # ------------------------------------------------------------------
    # Helpers
    # ------------------------------------------------------------------

    def _require_announced(self) -> None:
        if not self.announced or self.worker_id is None:
            raise WsServerError("worker.announce required", JSONRPC_INVALID_REQUEST)

    async def _verify_task_owner(self, task_id: str) -> Task:
        task = await self.hub.db.get_task(task_id)
        if task is None:
            raise WsServerError("task not found", JSONRPC_INVALID_PARAMS)
        if task.worker_id != self.worker_id:
            raise WsServerError("task not assigned to this worker", JSONRPC_INVALID_PARAMS)
        return task

    async def _running_task_ids_for_worker(self) -> list[str]:
        cursor = await self.hub.db._conn.execute(
            "SELECT task_id FROM tasks WHERE worker_id = ? AND status = 'running'",
            (self.worker_id,),
        )
        return [row[0] for row in await cursor.fetchall()]

    async def _cancel_monitor_loop(self) -> None:
        """Background task that pushes ``task.cancel`` for cancelling tasks."""
        while not self._closed:
            try:
                await asyncio.sleep(1)
                if self.worker_id is None:
                    continue
                if not await self._is_current_session_for_worker():
                    # This session was superseded; stop monitoring so a stale
                    # connection cannot push cancels to the wrong socket.
                    break
                cursor = await self.hub.db._conn.execute(
                    "SELECT task_id, cancel_reason, claim_expires_at FROM tasks"
                    " WHERE worker_id = ? AND status = 'cancelling'",
                    (self.worker_id,),
                )
                currently_cancelling = set()
                for row in await cursor.fetchall():
                    task_id = row["task_id"]
                    currently_cancelling.add(task_id)
                    if task_id in self._notified_cancelling:
                        continue
                    self._notified_cancelling.add(task_id)
                    await self.send_notification(
                        "task.cancel",
                        {
                            "task_id": task_id,
                            "reason": row["cancel_reason"] or "cancel requested",
                            "hard_deadline_at": row["claim_expires_at"],
                        },
                    )
                # Tasks that left cancelling (e.g. settled terminal) must be
                # removable from the notified set so a future redispatch+cancel
                # can be pushed again in the same worker session.
                self._notified_cancelling.intersection_update(currently_cancelling)
            except Exception:
                # Stop monitoring on any send/DB failure; the main loop will
                # clean up the session.
                break


# ------------------------------------------------------------------
# Factory helpers
# ------------------------------------------------------------------

def serve_ws(
    router: TaskRouter,
    auth: Auth,
    registry: WorkerRegistry,
    db: Database,
    config: HubConfig,
    delivery: DeliveryCoordinator | None = None,
    wake: WakeScheduler | None = None,
    host: str = "127.0.0.1",
    port: int = 0,
    **kwargs: Any,
) -> tuple[WsHubServer, Awaitable]:
    """Factory returning the hub instance and the websockets serve awaitable."""
    hub = WsHubServer(router, auth, registry, db, config, delivery=delivery, wake=wake)
    if delivery is not None:
        delivery.attach_ws_hub(hub)
    return hub, hub.serve(host=host, port=port, **kwargs)
