"""Mode B wake HTTP endpoint for the Task Relay worker (M2)."""

from __future__ import annotations

import asyncio
import logging
from typing import Any, Awaitable, Callable

logger = logging.getLogger("task_relay.worker.wake")


class WakeHttpServer:
    """Minimal aiohttp server that accepts Hub wake POSTs and triggers claim."""

    def __init__(
        self,
        on_wake: Callable[[dict[str, Any]], Awaitable[None]],
        *,
        host: str = "127.0.0.1",
        port: int = 0,
    ):
        self._on_wake = on_wake
        self._host = host
        self._port = port
        self._runner: Any | None = None
        self._site: Any | None = None

    @property
    def wake_url(self) -> str | None:
        if self._site is None:
            return None
        sock = self._site._server.sockets[0]
        host, port = sock.getsockname()[:2]
        return f"http://{host}:{port}/wake"

    async def start(self) -> str:
        from aiohttp import web

        app = web.Application()
        app.router.add_post("/wake", self._handle_wake)
        self._runner = web.AppRunner(app)
        await self._runner.setup()
        self._site = web.TCPSite(self._runner, self._host, self._port)
        await self._site.start()
        url = self.wake_url
        if url is None:
            raise RuntimeError("wake server failed to bind")
        logger.info("wake HTTP listening on %s", url)
        return url

    async def stop(self) -> None:
        if self._runner is not None:
            await self._runner.cleanup()
            self._runner = None
            self._site = None

    async def _handle_wake(self, request: Any) -> Any:
        from aiohttp import web

        try:
            body = await request.json()
        except Exception:
            return web.json_response({"error": "invalid json"}, status=400)
        if not isinstance(body, dict):
            return web.json_response({"error": "invalid body"}, status=400)
        try:
            asyncio.create_task(self._on_wake(body))
        except Exception:
            logger.exception("wake handler failed")
            return web.json_response({"error": "wake failed"}, status=500)
        return web.json_response({"accepted": True}, status=202)
