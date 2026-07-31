"""Prometheus metrics HTTP endpoint for the Task Relay Hub (M3)."""

from __future__ import annotations

import logging
import ssl

from aiohttp import web

from extend.task_relay.hub.metrics import render_prometheus

logger = logging.getLogger("task_relay.hub.metrics_http")

METRICS_PATH = "/metrics"


async def _handle_metrics(_request: web.Request) -> web.Response:
    body = render_prometheus()
    return web.Response(
        status=200,
        content_type="text/plain; version=0.0.4",
        charset="utf-8",
        text=body,
    )


def create_metrics_app() -> web.Application:
    app = web.Application()
    app.router.add_get(METRICS_PATH, _handle_metrics)
    return app


async def serve_metrics_http(
    *,
    host: str = "127.0.0.1",
    port: int = 9092,
    ssl_context: ssl.SSLContext | None = None,
) -> web.AppRunner:
    """Start the Prometheus metrics HTTP server and return its runner."""
    app = create_metrics_app()
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, host, port, ssl_context=ssl_context)
    await site.start()
    scheme = "https" if ssl_context is not None else "http"
    logger.info("metrics HTTP listening on %s://%s:%d%s", scheme, host, port, METRICS_PATH)
    return runner
