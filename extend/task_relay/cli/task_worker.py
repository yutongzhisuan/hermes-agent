"""Thin ``hermes task-worker`` style wrapper for the Task Relay Worker."""

from __future__ import annotations

import sys
from typing import Sequence

from extend.task_relay.worker.__main__ import main as worker_main


def main(argv: Sequence[str] | None = None) -> int:
    """Delegate to ``extend.task_relay.worker`` CLI."""
    return worker_main(list(argv) if argv is not None else None)


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
