"""Parse Hub ``task.run`` wire payloads into typed worker models."""

from __future__ import annotations

import base64
from typing import Any

from extend.task_relay.worker.task_executor import TaskRunPayload


def run_payload_from_dict(run: dict[str, Any]) -> TaskRunPayload:
    """Convert the Hub's ``task.run`` dict into a :class:`TaskRunPayload`."""
    return TaskRunPayload(
        task_id=run.get("task_id", ""),
        attempt=int(run.get("attempt", 1)),
        goal=run.get("goal", ""),
        params=run.get("params"),
        context=run.get("context"),
        toolsets=list(run.get("toolsets") or []),
        timeout_seconds=int(run.get("timeout_seconds", 600)),
        first_progress_seconds=run.get("first_progress_seconds"),
        trace_context=run.get("trace_context"),
        resume_from_checkpoint=run.get("resume_from_checkpoint"),
        resume_blob=decode_resume_blob(run.get("resume_blob")),
        claim_token=run.get("claim_token"),
    )


def decode_resume_blob(blob: Any) -> str | bytes | None:
    """Decode a base64 ``resume_blob`` string back to bytes."""
    if not isinstance(blob, str):
        return blob
    try:
        return base64.b64decode(blob, validate=True)
    except Exception:
        return blob
