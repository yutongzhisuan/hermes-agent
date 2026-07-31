"""Structured output enrichment for relay task results."""

from __future__ import annotations

import json
import re
from typing import Any

_JSON_FENCE_RE = re.compile(r"```(?:json)?\s*(\{[\s\S]*?\})\s*```", re.IGNORECASE)


def _structured_output_spec(params: dict[str, Any] | None) -> Any | None:
    if not params:
        return None
    return params.get("structured_output")


def _extract_json_object(text: str) -> dict[str, Any] | None:
    stripped = text.strip()
    if not stripped:
        return None
    try:
        parsed = json.loads(stripped)
        return parsed if isinstance(parsed, dict) else None
    except json.JSONDecodeError:
        pass

    match = _JSON_FENCE_RE.search(stripped)
    if match:
        try:
            parsed = json.loads(match.group(1))
            return parsed if isinstance(parsed, dict) else None
        except json.JSONDecodeError:
            return None

    start = stripped.find("{")
    end = stripped.rfind("}")
    if start >= 0 and end > start:
        try:
            parsed = json.loads(stripped[start : end + 1])
            return parsed if isinstance(parsed, dict) else None
        except json.JSONDecodeError:
            return None
    return None


def enrich_structured_output(payload: Any, run: Any) -> Any:
    """Attach schema_version and optional structured fields when requested."""
    from extend.task_relay.worker.task_executor import TaskCompletePayload

    spec = _structured_output_spec(run.params)
    if spec is None:
        return payload

    fields = dict(payload.fields or {})
    fields["schema_version"] = 1
    if spec not in (True, "true", 1):
        fields["structured_output_spec"] = spec

    source = payload.result_text or payload.summary or ""
    extracted = _extract_json_object(source)
    if extracted is not None:
        fields["structured"] = extracted

    return TaskCompletePayload(
        status=payload.status,
        summary=payload.summary,
        result_text=payload.result_text,
        fields=fields,
        usage=payload.usage,
        error=payload.error,
    )
