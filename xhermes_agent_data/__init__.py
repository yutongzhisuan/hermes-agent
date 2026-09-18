"""Bundled runtime data for headless pip wheel installs (skills, locales, …)."""

from __future__ import annotations

from pathlib import Path

DATA_ROOT = Path(__file__).resolve().parent
HEADLESS_WHEEL_MARKER = DATA_ROOT / ".headless_wheel_dist"


def is_headless_wheel_install() -> bool:
    return HEADLESS_WHEEL_MARKER.is_file()


def get_data_root() -> Path:
    return DATA_ROOT
