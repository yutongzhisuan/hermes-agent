"""CLI entry point: ``python -m extend.task_relay.worker``."""

from __future__ import annotations

import argparse
import asyncio
import logging
import sys
from pathlib import Path
from typing import Sequence

from extend.task_relay.worker.jwt_manager import derive_token_url, ensure_worker_jwt
from extend.task_relay.worker.task_worker import TaskWorker, install_signal_handlers
from extend.task_relay.worker.tls_client import build_client_ssl_context, client_tls_from_args

logger = logging.getLogger("task_relay.worker")


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
        help="path to cache the short-lived worker JWT",
    )
    parser.add_argument(
        "--worker-bootstrap-file",
        type=Path,
        default=None,
        help="optional path to a long-lived bootstrap credential",
    )
    parser.add_argument(
        "--token-url",
        default=None,
        help="Hub worker token HTTP URL (default: derived from --relay-url)",
    )
    parser.add_argument(
        "--hub-http-port",
        type=int,
        default=None,
        help="Hub HTTP port when deriving --token-url from --relay-url",
    )
    parser.add_argument(
        "--session-modes",
        default="a",
        help="comma-separated session modes (default: a)",
    )
    parser.add_argument(
        "--backend",
        default="stub",
        choices=["stub", "acp"],
        help="execution backend to use (default: stub)",
    )
    parser.add_argument(
        "--max-concurrent",
        type=int,
        default=None,
        help="capped by the JWT's limit",
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
        "--acp-progress-interval-seconds",
        type=float,
        default=5.0,
        help="minimum seconds between ACP progress frames (default: 5.0)",
    )
    parser.add_argument(
        "--log-level",
        default="INFO",
        help="logging level (default: INFO)",
    )
    parser.add_argument("--tls-ca", default="", help="CA bundle to verify Hub TLS (PEM)")
    parser.add_argument("--tls-cert", default="", help="client certificate for mTLS (PEM)")
    parser.add_argument("--tls-key", default="", help="client private key for mTLS (PEM)")
    parser.add_argument(
        "--tls-skip-hostname-verify",
        action="store_true",
        help="skip TLS hostname verification (dev/test only)",
    )
    return parser


def _create_backend(args: argparse.Namespace):
    if args.backend == "stub":
        from extend.task_relay.worker.backends.stub_backend import StubBackend, StubBackendConfig

        return StubBackend(StubBackendConfig(sleep_seconds=args.stub_sleep_seconds))
    if args.backend == "acp":
        from extend.task_relay.worker.backends.acp_backend import AcpTaskBackend

        return AcpTaskBackend(
            progress_interval_seconds=args.acp_progress_interval_seconds
        )
    raise ValueError(f"unknown backend: {args.backend}")


async def _async_main(argv: Sequence[str] | None) -> int:
    parser = _build_arg_parser()
    args = parser.parse_args(argv)

    logging.basicConfig(
        level=getattr(logging, args.log_level.upper(), logging.INFO),
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )

    try:
        ssl_context = build_client_ssl_context(client_tls_from_args(args))
    except ValueError as exc:
        logger.error("TLS configuration failed: %s", exc)
        return 1

    jwt = await ensure_worker_jwt(
        worker_id=args.worker_id,
        jwt_file=args.worker_jwt_file,
        token_url=args.token_url
        or derive_token_url(args.relay_url, http_port=args.hub_http_port),
        bootstrap_file=args.worker_bootstrap_file,
        ssl_context=ssl_context,
    )
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
        session_modes=session_modes,
        ssl_context=ssl_context,
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
