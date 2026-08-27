"""Tests for relay progress/checkpoint policy."""

from __future__ import annotations

from extend.task_relay.progress_policy import (
    DEFAULT_PROGRESS_MODE,
    PROGRESS_MODE_MINIMAL,
    PROGRESS_MODE_OFF,
    RelayRuntimeOptions,
    default_sidecar_options,
    parse_relay_options,
)


def test_default_stateless_progress_is_minimal():
    opts = default_sidecar_options(stateless=True)
    assert opts.progress_mode == PROGRESS_MODE_MINIMAL


def test_default_non_stateless_progress_is_tools():
    opts = default_sidecar_options(stateless=False)
    assert opts.progress_mode == "tools"


def test_parse_relay_options_normalizes():
    opts = parse_relay_options(
        {"progress_mode": "OFF", "checkpoint_every_steps": -3}
    )
    assert opts.progress_mode == PROGRESS_MODE_OFF
    assert opts.checkpoint_every_steps == 0


def test_parse_relay_options_defaults():
    opts = parse_relay_options(None)
    assert opts.progress_mode == DEFAULT_PROGRESS_MODE
