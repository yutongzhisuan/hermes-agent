"""Tests for headless wheel UI entrypoint guards."""

from __future__ import annotations

import argparse

import pytest

from hermes_cli.headless_dist import exit_if_ui_unavailable, is_headless_dist


def test_is_headless_dist_reflects_wheel_marker(monkeypatch):
    monkeypatch.setattr("xhermes_agent_data.is_headless_wheel_install", lambda: False)
    assert is_headless_dist() is False
    monkeypatch.setattr("xhermes_agent_data.is_headless_wheel_install", lambda: True)
    assert is_headless_dist() is True


def test_exit_if_ui_unavailable_noop_when_not_headless(monkeypatch):
    monkeypatch.setattr("hermes_cli.headless_dist.is_headless_dist", lambda: False)
    exit_if_ui_unavailable(feature="dashboard")


@pytest.mark.parametrize("feature", ["dashboard", "desktop", "tui"])
def test_exit_if_ui_unavailable_blocks_ui(monkeypatch, feature):
    monkeypatch.setattr("hermes_cli.headless_dist.is_headless_dist", lambda: True)
    with pytest.raises(SystemExit) as exc:
        exit_if_ui_unavailable(feature=feature)
    assert exc.value.code == 1


def test_cmd_dashboard_blocks_browser_ui_in_headless_dist(monkeypatch):
    monkeypatch.setattr("hermes_cli.headless_dist.is_headless_dist", lambda: True)
    from hermes_cli.main import cmd_dashboard

    args = argparse.Namespace(
        status=False,
        stop=False,
        headless_backend=False,
        ssh_session_token_file=None,
        ssh_owner_nonce=None,
    )
    with pytest.raises(SystemExit) as exc:
        cmd_dashboard(args)
    assert exc.value.code == 1
