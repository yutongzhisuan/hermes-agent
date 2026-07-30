"""WebSocket JSON-RPC client for the Mode A worker.

Handles the WS upgrade with a Bearer token, request/response correlation,
and incoming server notifications such as ``task.cancel``.
"""

from __future__ import annotations

import asyncio
import json
import logging
from typing import Any, Awaitable, Callable

import websockets
from websockets.exceptions import ConnectionClosed

logger = logging.getLogger("task_relay.worker.ws")


class WsClientError(Exception):
    """JSON-RPC error returned by the Hub."""

    def __init__(self, error_payload: dict[str, Any]):
        self.code = error_payload.get("code")
        self.message = error_payload.get("message", "")
        super().__init__(f"JSON-RPC error {self.code}: {self.message}")


class TaskWorkerWs:
    """Async JSON-RPC client for a single worker WebSocket session."""

    def __init__(
        self,
        relay_url: str,
        jwt: str,
        notification_handlers: dict[str, Callable[[dict[str, Any]], Awaitable[None]]] | None = None,
    ):
        self.relay_url = relay_url
        self.jwt = jwt
        self._ws: websockets.WebSocketClientProtocol | None = None
        self._pending: dict[int, asyncio.Future[dict[str, Any]]] = {}
        self._req_id = 0
        self._receiver_task: asyncio.Task | None = None
        self._closed = False
        self._notification_handlers = dict(notification_handlers or {})

    async def connect(self) -> None:
        """Open the WebSocket and start the receive loop."""
        logger.info("connecting to %s", self.relay_url)
        self._ws = await websockets.connect(
            self.relay_url,
            additional_headers={"Authorization": f"Bearer {self.jwt}"},
        )
        self._receiver_task = asyncio.create_task(self._receive_loop())

    async def close(self) -> None:
        """Close the WebSocket cleanly and cancel the receive loop."""
        self._closed = True
        if self._receiver_task is not None and not self._receiver_task.done():
            self._receiver_task.cancel()
            try:
                await self._receiver_task
            except asyncio.CancelledError:
                pass
        if self._ws is not None:
            await self._ws.close()
        # Wake any stranded waiters.
        for fut in list(self._pending.values()):
            if not fut.done():
                fut.cancel()

    def on_notification(
        self,
        method: str,
        handler: Callable[[dict[str, Any]], Awaitable[None]],
    ) -> None:
        """Register an async handler for server notifications."""
        self._notification_handlers[method] = handler

    async def request(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        """Send a JSON-RPC request and await its correlated response."""
        if self._ws is None:
            raise RuntimeError("websocket not connected")
        if self._closed:
            raise RuntimeError("websocket is closed")

        self._req_id += 1
        msg_id = self._req_id
        fut: asyncio.Future[dict[str, Any]] = asyncio.get_running_loop().create_future()
        self._pending[msg_id] = fut

        payload = {
            "jsonrpc": "2.0",
            "id": msg_id,
            "method": method,
            "params": params,
        }
        try:
            await self._ws.send(json.dumps(payload, separators=(",", ":")))
        except Exception:
            self._pending.pop(msg_id, None)
            raise

        try:
            response = await fut
        except asyncio.CancelledError:
            self._pending.pop(msg_id, None)
            raise

        if "error" in response:
            raise WsClientError(response["error"])
        return response.get("result", {})

    async def _receive_loop(self) -> None:
        """Read frames, route responses to waiters and dispatch notifications."""
        assert self._ws is not None
        try:
            async for raw in self._ws:
                try:
                    payload = json.loads(raw)
                except json.JSONDecodeError as exc:
                    logger.warning("non-JSON frame: %s", exc)
                    continue

                if not isinstance(payload, dict):
                    logger.warning("unexpected frame type: %s", type(payload))
                    continue

                msg_id = payload.get("id")
                if msg_id is not None:
                    fut = self._pending.pop(msg_id, None)
                    if fut is not None and not fut.done():
                        fut.set_result(payload)
                    continue

                method = payload.get("method")
                params = payload.get("params") or {}
                handler = self._notification_handlers.get(method)
                if handler is not None:
                    asyncio.create_task(handler(params))
                else:
                    logger.debug("unhandled notification: %s", method)
        except ConnectionClosed:
            logger.info("websocket connection closed")
        except asyncio.CancelledError:
            logger.debug("receive loop cancelled")
            raise
        except Exception:
            logger.exception("receive loop failed")
        finally:
            # Fail any remaining waiters so request() does not hang.
            for fut in list(self._pending.values()):
                if not fut.done():
                    fut.set_exception(RuntimeError("websocket receive loop ended"))
            self._pending.clear()
