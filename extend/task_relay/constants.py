"""Shared protocol constants for the Task Relay package."""

from __future__ import annotations

# Marker used by the Hub for execution/lease timeout cancels.  It is stored in
# ``Task.cancel_reason`` and delivered to workers in ``task.cancel`` frames.
# Workers settle tasks cancelled with this exact reason as ``failed``; any other
# reason is treated as a normal cancel and settles as ``cancelled``.
CANCEL_REASON_TIMEOUT = "__timeout__"
