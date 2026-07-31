"""Shared JSON helpers for Hub modules."""

from __future__ import annotations

import json
from typing import Any


def safe_json_loads(data: bytes | str | None) -> Any:
    """Parse JSON text/bytes; return None on empty or invalid input."""
    if data is None:
        return None
    if isinstance(data, bytes):
        try:
            data = data.decode("utf-8")
        except UnicodeDecodeError:
            return None
    if not data:
        return None
    try:
        return json.loads(data)
    except json.JSONDecodeError:
        return None


def safe_json_dict_loads(data: bytes | str | None) -> dict | None:
    """Like :func:`safe_json_loads` but return only JSON objects."""
    loaded = safe_json_loads(data)
    return loaded if isinstance(loaded, dict) else None
