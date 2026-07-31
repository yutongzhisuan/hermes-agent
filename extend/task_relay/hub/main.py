"""Task Relay Hub process entry point.

Wires the persistence, auth, event bus, registry, router, gRPC master service,
and WebSocket worker service into a single runnable process. A background
ticker evaluates task timeouts once per second.
"""

from __future__ import annotations

import argparse
import asyncio
import logging
import signal
import sys
from pathlib import Path
from typing import Sequence

from extend.task_relay.hub.auth import Auth, AuthError
from extend.task_relay.hub.config import (
    HubConfig,
    hub_config_from_args,
    parse_args,
    tls_config_from_args,
)
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.db_conn import is_postgres_url
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.grpc_server import serve_grpc
from extend.task_relay.hub.bootstrap import serve_ws_with_delivery, wire_orchestration
from extend.task_relay.hub.metrics_server import serve_metrics_http
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.tls import load_server_ssl_context
from extend.task_relay.hub.token_server import serve_token_http
from extend.task_relay.hub.worker_registry import WorkerRegistry

logger = logging.getLogger("task_relay.hub")


async def _timeout_ticker(
    router: TaskRouter,
    shutdown: asyncio.Event,
    interval: float = 1.0,
) -> None:
    """Periodically evaluate queue, first-progress, lease, and cancel-grace deadlines."""
    while not shutdown.is_set():
        try:
            await router.tick_timeouts()
        except Exception:
            logger.exception("timeout ticker failed")
        try:
            await asyncio.wait_for(shutdown.wait(), timeout=interval)
        except asyncio.TimeoutError:
            pass


def _format_sockets(sockets, host: str) -> list[str]:
    """Return human-readable bind addresses for a server socket list."""
    return [f"{host}:{sock.getsockname()[1]}" for sock in sockets]


async def run(
    db_target: str,
    hub_config: HubConfig,
    args: argparse.Namespace,
) -> int:
    """Assemble and run the Hub until a shutdown signal is received."""
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )

    try:
        auth = Auth.from_config(hub_config)
    except AuthError as exc:
        logger.error("auth configuration failed: %s", exc)
        return 1

    tls = tls_config_from_args(args)
    try:
        ssl_ctx = load_server_ssl_context(tls)
    except ValueError as exc:
        logger.error("TLS configuration failed: %s", exc)
        return 1

    db = await open_db(db_target)

    try:
        bus = EventBus(db, hub_config)
        registry = WorkerRegistry(db)
        router = TaskRouter(db, bus, hub_config, registry)
        wire_orchestration(router, db, bus)

        shutdown = asyncio.Event()

        def _on_signal(signum: int) -> None:
            logger.info("received signal %s, shutting down", signal.Signals(signum).name)
            shutdown.set()

        loop = asyncio.get_running_loop()
        for sig in (signal.SIGINT, signal.SIGTERM):
            loop.add_signal_handler(sig, _on_signal, sig)

        ticker_task = asyncio.create_task(_timeout_ticker(router, shutdown))

        grpc_server = await serve_grpc(
            router,
            auth,
            hub_config,
            db,
            bus,
            registry,
            host=args.host,
            port=args.grpc_port,
            ssl=ssl_ctx,
        )
        scheme = "grpcs" if ssl_ctx else "gRPC"
        logger.info("%s listening on %s:%d", scheme, args.host, args.grpc_port)

        ws_scheme = "wss" if ssl_ctx else "ws"
        relay_ws_url = f"{ws_scheme}://{args.host}:{args.ws_port}"
        _, ws_coro, _runtime = serve_ws_with_delivery(
            router,
            auth,
            registry,
            db,
            hub_config,
            relay_ws_url=relay_ws_url,
            host=args.host,
            port=args.ws_port,
            ssl=ssl_ctx,
        )
        ws_server = await ws_coro
        ws_addrs = _format_sockets(ws_server.sockets, args.host)
        logger.info("WebSocket listening on %s", ", ".join(ws_addrs))

        token_runner = await serve_token_http(
            auth,
            host=args.host,
            port=args.http_port,
            ssl_context=ssl_ctx,
        )

        metrics_runner = None
        if args.metrics_port:
            metrics_runner = await serve_metrics_http(
                host=args.host,
                port=args.metrics_port,
                ssl_context=ssl_ctx,
            )

        await shutdown.wait()

        logger.info("closing servers")
        if metrics_runner is not None:
            await metrics_runner.cleanup()
        await token_runner.cleanup()
        grpc_server.close()
        ws_server.close()
        await grpc_server.wait_closed()
        await ws_server.wait_closed()

        ticker_task.cancel()
        try:
            await ticker_task
        except asyncio.CancelledError:
            pass

    finally:
        await db.close()

    logger.info("hub stopped")
    return 0


def _main_sync(argv: Sequence[str] | None = None) -> int:
    """Parse arguments and perform synchronous setup before starting the event loop."""
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )

    args = parse_args(argv)
    db_target = args.db

    if not is_postgres_url(db_target):
        db_path = Path(db_target)
        try:
            db_path.parent.mkdir(parents=True, exist_ok=True)
        except OSError as exc:
            logger.error("cannot create database directory %s: %s", db_path.parent, exc)
            return 1

    try:
        hub_config = hub_config_from_args(args)
    except ValueError as exc:
        logger.error("invalid hub configuration: %s", exc)
        return 1

    try:
        return asyncio.run(run(db_target, hub_config, args))
    except KeyboardInterrupt:
        return 0


def main(argv: Sequence[str] | None = None) -> int:
    """CLI entry point for ``python -m extend.task_relay.hub``."""
    return _main_sync(argv)


if __name__ == "__main__":
    raise SystemExit(main())
