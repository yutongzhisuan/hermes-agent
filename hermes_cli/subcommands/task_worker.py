"""``xhermes task-worker`` subcommand parser."""

from __future__ import annotations

import argparse
from typing import Callable


def build_task_worker_parser(subparsers, *, cmd_task_worker: Callable) -> None:
    """Attach the ``task-worker`` subcommand."""
    parser = subparsers.add_parser(
        "task-worker",
        help="Run a Task Relay worker (Mode A poll loop)",
        description="Connect a worker to a Task Relay Hub and execute tasks.",
        add_help=False,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "worker_args",
        nargs=argparse.REMAINDER,
        help=argparse.SUPPRESS,
    )
    parser.set_defaults(func=cmd_task_worker)
