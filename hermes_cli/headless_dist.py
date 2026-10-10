"""Runtime detection for the headless pip wheel distribution."""

from __future__ import annotations

import sys

_HEADLESS_SERVE_HINT = (
    "Use `xhermes serve` and connect via WebSocket JSON-RPC "
    "(see docs/packaging/headless-wheel.md)."
)


def is_headless_dist() -> bool:
    """True when running from a headless pip wheel (no bundled UI assets)."""
    try:
        from xhermes_agent_data import is_headless_wheel_install
    except ImportError:
        return False
    return is_headless_wheel_install()


def exit_if_ui_unavailable(*, feature: str) -> None:
    """Abort UI entrypoints that are not shipped in the headless wheel."""
    if not is_headless_dist():
        return
    if feature == "tui":
        message = f"This distribution does not include the TUI. {_HEADLESS_SERVE_HINT}"
    else:
        message = f"This distribution does not include a Web UI. {_HEADLESS_SERVE_HINT}"
    print(message, file=sys.stderr)
    raise SystemExit(1)
