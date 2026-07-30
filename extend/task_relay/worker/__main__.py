"""CLI entry point: ``python -m extend.task_relay.worker``."""

from __future__ import annotations

import argparse
import asyncio
import logging
import sys
from pathlib import Path
from typing import Sequence

from extend.task_relay.worker.backends.stub_backend import StubBackend
from extend.task_relay.worker.task_worker import TaskWorker, install_signal_handlers

logger = logging.getLogger("task_relay.worker")


def _load_jwt(path: Path) -> str:
    """Read a JWT or bootstrap token from disk, stripping whitespace."""
    try:
        return path.read_text(encoding="utf-8").strip()
    except OSError as exc:
        raise RuntimeError(f"cannot read worker JWT file {path}: {exc}") from exc


def _build_arg_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Task Relay Mode A Worker")
    parser.add_argument(
        "--worker-id",
        required=True,
        help="unique worker identifier (must match JWT sub)",
    )
    parser.add_argument(
        "--relay-url",
        required=True,
        help="Hub WebSocket URL, e.g. ws://127.0.0.1:9000",
    )
    parser.add_argument(
        "--worker-jwt-file",
        required=True,
        type=Path,
        help="path to a file containing the short-lived worker JWT",
    )
    parser.add_argument(
        "--session-modes",
        default="a",
        help="comma-separated session modes (default: a)",
    )
    parser.add_argument(
        "--backend",
        default="stub",
        choices=["stub"],
        help="execution backend to use (default: stub)",
    )
    parser.add_argument(
        "--max-concurrent",
        type=int,
        default=None,
        help="override the JWT's max_concurrent concurrency limit",
    )
    parser.add_argument(
        "--poll-wait-ms",
        type=int,
        default=5_000,
        help="long-poll wait passed to worker.poll (default: 5000)",
    )
    parser.add_argument(
        "--stub-sleep-seconds",
        type=float,
        default=0.1,
        help="stub backend sleep duration (default: 0.1)",
    )
    parser.add_argument(
        "--log-level",
        default="INFO",
        help="logging level (default: INFO)",
    )
    return parser


def _create_backend(args: argparse.Namespace) -> StubBackend:
    if args.backend == "stub":
        from extend.task_relay.worker.backends.stub_backend import StubBackend, StubBackendConfig

        return StubBackend(StubBackendConfig(sleep_seconds=args.stub_sleep_seconds))
    raise ValueError(f"unknown backend: {args.backend}")


async def _async_main(argv: Sequence[str] | None) -> int:
    parser = _build_arg_parser()
    args = parser.parse_args(argv)

    logging.basicConfig(
        level=getattr(logging, args.log_level.upper(), logging.INFO),
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )

    jwt = _load_jwt(args.worker_jwt_file)
    backend = _create_backend(args)

    session_modes = [m.strip().lower() for m in args.session_modes.split(",") if m.strip()]
    if "a" not in session_modes:
        logger.error("Mode A is mandatory for all workers")
        return 1

    worker = TaskWorker(
        worker_id=args.worker_id,
        relay_url=args.relay_url,
        jwt=jwt,
        backend=backend,
        max_concurrent=args.max_concurrent,
        poll_wait_ms=args.poll_wait_ms,
    )
    install_signal_handlers(worker)

    try:
        await worker.run()
    except Exception:
        logger.exception("worker crashed")
        return 1

    logger.info("worker stopped")
    return 0


def main(argv: Sequence[str] | None = None) -> int:
    """Synchronous entry point."""
    try:
        return asyncio.run(_async_main(argv))
    except KeyboardInterrupt:
        return 0


if __name__ == "__main__":
    raise SystemExit(main())
