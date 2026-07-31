"""BatchPolicy completion-mode helpers (M3)."""

from __future__ import annotations

from typing import Any

from extend.task_relay.hub.models import Task

_MODE_ALIASES = {
    "0": "UNSPECIFIED",
    "1": "ALL",
    "2": "ANY",
    "3": "MAJORITY",
    "4": "THRESHOLD",
    "COMPLETION_MODE_UNSPECIFIED": "UNSPECIFIED",
    "COMPLETION_MODE_ALL": "ALL",
    "COMPLETION_MODE_ANY": "ANY",
    "COMPLETION_MODE_MAJORITY": "MAJORITY",
    "COMPLETION_MODE_THRESHOLD": "THRESHOLD",
}


def normalize_completion_mode(policy: dict[str, Any]) -> str:
    """Return an upper-case completion mode name from a policy dict."""
    raw = policy.get("completion_mode")
    if raw is None:
        raw = policy.get("completionMode")
    if raw is None:
        return "ALL"
    if isinstance(raw, int):
        return _MODE_ALIASES.get(str(raw), "ALL")
    text = str(raw).upper()
    return _MODE_ALIASES.get(text, text.replace("COMPLETION_MODE_", ""))


def count_completed(members: list[Task]) -> int:
    return sum(1 for task in members if task.status == "completed")


def completion_threshold_met(members: list[Task], policy: dict[str, Any]) -> bool:
    """True when the batch policy success condition is satisfied."""
    if not members:
        return False
    mode = normalize_completion_mode(policy)
    completed = count_completed(members)
    total = len(members)
    if mode in {"UNSPECIFIED", "ALL", ""}:
        return False
    if mode == "ANY":
        return completed >= 1
    if mode == "MAJORITY":
        return completed > total // 2
    if mode == "THRESHOLD":
        threshold = policy.get("success_threshold", policy.get("successThreshold", 1))
        try:
            needed = int(threshold)
        except (TypeError, ValueError):
            needed = 1
        return completed >= max(1, needed)
    return False
