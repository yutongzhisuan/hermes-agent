"""Subprocess-first SDK for third-party desktop hosts."""

from hermes_runtime.exceptions import RuntimeBinaryNotFound, RuntimeStartError
from hermes_runtime.rpc import GatewayRpcClient, GatewayRpcError
from hermes_runtime.runtime import HermesRuntime, RuntimeInfo

__all__ = [
    "GatewayRpcClient",
    "GatewayRpcError",
    "HermesRuntime",
    "RuntimeBinaryNotFound",
    "RuntimeInfo",
    "RuntimeStartError",
]
