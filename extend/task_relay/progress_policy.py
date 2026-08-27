"""Progress and checkpoint policy for Task Relay executor sessions."""

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
class RelayRuntimeOptions:
    """Sidecar/runtime tuning for a relay executor task."""

    progress_mode: str = DEFAULT_PROGRESS_MODE
    checkpoint_every_steps: int = DEFAULT_CHECKPOINT_EVERY_STEPS
    report_progress_interval_s: float = DEFAULT_REPORT_PROGRESS_INTERVAL_S

    def normalized(self) -> "RelayRuntimeOptions":
        mode = (self.progress_mode or DEFAULT_PROGRESS_MODE).strip().lower()
        if mode not in VALID_PROGRESS_MODES:
            mode = DEFAULT_PROGRESS_MODE
        every = int(self.checkpoint_every_steps or 0)
        if every < 0:
            every = 0
        interval = float(self.report_progress_interval_s or DEFAULT_REPORT_PROGRESS_INTERVAL_S)
        if interval < 1.0:
            interval = DEFAULT_REPORT_PROGRESS_INTERVAL_S
        return RelayRuntimeOptions(
            progress_mode=mode,
            checkpoint_every_steps=every,
            report_progress_interval_s=interval,
        )


def parse_relay_options(raw: Mapping[str, Any] | None) -> RelayRuntimeOptions:
    """Parse ``relay_options`` from RPC params or executor config."""
    if not raw:
        return RelayRuntimeOptions().normalized()
    mode = raw.get("progress_mode", DEFAULT_PROGRESS_MODE)
    every = raw.get("checkpoint_every_steps", DEFAULT_CHECKPOINT_EVERY_STEPS)
    interval = raw.get("report_progress_interval_s", DEFAULT_REPORT_PROGRESS_INTERVAL_S)
    return RelayRuntimeOptions(
        progress_mode=str(mode) if mode is not None else DEFAULT_PROGRESS_MODE,
        checkpoint_every_steps=int(every) if every is not None else 0,
        report_progress_interval_s=float(interval)
        if interval is not None
        else DEFAULT_REPORT_PROGRESS_INTERVAL_S,
    ).normalized()


def default_sidecar_options(*, stateless: bool) -> RelayRuntimeOptions:
    """Defaults for the sidecar process (stateless uses minimal progress)."""
    env_mode = os.environ.get(ENV_PROGRESS_MODE, "").strip().lower()
    mode = env_mode if env_mode in VALID_PROGRESS_MODES else (
        DEFAULT_PROGRESS_MODE if stateless else PROGRESS_MODE_TOOLS
    )
    every_raw = os.environ.get(ENV_CHECKPOINT_EVERY_STEPS, "").strip()
    every = int(every_raw) if every_raw.isdigit() else DEFAULT_CHECKPOINT_EVERY_STEPS
    return RelayRuntimeOptions(progress_mode=mode, checkpoint_every_steps=every).normalized()
