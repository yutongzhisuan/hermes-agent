"""Structured output LLM extraction for Task Relay results.

Owned by XHermes (migrated from swarm-network ``worker/structured_output.py``)
because it depends on the XHermes host LLM facade (:mod:`agent.plugin_llm`).
"""

from __future__ import annotations

import asyncio
import logging
from typing import Any

logger = logging.getLogger("task_relay.structured_llm")


async def llm_extract_structured(text: str, spec: Any) -> dict[str, Any] | None:
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
