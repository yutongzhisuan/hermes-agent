"""Minimal WebSocket JSON-RPC client for the Agent Gateway."""

from __future__ import annotations

import asyncio
import json
from collections.abc import Callable
from typing import Any

import websockets
from websockets.asyncio.client import ClientConnection

from .exceptions import HermesRuntimeError


class GatewayRpcError(HermesRuntimeError):
    """JSON-RPC or WebSocket transport failure."""


class GatewayRpcClient:
    """Async JSON-RPC client aligned with ``apps/shared/src/json-rpc-gateway.ts``."""

    def __init__(self, *, request_timeout_s: float = 120.0) -> None:
        self._request_timeout_s = request_timeout_s
        self._ws: ClientConnection | None = None
        self._next_id = 0
        self._pending: dict[int, asyncio.Future[Any]] = {}
        self._event_handlers: list[Callable[[dict[str, Any]], None]] = []
        self._reader_task: asyncio.Task[None] | None = None

    @property
    def connected(self) -> bool:
        return self._ws is not None

    async def connect(self, ws_url: str) -> None:
        if self._ws is not None:
            raise GatewayRpcError("GatewayRpcClient.connect() called while already connected")
        self._ws = await websockets.connect(ws_url)
        self._reader_task = asyncio.create_task(self._read_loop())

    async def request(self, method: str, params: dict[str, Any] | None = None) -> Any:
        ws = self._require_ws()
        req_id = self._next_id
        self._next_id += 1
        loop = asyncio.get_running_loop()
        fut: asyncio.Future[Any] = loop.create_future()
        self._pending[req_id] = fut
        frame = {"jsonrpc": "2.0", "id": req_id, "method": method, "params": params or {}}
        try:
            await ws.send(json.dumps(frame))
            return await asyncio.wait_for(fut, timeout=self._request_timeout_s)
        except asyncio.TimeoutError as exc:
            self._pending.pop(req_id, None)
            raise GatewayRpcError(f"request timed out after {self._request_timeout_s}s") from exc

    def on_event(self, handler: Callable[[dict[str, Any]], None]) -> None:
        self._event_handlers.append(handler)

    async def close(self) -> None:
        if self._reader_task is not None:
            self._reader_task.cancel()
            try:
                await self._reader_task
            except asyncio.CancelledError:
                pass
            self._reader_task = None
        if self._ws is not None:
            await self._ws.close()
            self._ws = None
        for fut in self._pending.values():
            if not fut.done():
                fut.set_exception(GatewayRpcError("connection closed"))
        self._pending.clear()

    async def __aenter__(self) -> GatewayRpcClient:
        return self

    async def __aexit__(self, *exc: object) -> None:
        await self.close()

    def _require_ws(self) -> ClientConnection:
        if self._ws is None:
            raise GatewayRpcError("gateway not connected")
        return self._ws

    async def _read_loop(self) -> None:
        ws = self._require_ws()
        try:
            async for raw in ws:
                self._handle_frame(json.loads(raw))
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            err = GatewayRpcError(f"read loop failed: {exc}")
            for fut in self._pending.values():
                if not fut.done():
                    fut.set_exception(err)
            raise

    def _handle_frame(self, frame: dict[str, Any]) -> None:
        if frame.get("method") == "event":
            params = frame.get("params")
            if isinstance(params, dict):
                for handler in self._event_handlers:
                    handler(params)
            return
        req_id = frame.get("id")
        if req_id is None:
            return
        fut = self._pending.pop(req_id, None)
        if fut is None or fut.done():
            return
        if "error" in frame:
            message = frame.get("error", {}).get("message", "json-rpc error")
            fut.set_exception(GatewayRpcError(message))
        else:
            fut.set_result(frame.get("result"))
