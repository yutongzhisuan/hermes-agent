"""master-planner must be discoverable and default-enabled for Swarm mode."""

from __future__ import annotations

from pathlib import Path

from hermes_cli.plugins_cmd import (
    _MASTER_PLANNER_PLUGIN_KEYS,
    ensure_master_planner_plugin_enabled_in_config,
)


def test_bundled_master_planner_shim_exists():
    root = Path(__file__).resolve().parents[1]
    plugin_dir = root / "plugins" / "master-planner"
    assert (plugin_dir / "plugin.yaml").is_file()
    assert (plugin_dir / "__init__.py").is_file()
    text = (plugin_dir / "plugin.yaml").read_text(encoding="utf-8")
    assert "name: master-planner" in text


def test_ensure_enables_when_missing():
    cfg: dict = {"plugins": {"enabled": [], "disabled": []}}
    assert ensure_master_planner_plugin_enabled_in_config(cfg) is True
    assert "master-planner" in cfg["plugins"]["enabled"]


def test_ensure_noop_when_already_enabled():
    cfg = {"plugins": {"enabled": ["master-planner"], "disabled": []}}
    assert ensure_master_planner_plugin_enabled_in_config(cfg) is False


def test_ensure_respects_disabled():
    cfg = {"plugins": {"enabled": [], "disabled": ["master-planner"]}}
    assert ensure_master_planner_plugin_enabled_in_config(cfg) is False
    assert cfg["plugins"]["enabled"] == []


def test_ensure_creates_plugins_block():
    cfg: dict = {}
    assert ensure_master_planner_plugin_enabled_in_config(cfg) is True
    assert set(cfg["plugins"]["enabled"]) & _MASTER_PLANNER_PLUGIN_KEYS


def test_default_config_enables_master_planner():
    from hermes_cli.config_defaults import DEFAULT_CONFIG

    enabled = DEFAULT_CONFIG.get("plugins", {}).get("enabled", [])
    assert "master-planner" in enabled
    assert DEFAULT_CONFIG.get("_config_version") == 34
