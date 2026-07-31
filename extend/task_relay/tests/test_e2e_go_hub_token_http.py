"""Go Hub E2E: bootstrap token HTTP endpoint."""

from __future__ import annotations

import json
import urllib.request

import pytest

from extend.task_relay.tests.live_hub import HubLaunchConfig, start_live_hub, stop_live_hub


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


@pytest.mark.asyncio
async def test_go_hub_bootstrap_token_exchange(tmp_path):
    hub = await start_live_hub(
        tmp_path,
        HubLaunchConfig(bootstrap_tokens="boot-w1=w1:terminal:2"),
    )
    try:
        assert hub.http_url is not None
        url = f"{hub.http_url}/v1/worker/token"
        req = urllib.request.Request(
            url,
            data=json.dumps({"bootstrap_token": "boot-w1", "worker_id": "w1"}).encode(),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=3) as resp:
            payload = json.loads(resp.read().decode())
        assert "worker_jwt" in payload
        assert payload["expires_at"] > 0
    finally:
        await stop_live_hub(hub)
