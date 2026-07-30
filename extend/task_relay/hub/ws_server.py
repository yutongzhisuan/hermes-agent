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
import json
import time
import uuid
from dataclasses import asdict
from typing import Any, Awaitable, Callable

from websockets.asyncio.server import Response, ServerConnection, serve
from websockets.datastructures import Headers

from extend.task_relay.hub.auth import Auth, AuthError, WorkerClaims
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import Database
from extend.task_relay.hub.models import Checkpoint, Task
from extend.task_relay.hub.task_router import TaskRouter, TaskRouterError
from extend.task_relay.hub.worker_registry import WorkerRegistry

JSONRPC_PARSE_ERROR = -32700
JSONRPC_INVALID_REQUEST = -32600
JSONRPC_METHOD_NOT_FOUND = -32601
JSONRPC_INVALID_PARAMS = -32602
JSONRPC_INTERNAL_ERROR = -32603
JSONRPC_DOMAIN_ERROR = -32000

DEFAULT_HEARTBEAT_INTERVAL_MS = 30_000
DEFAULT_POLL_MAX_WAIT_MS = 60_000
DEFAULT_TWO_STEP_CLAIM_TIMEOUT_S = 30
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
    if data is None:
        return None
    if isinstance(data, bytes):
        try:
            data = data.decode("utf-8")
        except UnicodeDecodeError:
            return None
    if not data:
        return None
    try:
        loaded = json.loads(data)
    except json.JSONDecodeError:
        return None
    return loaded if isinstance(loaded, dict) else None


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
    ):
        self.router = router
        self.auth = auth
        self.registry = registry
        self.db = db
        self.config = config
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
            return Response(
                401,
                "Unauthorized",
                Headers([("WWW-Authenticate", 'Bearer realm="task-relay-hub"')]),
                _json_dumps({"error": f"invalid token: {exc}"}).encode(),
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
        """Best-effort push of a ``task.cancel`` frame to the worker that owns it."""
        task = await self.db.get_task(task_id)
        if task is None or task.worker_id is None:
            return
        for session in list(self._sessions):
            if session.worker_id == task.worker_id:
                await session.send_notification(
                    "task.cancel",
                    {
                        "task_id": task_id,
                        "reason": reason,
                        "hard_deadline_at": hard_deadline_at,
                    },
                )
                break


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
        self.announced = False
        self.draining = False
        self._closed = False
        self._close_after_response = False
        self._cancel_monitor_task: asyncio.Task | None = None

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

    async def _set_worker_offline_if_still_owned(self) -> None:
        """Mark the worker offline when its WS drops without a graceful close."""
        if self.worker_id is None:
            return
        worker = await self.hub.registry.get_worker(self.worker_id)
        if worker is None:
            return
        # Only mutate if this session was the last to announce. A newer session
        # would have bumped last_announce_at, so leave it alone.
        if worker.status != "draining":
            worker.status = "offline"
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

        session_modes = params.get("session_modes", ["a"])
        if isinstance(session_modes, str):
            session_modes = [session_modes]
        modes_lower = [str(m).lower() for m in session_modes]
        if "a" not in modes_lower:
            raise WsServerError("Mode A is mandatory for all workers", JSONRPC_INVALID_PARAMS)

        max_concurrent = int(params.get("max_concurrent", self.claims.max_concurrent))
        if max_concurrent <= 0:
            raise WsServerError("max_concurrent must be > 0", JSONRPC_INVALID_PARAMS)

        toolsets = params.get("toolsets") or params.get("capabilities", {}).get("toolsets") or []
        capabilities = params.get("capabilities", {})
        if "toolsets" not in capabilities and toolsets:
            capabilities["toolsets"] = list(toolsets)

        await self.hub.registry.announce(
            worker_id,
            session_modes="".join(modes_lower),
            toolsets=toolsets,
            capabilities=capabilities or None,
            resources=params.get("resources"),
            load=params.get("load"),
            max_concurrent=max_concurrent,
            wake_url=params.get("wake_url"),
            status="idle",
        )

        self.worker_id = worker_id
        self.announced = True
        self._cancel_monitor_task = asyncio.create_task(self._cancel_monitor_loop())

        return {
            "session_id": str(uuid.uuid4()),
            "heartbeat_interval_ms": DEFAULT_HEARTBEAT_INTERVAL_MS,
            "server_time": int(time.time() * 1000),
        }

    async def _handle_worker_poll(self, params: dict) -> dict:
        """Atomically claim up to ``max_tasks`` and return them in a poll_result."""
        self._require_announced()
        max_wait_ms = int(params.get("max_wait_ms", DEFAULT_POLL_MAX_WAIT_MS))
        max_tasks = int(params.get("max_tasks", 1))
        prefer_atomic_claim = params.get("prefer_atomic_claim", True)
        if not prefer_atomic_claim:
            # Two-step offers are optional in M1; atomic claim is the default.
            raise WsServerError(
                "two-step poll offers are not implemented in M1", JSONRPC_INVALID_PARAMS
            )
        if max_tasks <= 0:
            raise WsServerError("max_tasks must be > 0", JSONRPC_INVALID_PARAMS)

        # Bound the long-poll wait to a sane maximum.
        wait_s = min(max_wait_ms / 1000.0, DEFAULT_POLL_MAX_WAIT_MS / 1000.0)
        claimed = await self._wait_for_claims(wait_s, max_tasks)
        if not claimed:
            return {"offered": False}

        tasks_out = []
        for claimed_task in claimed:
            task = await self.hub.db.get_task(claimed_task.task_id)
            run_payload = await self._build_run_payload(task, claimed_task)
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

    async def _handle_worker_claim(self, params: dict) -> dict:
        """Two-step claim: move an offered task to running and return task.run."""
        self._require_announced()
        task_id = params.get("task_id")
        claim_token = params.get("claim_token")
        if not task_id or not claim_token:
            raise WsServerError("task_id and claim_token required", JSONRPC_INVALID_PARAMS)

        async with self.hub.router._lock:
            task = await self.hub.db.get_task(task_id)
            if task is None:
                raise WsServerError("task not found", JSONRPC_INVALID_PARAMS)
            if task.status != "pending":
                raise WsServerError("task is no longer available", JSONRPC_INVALID_PARAMS)
            if task.claim_token != claim_token:
                raise WsServerError("claim_token mismatch", JSONRPC_INVALID_PARAMS)
            if task.worker_id is not None and task.worker_id != self.worker_id:
                raise WsServerError("task reserved for another worker", JSONRPC_INVALID_PARAMS)
            claimed = await self.hub.router._claim_task(task, self.worker_id)
            if claimed is None:
                raise WsServerError("task was claimed by another session", JSONRPC_INVALID_PARAMS)

        return {
            "claimed": True,
            "task_id": claimed.task_id,
            "attempt": claimed.attempt,
            "claim_token": claimed.claim_token,
            "claim_expires_at": task.claim_expires_at,
            "run": await self._build_run_payload(task, claimed),
        }

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

    async def _handle_cancel_ack(self, params: dict) -> dict:
        """Worker acknowledges a pushed ``task.cancel``."""
        self._require_announced()
        task_id = params.get("task_id")
        if not task_id:
            raise WsServerError("task_id is required", JSONRPC_INVALID_PARAMS)
        await self._verify_task_owner(task_id)
        return {"acknowledged": True}

    async def _handle_worker_heartbeat(self, params: dict) -> dict:
        """Refresh the worker heartbeat timestamp."""
        self._require_announced()
        worker = await self.hub.registry.get_worker(self.worker_id)
        if worker is not None:
            worker.last_heartbeat_at = time.time()
            await self.hub.db.upsert_worker(worker)
        return {}

    async def _handle_worker_drain(self, params: dict) -> dict:
        """Set the worker to draining and return its running task ids."""
        self._require_announced()
        worker = await self.hub.registry.get_worker(self.worker_id)
        if worker is not None:
            worker.status = "draining"
            await self.hub.db.upsert_worker(worker)
        self.draining = True
        running_ids = await self._running_task_ids_for_worker()
        return {"running_task_ids": running_ids}

    async def _handle_worker_close(self, params: dict) -> dict:
        """Graceful close: mark worker offline and end the session."""
        self._require_announced()
        worker = await self.hub.registry.get_worker(self.worker_id)
        if worker is not None:
            worker.status = "offline"
            await self.hub.db.upsert_worker(worker)
        self._close_after_response = True
        return {}

    async def _handle_worker_nack(self, params: dict) -> dict:
        """Worker declines a two-step offer or a pushed task.

        M1 uses atomic claim-on-poll, so a nack after receiving ``task.run`` is
        treated as a graceful loss. The task is marked ``lost`` so the Master
        can redispatch if configured.
        """
        self._require_announced()
        task_id = params.get("task_id")
        reason = params.get("reason", "worker nack")
        if not task_id:
            raise WsServerError("task_id is required", JSONRPC_INVALID_PARAMS)
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
                cursor = await self.hub.db._conn.execute(
                    "SELECT task_id, summary, claim_expires_at FROM tasks"
                    " WHERE worker_id = ? AND status = 'cancelling'",
                    (self.worker_id,),
                )
                for row in await cursor.fetchall():
                    await self.send_notification(
                        "task.cancel",
                        {
                            "task_id": row["task_id"],
                            "reason": row["summary"] or "cancel requested",
                            "hard_deadline_at": row["claim_expires_at"],
                        },
                    )
            except Exception:
                # Stop monitoring on any send/DB failure; the main loop will
                # clean up the session.
                break

    async def _build_run_payload(self, task: Task | None, claimed: Any) -> dict:
        """Build the ``task.run`` payload delivered inside a poll result."""
        if task is None:
            return {}
        latest = None
        if task.resume_from_checkpoint:
            latest = await self.hub.db.get_latest_checkpoint(task.task_id)
        run: dict[str, Any] = {
            "task_id": task.task_id,
            "attempt": claimed.attempt,
            "goal": task.goal,
            "params": _safe_json_loads(task.params_json),
            "context": _safe_json_loads(task.context_json),
            "toolsets": _safe_json_loads(task.toolsets_json) or [],
            "timeout_seconds": claimed.timeout_seconds,
            "first_progress_seconds": task.first_progress_seconds,
            "trace_context": _safe_json_loads(task.trace_context_json),
            "resume_from_checkpoint": task.resume_from_checkpoint,
        }
        if latest is not None:
            run["resume_blob"] = latest.resume_blob.decode("utf-8") if latest.resume_blob else None
        return run


# ------------------------------------------------------------------
# Factory helpers
# ------------------------------------------------------------------

def serve_ws(
    router: TaskRouter,
    auth: Auth,
    registry: WorkerRegistry,
    db: Database,
    config: HubConfig,
    host: str = "127.0.0.1",
    port: int = 0,
    **kwargs: Any,
) -> Awaitable:
    """Factory for a ``websockets.serve`` awaitable running the Mode A server.

    Example::

        server = await serve_ws(router, auth, registry, db, config, port=9000)
    """
    hub = WsHubServer(router, auth, registry, db, config)
    return hub.serve(host=host, port=port, **kwargs)
