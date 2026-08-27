"""Executor-only tools for sub-agent sidecar sessions."""

from __future__ import annotations

import asyncio
import json
import logging
from typing import Any

logger = logging.getLogger("sub_agent.sub_agent_tools")

TOOLSET_NAME = "sub_agent"
_registered = False


def ensure_sub_agent_tools_registered() -> None:
    """Register report_progress into the global tool registry once."""
    global _registered
    if _registered:
        return
    try:
        from tools.registry import registry
    except Exception:
        logger.debug("tool registry unavailable; skipping sub-agent tool registration")
        return

    def _check_sub_agent_context() -> bool:
        from extend.sub_agent.task_context import get_task_context

        return get_task_context() is not None

    registry.register(
        name="report_progress",
        toolset=TOOLSET_NAME,
        schema={
            "type": "function",
            "function": {
                "name": "report_progress",
                "description": (
                    "Record a milestone summary for the master planner watching this "
                    "remote task. Use sparingly for meaningful stage updates — not "
                    "every step. Does not expose tool arguments or reasoning."
                ),
                "parameters": {
                    "type": "object",
                    "properties": {
                        "summary": {
                            "type": "string",
                            "description": "Short human-readable milestone (<=500 chars).",
                        },
                        "fields": {
                            "type": "object",
                            "description": "Optional structured metadata (phase, counts, etc.).",
                        },
                    },
                    "required": ["summary"],
                },
            },
        },
        handler=_report_progress_handler,
        check_fn=_check_sub_agent_context,
        description="Push a voluntary milestone checkpoint to the master planner.",
        emoji="📡",
    )
    _registered = True


def _report_progress_handler(args: dict[str, Any], **_kwargs: object) -> str:
    from extend.sub_agent.task_context import get_task_context

    ctx = get_task_context()
    if ctx is None:
        return json.dumps(
            {"error": "unavailable", "message": "report_progress is executor-only."}
        )
    summary = str(args.get("summary") or "").strip()
    if not summary:
        return json.dumps({"error": "invalid_args", "message": "summary is required."})
    fields = args.get("fields") if isinstance(args.get("fields"), dict) else {}
    accepted = asyncio_run(ctx.emit_checkpoint(summary, fields=fields))
    if not accepted:
        return json.dumps(
            {
                "accepted": False,
                "message": "Rate limited — wait before reporting again.",
            }
        )
    return json.dumps({"accepted": True, "summary": summary[:500]})


def asyncio_run(coro: Any) -> bool:
    """Run emit_checkpoint from sync tool handler (executor thread)."""
    try:
        loop = asyncio.get_running_loop()
    except RuntimeError:
        return asyncio.run(coro)
    future = asyncio.run_coroutine_threadsafe(coro, loop)
    try:
        return bool(future.result(timeout=10))
    except Exception:
        logger.exception("report_progress failed")
        return False
