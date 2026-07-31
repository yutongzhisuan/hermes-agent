"""Build the worker-facing ``task.run`` payload from Hub task state."""

from __future__ import annotations

import base64
from typing import Any

from extend.task_relay.hub.context_crypto import decrypt_context_json
from extend.task_relay.hub.db import Database
from extend.task_relay.hub.json_util import safe_json_loads


async def build_run_payload(
    db: Database,
    task_id: str,
    claimed: Any,
    *,
    decrypt_secret: str = "",
    encrypt_at_rest: bool = False,
) -> dict[str, Any]:
    """Assemble the ``task.run`` dict for poll, claim, or Mode C push."""
    task = await db.get_task(task_id)
    if task is None:
        return {}

    latest = None
    if task.resume_from_checkpoint:
        latest = await db.get_latest_checkpoint(task.task_id)

    context = safe_json_loads(task.context_json)
    if encrypt_at_rest and task.context_json:
        context = decrypt_context_json(task.context_json, decrypt_secret)

    run: dict[str, Any] = {
        "task_id": task.task_id,
        "attempt": claimed.attempt,
        "goal": task.goal,
        "params": safe_json_loads(task.params_json),
        "context": context,
        "toolsets": safe_json_loads(task.toolsets_json) or [],
        "timeout_seconds": claimed.timeout_seconds,
        "first_progress_seconds": task.first_progress_seconds,
        "trace_context": safe_json_loads(task.trace_context_json),
        "resume_from_checkpoint": task.resume_from_checkpoint,
        "claim_token": claimed.claim_token,
    }
    if latest is not None and latest.resume_blob:
        run["resume_blob"] = base64.b64encode(latest.resume_blob).decode("ascii")
    return run
