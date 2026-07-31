"""ACL audit log for Task Relay dispatch (M3 hardening)."""

from __future__ import annotations

import json
import time
from typing import Any

from extend.task_relay.hub.db import Database
from extend.task_relay.hub.json_util import safe_json_loads
from extend.task_relay.hub.models import Task


def _acl_payload(task: Task) -> dict[str, Any] | None:
    allowed = safe_json_loads(task.allowed_worker_ids_json) or []
    denied = safe_json_loads(task.deny_worker_ids_json) or []
    payload: dict[str, Any] = {}
    if task.target_worker:
        payload["target_worker"] = task.target_worker
    if allowed:
        payload["allowed_worker_ids"] = allowed
    if denied:
        payload["deny_worker_ids"] = denied
    return payload or None


async def record_acl_dispatch(
    db: Database,
    task: Task,
    *,
    master_session_id: str,
) -> None:
    """Persist an audit row when dispatch carries worker ACL constraints."""
    payload = _acl_payload(task)
    if payload is None:
        return
    payload["master_session_id"] = master_session_id
    await db._conn.execute(
        "INSERT INTO audit_log (event_at, action, task_id, master_session_id, payload_json)"
        " VALUES (?, ?, ?, ?, ?)",
        (
            time.time(),
            "dispatch_acl",
            task.task_id,
            master_session_id,
            json.dumps(payload),
        ),
    )
    await db._conn.commit()
