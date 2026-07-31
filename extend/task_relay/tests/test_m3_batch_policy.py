"""BatchPolicy helper unit tests."""

from __future__ import annotations

from extend.task_relay.hub.batch_policy import (
    completion_threshold_met,
    normalize_completion_mode,
)
from extend.task_relay.hub.models import Task


def _task(task_id: str, status: str) -> Task:
    return Task(
        task_id=task_id,
        goal="g",
        callback_topic="t",
        status=status,
        created_at=0.0,
    )


def test_normalize_completion_mode_defaults_to_all():
    assert normalize_completion_mode({}) == "ALL"


def test_completion_threshold_any():
    members = [_task("a", "completed"), _task("b", "pending")]
    assert completion_threshold_met(members, {"completion_mode": "ANY"})


def test_completion_threshold_majority():
    members = [
        _task("a", "completed"),
        _task("b", "completed"),
        _task("c", "pending"),
    ]
    assert completion_threshold_met(members, {"completion_mode": "MAJORITY"})


def test_completion_threshold_numeric_mode():
    members = [_task("a", "completed"), _task("b", "pending")]
    assert completion_threshold_met(members, {"completion_mode": 2})
