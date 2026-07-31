"""Hub Prometheus-style metrics (M3)."""

from __future__ import annotations

from collections import defaultdict
from threading import Lock

_lock = Lock()
_counters: dict[tuple[str, tuple[tuple[str, str], ...]], float] = defaultdict(float)
_gauges: dict[tuple[str, tuple[tuple[str, str], ...]], float] = {}
_hist_sum: dict[tuple[str, tuple[tuple[str, str], ...]], float] = defaultdict(float)
_hist_count: dict[tuple[str, tuple[tuple[str, str], ...]], float] = defaultdict(float)


def inc(name: str, value: float = 1.0, **labels: str) -> None:
    key = (name, tuple(sorted(labels.items())))
    with _lock:
        _counters[key] += value


def set_gauge(name: str, value: float, **labels: str) -> None:
    key = (name, tuple(sorted(labels.items())))
    with _lock:
        _gauges[key] = value


def observe(name: str, value: float, **labels: str) -> None:
    key = (name, tuple(sorted(labels.items())))
    with _lock:
        _hist_sum[key] += value
        _hist_count[key] += 1


def render_prometheus() -> str:
    """Render collected metrics in Prometheus text exposition format."""
    lines: list[str] = []
    typed: set[str] = set()
    with _lock:
        for (name, label_items), value in sorted(_counters.items()):
            if name not in typed:
                lines.append(f"# TYPE {name} counter")
                typed.add(name)
            lines.append(f"{_format_key(name, label_items)} {value}")

        for (name, label_items), value in sorted(_gauges.items()):
            if name not in typed:
                lines.append(f"# TYPE {name} gauge")
                typed.add(name)
            lines.append(f"{_format_key(name, label_items)} {value}")

        for (name, label_items), total in sorted(_hist_sum.items()):
            if name not in typed:
                lines.append(f"# TYPE {name} summary")
                typed.add(name)
            base = _format_key(name, label_items)
            lines.append(f"{base}_sum {total}")
            lines.append(f"{base}_count {_hist_count[(name, label_items)]}")
    return "\n".join(lines) + ("\n" if lines else "")


def snapshot() -> dict[str, float]:
    with _lock:
        out: dict[str, float] = {}
        for (name, label_items), value in _counters.items():
            out[_format_key(name, label_items)] = value
        for (name, label_items), value in _gauges.items():
            out[_format_key(name, label_items)] = value
        for (name, label_items), total in _hist_sum.items():
            base = _format_key(name, label_items)
            out[f"{base}_sum"] = total
            out[f"{base}_count"] = _hist_count[(name, label_items)]
        return dict(out)


def reset() -> None:
    with _lock:
        _counters.clear()
        _gauges.clear()
        _hist_sum.clear()
        _hist_count.clear()


def _format_key(name: str, label_items: tuple[tuple[str, str], ...]) -> str:
    if label_items:
        suffix = ",".join(f'{k}="{v}"' for k, v in label_items)
        return f"{name}{{{suffix}}}"
    return name


async def refresh_worker_sessions_gauge(db) -> None:
    """Update per-worker session gauges from the workers table."""
    workers = await db.list_workers(only_schedulable=True)
    for worker in workers:
        set_gauge(
            "relay_worker_sessions_active",
            1.0 if worker.online_session_id else 0.0,
            worker_id=worker.worker_id,
            session_modes=worker.session_modes.lower(),
        )
