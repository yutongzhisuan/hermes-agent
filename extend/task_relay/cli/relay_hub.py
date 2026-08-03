"""Thin ``xhermes relay-hub`` style wrapper for the Task Relay Hub."""

from __future__ import annotations

import sys
from typing import Sequence

from extend.task_relay.hub.main import main as hub_main


def main(argv: Sequence[str] | None = None) -> int:
    """Delegate to ``extend.task_relay.hub.main``."""
    return hub_main(list(argv) if argv is not None else None)


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
