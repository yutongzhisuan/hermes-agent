"""Progress and checkpoint policy for sub-agent executor sessions."""

from __future__ import annotations

import os
from dataclasses import dataclass
from typing import Any, Mapping

PROGRESS_MODE_MINIMAL = "minimal"
PROGRESS_MODE_TOOLS = "tools"
PROGRESS_MODE_OFF = "off"
VALID_PROGRESS_MODES = frozenset(
    {PROGRESS_MODE_MINIMAL, PROGRESS_MODE_TOOLS, PROGRESS_MODE_OFF}
)

ENV_PROGRESS_MODE = "ACP_PROGRESS_MODE"
ENV_CHECKPOINT_EVERY_STEPS = "ACP_CHECKPOINT_EVERY_STEPS"

DEFAULT_PROGRESS_MODE = PROGRESS_MODE_MINIMAL
DEFAULT_CHECKPOINT_EVERY_STEPS = 0
DEFAULT_REPORT_PROGRESS_INTERVAL_S = 30.0


@dataclass(frozen=True)
class SubAgentRuntimeOptions:
    """Sidecar/runtime tuning for a sub-agent executor task."""

    progress_mode: str = DEFAULT_PROGRESS_MODE
    checkpoint_every_steps: int = DEFAULT_CHECKPOINT_EVERY_STEPS
    report_progress_interval_s: float = DEFAULT_REPORT_PROGRESS_INTERVAL_S

    def normalized(self) -> "SubAgentRuntimeOptions":
        mode = (self.progress_mode or DEFAULT_PROGRESS_MODE).strip().lower()
        if mode not in VALID_PROGRESS_MODES:
            mode = DEFAULT_PROGRESS_MODE
        every = int(self.checkpoint_every_steps or 0)
        if every < 0:
            every = 0
        interval = float(self.report_progress_interval_s or DEFAULT_REPORT_PROGRESS_INTERVAL_S)
        if interval < 1.0:
            interval = DEFAULT_REPORT_PROGRESS_INTERVAL_S
        return SubAgentRuntimeOptions(
            progress_mode=mode,
            checkpoint_every_steps=every,
            report_progress_interval_s=interval,
        )


def runtime_options_from_params(params: Mapping[str, Any] | None) -> Mapping[str, Any] | None:
    """Return ``sub_agent_options`` from task params, with legacy ``relay_options`` fallback."""
    if not isinstance(params, Mapping):
        return None
    raw = params.get("sub_agent_options")
    if isinstance(raw, Mapping):
        return raw
    legacy = params.get("relay_options")
    if isinstance(legacy, Mapping):
        return legacy
    return None


def parse_sub_agent_options(raw: Mapping[str, Any] | None) -> SubAgentRuntimeOptions:
    """Parse ``sub_agent_options`` from RPC params or executor config."""
    if not raw:
        return SubAgentRuntimeOptions().normalized()
    mode = raw.get("progress_mode", DEFAULT_PROGRESS_MODE)
    every = raw.get("checkpoint_every_steps", DEFAULT_CHECKPOINT_EVERY_STEPS)
    interval = raw.get("report_progress_interval_s", DEFAULT_REPORT_PROGRESS_INTERVAL_S)
    return SubAgentRuntimeOptions(
        progress_mode=str(mode) if mode is not None else DEFAULT_PROGRESS_MODE,
        checkpoint_every_steps=int(every) if every is not None else 0,
        report_progress_interval_s=float(interval)
        if interval is not None
        else DEFAULT_REPORT_PROGRESS_INTERVAL_S,
    ).normalized()


def default_sidecar_options(*, stateless: bool) -> SubAgentRuntimeOptions:
    """Defaults for the sidecar process (stateless uses minimal progress)."""
    env_mode = os.environ.get(ENV_PROGRESS_MODE, "").strip().lower()
    mode = env_mode if env_mode in VALID_PROGRESS_MODES else (
        DEFAULT_PROGRESS_MODE if stateless else PROGRESS_MODE_TOOLS
    )
    every_raw = os.environ.get(ENV_CHECKPOINT_EVERY_STEPS, "").strip()
    every = int(every_raw) if every_raw.isdigit() else DEFAULT_CHECKPOINT_EVERY_STEPS
    return SubAgentRuntimeOptions(
        progress_mode=mode, checkpoint_every_steps=every
    ).normalized()
