"""Plain dataclasses mirroring the SQLite store columns — no ORM.

Field names and order match the table definitions in the design spec
§Persistence, so rows map 1:1 onto these types.
"""

import json
from dataclasses import dataclass


def _json_list(value: str | None) -> list[str]:
    if not value:
        return []
    try:
        parsed = json.loads(value)
    except json.JSONDecodeError:
        return []
    if isinstance(parsed, list):
        return [str(x) for x in parsed]
    return []


@dataclass
class TaskSpec:
    """Master-facing task specification; not a DB row."""

    task_id: str
    goal: str
    callback_topic: str = "default"
    params_json: str | None = None
    context_json: str | None = None
    toolsets_json: str | None = None
    target_worker: str | None = None
    timeout_seconds: int | None = None
    priority: int = 0
    depends_on_json: str | None = None
    aggregate_key: str | None = None
    min_resources_json: str | None = None
    trace_context_json: str | None = None
    allowed_worker_ids_json: str | None = None
    deny_worker_ids_json: str | None = None
    queue_timeout_seconds: int | None = None
    max_attempts: int | None = None
    first_progress_seconds: int | None = None


@dataclass
class Task:
    task_id: str
    goal: str
    callback_topic: str
    created_at: float
    batch_id: str | None = None
    master_session_id: str | None = None
    params_json: str | None = None
    context_json: str | None = None
    toolsets_json: str | None = None
    worker_id: str | None = None
    status: str = "pending"
    result_json: str | None = None
    summary: str | None = None
    cancel_reason: str | None = None
    fields_json: str | None = None
    usage_json: str | None = None
    error: str | None = None
    allow_redispatch: int = 0
    claim_token: str | None = None
    claim_expires_at: float | None = None
    first_progress_deadline_at: float | None = None
    queue_deadline_at: float | None = None
    attempt: int = 0
    max_attempts: int = 1
    priority: int = 0
    depends_on_json: str | None = None
    aggregate_key: str | None = None
    min_resources_json: str | None = None
    trace_context_json: str | None = None
    allowed_worker_ids_json: str | None = None
    deny_worker_ids_json: str | None = None
    resume_from_checkpoint: str | None = None
    timeout_seconds: int | None = None
    queue_timeout_seconds: int | None = None
    first_progress_seconds: int | None = None
    started_at: float | None = None
    completed_at: float | None = None


@dataclass
class Batch:
    batch_id: str
    callback_topic: str
    batch_spec_hash: str
    created_at: float
    master_session_id: str | None = None
    policy_json: str | None = None
    batch_deadline_at: float | None = None


@dataclass
class Worker:
    worker_id: str
    wake_url: str | None = None
    session_modes: str = "A"
    capabilities_json: str | None = None
    resources_json: str | None = None
    load_json: str | None = None
    max_concurrent: int = 1
    credit_available: int = 0
    running_tasks: int = 0
    last_announce_at: float | None = None
    last_heartbeat_at: float | None = None
    last_seen_at: float | None = None
    status: str = "offline"  # offline | idle | busy | stale | draining
    online_session_id: str | None = None


@dataclass
class TaskEvent:
    event_id: int
    callback_topic: str
    task_id: str | None  # nullable only for AGGREGATE rows
    batch_id: str | None
    kind: str
    payload_json: str | None
    event_at: float


@dataclass
class Checkpoint:
    checkpoint_id: str
    task_id: str
    event_id: int
    checkpoint_at: float
    summary: str | None = None
    fields_json: str | None = None
    resume_blob: bytes | None = None
    lease_until: float | None = None
