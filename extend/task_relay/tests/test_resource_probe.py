"""Tests for worker resource probing."""

from __future__ import annotations

from extend.task_relay.worker.resource_probe import (
    probe_capabilities,
    probe_load,
    probe_resources,
)


def test_probe_capabilities_includes_toolsets():
    caps = probe_capabilities(["terminal", "file"])
    assert caps["toolsets"] == ["terminal", "file"]
    assert caps["os"]
    assert caps["arch"]


def test_probe_resources_reports_cpu_and_memory():
    resources = probe_resources()
    assert resources["cpu_cores"] >= 1
    assert resources["memory_gb"] >= 1


def test_probe_load_tracks_running_tasks():
    load = probe_load(running_tasks=2)
    assert load["running_tasks"] == 2
