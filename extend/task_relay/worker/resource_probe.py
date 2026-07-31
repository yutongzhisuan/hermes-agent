"""Probe host resources and load for worker.announce / heartbeat frames."""

from __future__ import annotations

import os
import platform
from typing import Any

try:
    import psutil
except ImportError:  # pragma: no cover - psutil is a core Hermes dependency
    psutil = None  # type: ignore[assignment]


def probe_capabilities(toolsets: list[str] | None = None) -> dict[str, Any]:
    """Return static worker capabilities for scheduling hints."""
    caps: dict[str, Any] = {
        "toolsets": list(toolsets or []),
        "os": platform.system().lower(),
        "arch": platform.machine().lower(),
    }
    region = os.environ.get("TASK_RELAY_WORKER_REGION", "").strip()
    if region:
        caps["region"] = region
    return caps


def probe_resources() -> dict[str, Any]:
    """Return self-reported resource inventory (advisory for Hub scoring)."""
    if psutil is None:
        return {"cpu_cores": os.cpu_count() or 1}

    vm = psutil.virtual_memory()
    resources: dict[str, Any] = {
        "cpu_cores": os.cpu_count() or 1,
        "memory_gb": max(1, int(vm.total / (1024**3))),
    }
    try:
        disk = psutil.disk_usage("/")
        resources["disk_gb"] = max(1, int(disk.total / (1024**3)))
    except OSError:
        pass

    profile = os.environ.get("TASK_RELAY_NETWORK_PROFILE", "").strip()
    if profile:
        resources["network_profile"] = profile
    return resources


def probe_load(*, running_tasks: int = 0) -> dict[str, Any]:
    """Return current worker load snapshot."""
    load: dict[str, Any] = {"running_tasks": max(0, running_tasks)}
    if psutil is None:
        return load

    try:
        load["cpu_percent"] = float(psutil.cpu_percent(interval=0.0))
        load["memory_percent"] = float(psutil.virtual_memory().percent)
    except Exception:
        pass
    return load
