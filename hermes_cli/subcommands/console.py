"""``xhermes console`` subcommand parser."""

from __future__ import annotations

from typing import Callable


def build_console_parser(subparsers, *, cmd_console: Callable) -> None:
    """Attach the safe XHermes Console REPL subcommand."""
    console_parser = subparsers.add_parser(
        "console",
        help="Open the safe XHermes command console",
        description=(
            "Open a curated XHermes command REPL. This is not a raw shell and "
            "does not expose the full XHermes CLI."
        ),
    )
    console_parser.set_defaults(func=cmd_console)
