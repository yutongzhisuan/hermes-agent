"""Resource requirement matching and worker load scoring (M3)."""

from __future__ import annotations

from typing import Any

from extend.task_relay.hub.json_util import safe_json_loads
from extend.task_relay.hub.models import Task, Worker


def parse_min_resources(min_resources_json: str | None) -> dict[str, Any] | None:
    """Return parsed min_resources dict or None when unset."""
    if not min_resources_json:
        return None
    parsed = safe_json_loads(min_resources_json)
    return parsed if isinstance(parsed, dict) else None


def worker_meets_resources(worker: Worker, requirements: dict[str, Any]) -> bool:
    """Return True when worker.resources satisfies hard min_resources gates."""
    resources = safe_json_loads(worker.resources_json) or {}
    min_cpu = int(requirements.get("min_cpu_cores") or 0)
    if min_cpu and int(resources.get("cpu_cores") or resources.get("cpu") or 0) < min_cpu:
        return False

    min_memory = int(requirements.get("min_memory_gb") or 0)
    if min_memory and int(resources.get("memory_gb") or resources.get("memory") or 0) < min_memory:
        return False

    if requirements.get("requires_gpu"):
        if int(resources.get("gpu_count") or resources.get("gpu") or 0) < 1:
            return False

    required_profiles = requirements.get("required_network_profiles") or []
    if required_profiles:
        profiles = set(resources.get("network_profiles") or [])
        profile = resources.get("network_profile")
        if profile:
            profiles.add(str(profile))
        if not set(required_profiles).issubset(profiles):
            return False
    return True


def task_has_satisfiable_worker(task: Task, workers: list[Worker]) -> bool:
    """Return True if any worker can satisfy task min_resources (or none set)."""
    requirements = parse_min_resources(task.min_resources_json)
    if not requirements:
        return True
    return any(worker_meets_resources(worker, requirements) for worker in workers)


def worker_load_score(worker: Worker) -> float:
    """Lower score means less loaded (preferred for scheduling)."""
    load = safe_json_loads(worker.load_json) or {}
    running = float(load.get("running_tasks") or worker.running_tasks or 0)
    max_concurrent = max(1, int(worker.max_concurrent or 1))
    utilization = running / max_concurrent
    cpu = float(load.get("cpu_percent") or load.get("cpu") or 0.0)
    memory = float(load.get("memory_percent") or load.get("memory") or 0.0)
    return utilization * 1000.0 + cpu + memory


def sort_workers_by_load(workers: list[Worker]) -> list[Worker]:
    """Return workers ordered by ascending load (least loaded first)."""
    return sorted(workers, key=worker_load_score)
