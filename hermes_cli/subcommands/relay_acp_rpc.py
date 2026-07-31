"""``hermes relay-acp-rpc`` subcommand parser."""

from __future__ import annotations

import argparse
from typing import Callable


def build_relay_acp_rpc_parser(subparsers, *, cmd_relay_acp_rpc: Callable) -> None:
    """Attach the ``relay-acp-rpc`` subcommand."""
    parser = subparsers.add_parser(
        "relay-acp-rpc",
        help="Run the Hermes ACP JSON-RPC server for remote-acp workers",
        description=(
            "Expose in-process Hermes ACP execution over HTTP JSON-RPC "
            "(acp.run / acp.cancel) for Task Relay remote-acp backends."
        ),
        add_help=False,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "rpc_args",
        nargs=argparse.REMAINDER,
        help=argparse.SUPPRESS,
    )
    parser.set_defaults(func=cmd_relay_acp_rpc)
