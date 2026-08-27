"""``xhermes sub-agent-acp-rpc`` subcommand parser."""

from __future__ import annotations

import argparse
from typing import Callable


def build_sub_agent_acp_rpc_parser(subparsers, *, cmd_sub_agent_acp_rpc: Callable) -> None:
    """Attach the ``sub-agent-acp-rpc`` subcommand."""
    parser = subparsers.add_parser(
        "sub-agent-acp-rpc",
        help="Run the XHermes ACP JSON-RPC server for remote-acp workers",
        description=(
            "Expose in-process XHermes sub-agent execution over JSON-RPC "
            "(acp.run / acp.cancel) for remote-acp worker backends."
        ),
        add_help=False,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "rpc_args",
        nargs=argparse.REMAINDER,
        help=argparse.SUPPRESS,
    )
    parser.set_defaults(func=cmd_sub_agent_acp_rpc)
