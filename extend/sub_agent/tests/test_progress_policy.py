"""Tests for sub-agent progress/checkpoint policy."""

from __future__ import annotations

from extend.sub_agent.progress_policy import (
    DEFAULT_PROGRESS_MODE,
    PROGRESS_MODE_MINIMAL,
    PROGRESS_MODE_OFF,
    SubAgentRuntimeOptions,
    default_sidecar_options,
    parse_sub_agent_options,
)


def test_default_stateless_progress_is_minimal():
    opts = default_sidecar_options(stateless=True)
    assert opts.progress_mode == PROGRESS_MODE_MINIMAL


def test_default_non_stateless_progress_is_tools():
    opts = default_sidecar_options(stateless=False)
    assert opts.progress_mode == "tools"


def test_parse_sub_agent_options_normalizes():
    opts = parse_sub_agent_options(
        {"progress_mode": "OFF", "checkpoint_every_steps": -3}
    )
    assert opts.progress_mode == PROGRESS_MODE_OFF
    assert opts.checkpoint_every_steps == 0


def test_runtime_options_from_params_prefers_sub_agent_key():
    from extend.sub_agent.progress_policy import runtime_options_from_params

    raw = runtime_options_from_params(
        {"sub_agent_options": {"progress_mode": "off"}, "relay_options": {"progress_mode": "tools"}}
    )
    assert raw == {"progress_mode": "off"}


def test_runtime_options_from_params_legacy_relay_options():
    from extend.sub_agent.progress_policy import runtime_options_from_params

    raw = runtime_options_from_params({"relay_options": {"progress_mode": "minimal"}})
    assert raw == {"progress_mode": "minimal"}
