"""Shared protocol constants for the Task Relay package.

Mirrors the constants defined by the swarm-network Task Relay Hub so the
ACP sidecar can attribute timeout cancels without importing the Hub code.
"""

from __future__ import annotations

# Marker used by the Hub for execution/lease timeout cancels.  It is stored in
# ``Task.cancel_reason`` and delivered to workers in ``task.cancel`` frames.
# Workers settle tasks cancelled with this exact reason as ``failed``; any other
# reason is treated as a normal cancel and settles as ``cancelled``.
CANCEL_REASON_TIMEOUT = "__timeout__"

# Default Task Relay ACP sidecar transport (Worker dials this UDS by default).
DEFAULT_ACP_RPC_SOCKET = "~/.xhermes/relay/acp.sock"

# HTTP fallback when the sidecar is started with ``--http``.
DEFAULT_ACP_RPC_HTTP_HOST = "127.0.0.1"
DEFAULT_ACP_RPC_HTTP_PORT = 9105
DEFAULT_ACP_RPC_HTTP_URL = (
    f"http://{DEFAULT_ACP_RPC_HTTP_HOST}:{DEFAULT_ACP_RPC_HTTP_PORT}/rpc"
)
