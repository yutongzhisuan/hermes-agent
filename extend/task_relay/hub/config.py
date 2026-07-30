"""Hub configuration — timeout/limit/retention defaults from the design spec.

See docs/superpowers/specs/2026-07-31-task-relay-design.md ("Global
Constraints" and timeout layers) for where each default comes from.
"""

from dataclasses import dataclass


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
