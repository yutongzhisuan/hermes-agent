"""Hub Prometheus-style counters (M3)."""

from __future__ import annotations

from collections import defaultdict
from threading import Lock

_lock = Lock()
_counters: dict[tuple[str, tuple[tuple[str, str], ...]], float] = defaultdict(float)


def inc(name: str, value: float = 1.0, **labels: str) -> None:
    key = (name, tuple(sorted(labels.items())))
    with _lock:
        _counters[key] += value


def snapshot() -> dict[str, float]:
    with _lock:
        out: dict[str, float] = {}
        for (name, label_items), value in _counters.items():
            if label_items:
                suffix = ",".join(f'{k}="{v}"' for k, v in label_items)
                out[f"{name}{{{suffix}}}"] = value
            else:
                out[name] = value
        return dict(out)


def reset() -> None:
    with _lock:
        _counters.clear()
