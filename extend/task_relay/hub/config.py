"""Hub configuration — timeout/limit/retention defaults from the design spec.

See docs/superpowers/specs/2026-07-31-task-relay-design.md ("Global
Constraints" and timeout layers) for where each default comes from.
"""

import argparse
import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Mapping, Sequence

from extend.task_relay.hub.tls import TlsConfig


def _default_db_path() -> str:
    """Return the default SQLite DB path under the Hermes home directory.

    Uses ``hermes_constants.get_hermes_home()`` when available. Falls back to
    ``~/.hermes/relay/tasks.db`` so the Hub can still start in environments
    where the constants module is unreachable.
    """
    try:
        from hermes_constants import get_hermes_home

        return str(get_hermes_home() / "relay" / "tasks.db")
    except Exception:
        return str(Path.home() / ".hermes" / "relay" / "tasks.db")


@dataclass(frozen=True)
class BootstrapEntry:
    """Long-lived bootstrap credential entry: what a worker is scoped to once
    it exchanges the bootstrap token for a short-lived JWT."""

    worker_id: str
    allowed_toolsets: tuple[str, ...] = ()
    max_concurrent: int = 1


@dataclass(frozen=True)
class HubConfig:
    # No eligible worker ever appeared within this window -> settle as `lost`.
    queue_timeout_seconds: int = 900
    # First progress event must arrive within this window after dispatch.
    first_progress_seconds: int = 120
    # Hard per-task timeout once running.
    timeout_seconds: int = 600
    # Total dispatch attempts per task (1 = no redispatch by default).
    max_attempts: int = 1
    # Cancel pushed but worker has not settled -> Hub settles it itself.
    cancel_grace_seconds: int = 60
    # Workers without a heartbeat/announce for this long are marked stale.
    worker_stale_seconds: int = 300
    # Max opaque resume_blob bytes a checkpoint may carry; oversize checkpoints
    # are rejected so workers fall back to ContextRef.
    resume_blob_max_bytes: int = 1_048_576
    # Bounded per-WatchTask-stream buffer; overflow closes the stream with
    # RESOURCE_EXHAUSTED + SlowConsumer detail.
    watch_stream_buffer_events: int = 1024
    # ListTasks pagination: default page size and hard cap (over-limit clamped).
    list_tasks_default_limit: int = 100
    list_tasks_max_limit: int = 500
    # Events older than this are pruned; cursors older than retention fail
    # with FAILED_PRECONDITION + CursorOutOfRange.
    retention_days: int = 7
    # Auth (M1): HS256 shared secret signing Hub-issued worker/master JWTs.
    # The default is empty so the dataclass stays cheap to construct, but
    # Auth.from_config rejects an empty secret (fail-closed): a deployment
    # MUST set jwt_secret explicitly before any token can be issued.
    jwt_secret: str = ""
    jwt_issuer: str = "hermes-relay-hub"
    jwt_audience: str = "task-relay-hub"
    # Lifetime of issued worker/master JWTs.
    jwt_ttl_seconds: int = 3600
    # Long-lived bootstrap credentials: token -> worker scope. Workers present
    # one to the token endpoint once, then refresh the issued JWT before exp.
    bootstrap_tokens: Mapping[str, BootstrapEntry] = field(default_factory=dict)
    # Reject ContextRef dispatches without a valid HMAC signature (M3).
    require_signed_context_ref: bool = False
    # Encrypt inline / inline_gzip context before persisting tasks.context_json.
    encrypt_inline_context_at_rest: bool = False


def load_bootstrap_tokens(raw: str) -> dict[str, BootstrapEntry]:
    """Parse ``--bootstrap-tokens`` as either inline JSON or a JSON file path.

    Expected object shape::

        {
          "<opaque-token>": {
            "worker_id": "worker-01",
            "allowed_toolsets": ["terminal", "file"],
            "max_concurrent": 2
          }
        }

    ``allowed_toolsets`` defaults to empty and ``max_concurrent`` defaults to 1.
    """
    if not raw:
        return {}
    text = raw.strip()
    if text.startswith(("{", "[")):
        data = json.loads(text)
    else:
        with open(text, "r", encoding="utf-8") as f:
            data = json.load(f)
    if not isinstance(data, dict):
        raise ValueError("bootstrap tokens must be a JSON object mapping token -> entry")
    result: dict[str, BootstrapEntry] = {}
    for token, entry in data.items():
        if not isinstance(entry, dict):
            raise ValueError(f"bootstrap entry for {token!r} must be an object")
        result[token] = BootstrapEntry(
            worker_id=entry["worker_id"],
            allowed_toolsets=tuple(entry.get("allowed_toolsets", [])),
            max_concurrent=entry.get("max_concurrent", 1),
        )
    return result


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    """Parse the Hub process command line."""
    parser = argparse.ArgumentParser(description="Task Relay Hub")
    parser.add_argument(
        "--host",
        default="127.0.0.1",
        help="interface to bind (default: 127.0.0.1)",
    )
    parser.add_argument(
        "--grpc-port",
        type=int,
        default=9090,
        help="gRPC master port (default: 9090)",
    )
    parser.add_argument(
        "--ws-port",
        type=int,
        default=9000,
        help="WebSocket worker port (default: 9000)",
    )
    parser.add_argument(
        "--http-port",
        type=int,
        default=9001,
        help="HTTP worker token port (default: 9001)",
    )
    parser.add_argument(
        "--metrics-port",
        type=int,
        default=0,
        help="Prometheus metrics HTTP port; 0 disables (default: 0)",
    )
    parser.add_argument(
        "--db",
        default=_default_db_path(),
        help="SQLite path or postgres:// URL (default: SQLite under Hermes home)",
    )
    parser.add_argument(
        "--jwt-secret",
        default="",
        help="HS256 shared secret for issuing/verifying Hub JWTs (required)",
    )
    parser.add_argument(
        "--bootstrap-tokens",
        default="",
        help="JSON file path or inline JSON mapping bootstrap token to worker scope",
    )
    parser.add_argument("--tls-cert", default="", help="TLS server certificate (PEM)")
    parser.add_argument("--tls-key", default="", help="TLS server private key (PEM)")
    parser.add_argument("--tls-ca", default="", help="TLS CA bundle for client verification")
    parser.add_argument(
        "--tls-require-client-cert",
        action="store_true",
        help="Require mTLS client certificates (requires --tls-ca)",
    )
    parser.add_argument(
        "--require-signed-context-ref",
        action="store_true",
        help="Reject ContextRef dispatches without a valid HMAC signature (M3)",
    )
    parser.add_argument(
        "--encrypt-inline-context-at-rest",
        action="store_true",
        help="Encrypt inline / inline_gzip context before persisting tasks.context_json (M3)",
    )
    return parser.parse_args(argv)


def hub_config_from_args(args: argparse.Namespace) -> HubConfig:
    """Build :class:`HubConfig` from parsed CLI arguments."""
    return HubConfig(
        jwt_secret=args.jwt_secret,
        bootstrap_tokens=load_bootstrap_tokens(args.bootstrap_tokens),
        require_signed_context_ref=args.require_signed_context_ref,
        encrypt_inline_context_at_rest=args.encrypt_inline_context_at_rest,
    )


def tls_config_from_args(args: argparse.Namespace) -> TlsConfig:
    """Build :class:`TlsConfig` from parsed CLI arguments."""
    return TlsConfig(
        cert_file=args.tls_cert,
        key_file=args.tls_key,
        ca_file=args.tls_ca,
        require_client_cert=args.tls_require_client_cert,
    )
