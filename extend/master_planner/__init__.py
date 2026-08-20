"""Master Planner plugin — user-side Master Agent for the INFA inference platform.

Registers seven ``gateway_*`` tools (toolset ``master_planner``) so a planner
profile can dispatch tasks to the platform's AgentRelayService, watch their
event stream, and join the results. All new code lives under
``extend/master_planner/`` — zero core edits, everything goes through the
public PluginContext surface (``ctx.register_tool``).

Hard constraints (see docs/superpowers/specs/2026-08-20-user-side-master-agent-design.md):
  * sync handlers only (async handlers hit a 300s hard-timeout branch);
  * blocking watch polls ``tools.interrupt.is_interrupted()`` every second;
  * every handler refuses to run inside a delegate_task child context;
  * task state is mirrored into a local sqlite ledger — the LLM context is
    NOT a reliable store (compaction replaces old tool results).
"""

from __future__ import annotations

import logging

logger = logging.getLogger(__name__)

__all__ = ["register"]


def check_requirements() -> bool:
    """Stdlib only — always loadable. The API key is enforced per tool call."""
    return True


def register(ctx) -> None:
    """Plugin entry point — called by the XHermes plugin system."""
    try:
        from .tools import register_tools

        register_tools(ctx)
    except Exception:
        logger.warning("master_planner: failed to register tools", exc_info=True)
