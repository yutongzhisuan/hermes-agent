"""End-to-end smoke test: headless wheel install → serve → gateway.ready."""

from __future__ import annotations

import asyncio
import json
import os
import subprocess
import sys
from pathlib import Path

import pytest

from hermes_runtime.runtime import HermesRuntime
from tests.test_headless_wheel_assets import _build_headless_wheel


async def _wait_for_gateway_ready(ws_url: str, *, timeout_s: float = 60.0) -> None:
    import websockets

    deadline = asyncio.get_running_loop().time() + timeout_s
    async with websockets.connect(ws_url) as ws:
        while asyncio.get_running_loop().time() < deadline:
            remaining = deadline - asyncio.get_running_loop().time()
            raw = await asyncio.wait_for(ws.recv(), timeout=max(0.1, remaining))
            frame = json.loads(raw)
            if frame.get("method") != "event":
                continue
            params = frame.get("params") or {}
            if params.get("type") == "gateway.ready":
                return
    raise AssertionError("timed out waiting for gateway.ready")


@pytest.mark.integration
def test_headless_wheel_serve_emits_gateway_ready(tmp_path, monkeypatch):
    wheel = _build_headless_wheel(tmp_path)
    venv = tmp_path / "venv"
    subprocess.run([sys.executable, "-m", "venv", str(venv)], check=True)
    pip = venv / "bin" / "pip"
    py = venv / "bin" / "python"
    xhermes = venv / "bin" / "xhermes"
    subprocess.run([str(pip), "install", str(wheel)], check=True, capture_output=True)

    home = tmp_path / ".xhermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))

    rt = HermesRuntime(xhermes_executable=str(xhermes), hermes_home=home)
    info = rt.start(timeout_s=120)
    try:
        assert info.ws_url
        asyncio.run(_wait_for_gateway_ready(info.ws_url, timeout_s=60.0))
    finally:
        rt.stop(grace_s=5)
