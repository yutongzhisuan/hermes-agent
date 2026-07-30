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
from extend.task_relay.hub.config import HubConfig, load_bootstrap_tokens, parse_args
from extend.task_relay.hub.db import Database, open_db
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.grpc_server import serve_grpc
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.hub.ws_server import serve_ws

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
    db_path: Path,
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

    db: Database = await open_db(str(db_path))

    try:
        bus = EventBus(db, hub_config)
        registry = WorkerRegistry(db)
        router = TaskRouter(db, bus, hub_config, registry)

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
            host=args.host,
            port=args.grpc_port,
        )
        logger.info("gRPC listening on %s:%d", args.host, args.grpc_port)

        ws_server = await serve_ws(
            router,
            auth,
            registry,
            db,
            hub_config,
            host=args.host,
            port=args.ws_port,
        )
        ws_addrs = _format_sockets(ws_server.sockets, args.host)
        logger.info("WebSocket listening on %s", ", ".join(ws_addrs))

        await shutdown.wait()

        logger.info("closing servers")
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

    db_path = Path(args.db)
    try:
        db_path.parent.mkdir(parents=True, exist_ok=True)
    except OSError as exc:
        logger.error("cannot create database directory %s: %s", db_path.parent, exc)
        return 1

    try:
        bootstrap_tokens = load_bootstrap_tokens(args.bootstrap_tokens)
    except ValueError as exc:
        logger.error("invalid bootstrap tokens: %s", exc)
        return 1

    hub_config = HubConfig(
        jwt_secret=args.jwt_secret,
        bootstrap_tokens=bootstrap_tokens,
    )

    try:
        return asyncio.run(run(db_path, hub_config, args))
    except KeyboardInterrupt:
        return 0


def main(argv: Sequence[str] | None = None) -> int:
    """CLI entry point for ``python -m extend.task_relay.hub``."""
    return _main_sync(argv)


if __name__ == "__main__":
    raise SystemExit(main())
