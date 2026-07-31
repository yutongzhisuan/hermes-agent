"""``hermes relay-hub`` subcommand parser."""

from __future__ import annotations

import argparse
from typing import Callable


def build_relay_hub_parser(subparsers, *, cmd_relay_hub: Callable) -> None:
    """Attach the ``relay-hub`` subcommand."""
    parser = subparsers.add_parser(
        "relay-hub",
        help="Run the Task Relay Hub (gRPC master + WebSocket worker)",
        description="Start the distributed Task Relay Hub control plane.",
        add_help=False,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "hub_args",
        nargs=argparse.REMAINDER,
        help=argparse.SUPPRESS,
    )
    parser.set_defaults(func=cmd_relay_hub)
