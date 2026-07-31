"""Structured output enrichment for relay task results."""

from __future__ import annotations

import asyncio
import json
import logging
import re
from typing import Any

logger = logging.getLogger("task_relay.worker.structured_output")

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


async def _llm_extract_structured(text: str, spec: Any) -> dict[str, Any] | None:
    """Second-pass structured extraction via the host LLM facade."""
    if not text.strip():
        return None
    try:
        from agent.plugin_llm import PluginLlm, PluginLlmTextInput
    except ImportError:
        logger.debug("plugin_llm unavailable; skipping structured output LLM pass")
        return None

    schema = spec if isinstance(spec, dict) else None
    client = PluginLlm(plugin_id="task_relay")

    def _call():
        return client.complete_structured(
            instructions=(
                "Extract a JSON object from the assistant summary. "
                "Return only valid JSON that matches the requested schema."
            ),
            input=[PluginLlmTextInput(text=text[:8000])],
            json_schema=schema,
            json_mode=schema is None,
            purpose="task_relay_structured_output",
            max_tokens=2048,
        )

    try:
        result = await asyncio.to_thread(_call)
    except Exception:
        logger.warning("structured output LLM pass failed", exc_info=True)
        return None

    parsed = result.parsed
    return parsed if isinstance(parsed, dict) else None


async def enrich_structured_output(payload: Any, run: Any) -> Any:
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
    if extracted is None:
        extracted = await _llm_extract_structured(source, spec)
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
