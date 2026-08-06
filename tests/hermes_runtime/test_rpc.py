"""Unit tests for GatewayRpcClient frame handling."""

from __future__ import annotations

import asyncio

import pytest

from hermes_runtime.rpc import GatewayRpcClient, GatewayRpcError


@pytest.mark.asyncio
async def test_gateway_rpc_client_resolves_request():
    client = GatewayRpcClient(request_timeout_s=5.0)

    async def _respond():
        await asyncio.sleep(0.01)
        client._handle_frame({"jsonrpc": "2.0", "id": 0, "result": {"ok": True}})

    task = asyncio.create_task(_respond())
    fut = asyncio.get_running_loop().create_future()
    client._pending[0] = fut
    result = await asyncio.wait_for(fut, timeout=1)
    await task
    assert result == {"ok": True}


@pytest.mark.asyncio
async def test_gateway_rpc_client_dispatches_events():
    client = GatewayRpcClient()
    seen: list[str] = []
    client.on_event(lambda params: seen.append(params.get("type", "")))
    client._handle_frame(
        {
            "jsonrpc": "2.0",
            "method": "event",
            "params": {"type": "gateway.ready", "payload": {}},
        }
    )
    assert seen == ["gateway.ready"]


@pytest.mark.asyncio
async def test_gateway_rpc_client_maps_json_rpc_errors():
    client = GatewayRpcClient()
    fut = asyncio.get_running_loop().create_future()
    client._pending[1] = fut
    client._handle_frame({"jsonrpc": "2.0", "id": 1, "error": {"message": "nope"}})
    with pytest.raises(GatewayRpcError, match="nope"):
        await fut
