"""Master Planner plugin — user-side Master Agent for the INFA inference platform.

Registers eight ``gateway_*`` tools (toolset ``master_planner``) so a planner
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


PROMPT_BLOCK_START = "<!-- master-planner:policy BEGIN -->"
PROMPT_BLOCK_END = "<!-- master-planner:policy END -->"


def _sync_planner_prompt() -> None:
    """Idempotently mirror PLANNER_SYSTEM_PROMPT into $XHERMES_HOME/SOUL.md.

    The planner's decomposition/parallel-dispatch policy must be part of the
    agent's system prompt by default — the user never asks for parallelism.
    SOUL.md is auto-injected into the system prompt by the agent runtime, so
    the plugin keeps a clearly-marked block in sync at registration time
    (before the system prompt is built). Content is replaced when the prompt
    changes; anything the user added around the block is left untouched.
    """
    import re

    from hermes_constants import get_hermes_home

    try:
        from .planner_prompt import PLANNER_SYSTEM_PROMPT
    except Exception:
        logger.warning("master_planner: planner prompt unavailable", exc_info=True)
        return

    block = f"{PROMPT_BLOCK_START}\n{PLANNER_SYSTEM_PROMPT}\n{PROMPT_BLOCK_END}\n"
    soul = get_hermes_home() / "SOUL.md"
    try:
        text = soul.read_text(encoding="utf-8") if soul.exists() else ""
        pattern = re.escape(PROMPT_BLOCK_START) + r".*?" + re.escape(PROMPT_BLOCK_END) + r"\n?"
        kept = re.sub(pattern, "", text, flags=re.S)
        new = kept + block
        if new != text:
            soul.parent.mkdir(parents=True, exist_ok=True)
            soul.write_text(new, encoding="utf-8")
    except Exception:
        logger.warning("master_planner: failed to sync planner policy into SOUL.md", exc_info=True)


def register(ctx) -> None:
    """Plugin entry point — called by the XHermes plugin system."""
    _sync_planner_prompt()
    try:
        from .tools import register_tools

        register_tools(ctx)
    except Exception:
        logger.warning("master_planner: failed to register tools", exc_info=True)
