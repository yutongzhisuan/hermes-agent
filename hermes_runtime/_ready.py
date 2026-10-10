"""Parse headless backend ready announcements."""

from __future__ import annotations

import re

READY_LINE_RE = re.compile(
    rb"^XHERMES_(?:BACKEND|DASHBOARD)_READY port=(\d+)\s*$",
)


def parse_ready_port(chunk: bytes, buffer: bytearray) -> int | None:
    """Scan stdout bytes for a ready line; return port when found."""
    buffer.extend(chunk)
    while True:
        nl = buffer.find(b"\n")
        if nl < 0:
            break
        line = bytes(buffer[:nl]).strip()
        del buffer[: nl + 1]
        match = READY_LINE_RE.match(line)
        if match:
            return int(match.group(1))
    return None
