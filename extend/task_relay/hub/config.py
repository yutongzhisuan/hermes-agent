"""Hub configuration — timeout/limit/retention defaults from the design spec.

See docs/superpowers/specs/2026-07-31-task-relay-design.md ("Global
Constraints" and timeout layers) for where each default comes from.
"""

from dataclasses import dataclass, field
from typing import Mapping


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
