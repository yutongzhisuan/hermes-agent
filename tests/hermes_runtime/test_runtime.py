"""Unit tests for hermes_runtime ready-line parsing and lifecycle helpers."""

from __future__ import annotations

import os
import subprocess
import sys
import time
from pathlib import Path

import pytest

from hermes_runtime.exceptions import RuntimeBinaryNotFound, RuntimeStartError
from hermes_runtime._ready import parse_ready_port
from hermes_runtime.runtime import HermesRuntime


def test_parse_ready_port_finds_backend_line():
    buf = bytearray()
    port = parse_ready_port(b"noise\nXHERMES_BACKEND_READY port=9123\n", buf)
    assert port == 9123
    assert buf == b""


def test_parse_ready_port_buffers_partial_line():
    buf = bytearray()
    assert parse_ready_port(b"XHERMES_BACKEND", buf) is None
    port = parse_ready_port(b"_READY port=4400\n", buf)
    assert port == 4400


def test_runtime_stop_is_idempotent():
    rt = HermesRuntime(hermes_home="/tmp/unused-hermes-home")
    rt.stop()
    rt.stop()


def test_runtime_start_missing_binary(tmp_path, monkeypatch):
    monkeypatch.setenv("XHERMES_HOME", str(tmp_path / ".xhermes"))
    rt = HermesRuntime(xhermes_executable="/no/such/xhermes", hermes_home=tmp_path / ".xhermes")
    with pytest.raises(RuntimeBinaryNotFound):
        rt.start(timeout_s=1)


def test_runtime_start_parses_fake_backend(tmp_path, monkeypatch):
    home = tmp_path / ".xhermes"
    home.mkdir()
    monkeypatch.setenv("XHERMES_HOME", str(home))

    script = tmp_path / "fake_xhermes"
    script.write_text(
        "#!/usr/bin/env python3\n"
        "import sys, time\n"
        "print('booting', flush=True)\n"
        "print('XHERMES_BACKEND_READY port=8765', flush=True)\n"
        "time.sleep(60)\n",
        encoding="utf-8",
    )
    script.chmod(0o755)

    rt = HermesRuntime(xhermes_executable=str(script), hermes_home=home)
    info = rt.start(timeout_s=5)
    try:
        assert info.port == 8765
        assert info.token
        assert "8765" in info.ws_url
        assert f"token={info.token}" in info.ws_url or "token=" in info.ws_url
        assert rt.is_running()
    finally:
        rt.stop(grace_s=1)


def test_runtime_start_timeout_kills_child(tmp_path, monkeypatch):
    home = tmp_path / ".xhermes"
    home.mkdir()
    monkeypatch.setenv("XHERMES_HOME", str(home))

    script = tmp_path / "hang_xhermes"
    script.write_text(
        "#!/usr/bin/env python3\nimport time\ntime.sleep(120)\n",
        encoding="utf-8",
    )
    script.chmod(0o755)

    rt = HermesRuntime(xhermes_executable=str(script), hermes_home=home)

    with pytest.raises(RuntimeStartError, match="timed out"):
        rt.start(timeout_s=0.3)
    assert not rt.is_running()
