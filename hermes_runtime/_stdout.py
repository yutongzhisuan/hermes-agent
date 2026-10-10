"""Timed reads from a subprocess stdout pipe."""

from __future__ import annotations

import sys
from typing import IO


def read_stdout_chunk(stream: IO[bytes], timeout_s: float) -> bytes | None:
    """Return the next stdout chunk, or ``None`` when no data before timeout."""
    if timeout_s <= 0:
        return None
    if sys.platform != "win32":
        import select

        ready, _, _ = select.select([stream], [], [], timeout_s)
        if not ready:
            return None
    chunk = stream.read1(4096) if hasattr(stream, "read1") else stream.read(4096)
    return chunk or None
