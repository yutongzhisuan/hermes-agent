"""Tests for the executor-side toolset whitelist (M2-W4 sandbox profile).

Behavior contracts covered:

- The default executor profile whitelists only inference-support, retrieval
  and file read/write toolsets; shell/terminal/browser/system-control
  toolsets are excluded unless the node operator opts in.
- ``AcpTaskBackend`` enforces the whitelist at ACP session creation: denied
  toolsets never reach ``create_session``, and a fully-denied request
  resolves to an explicit empty list ("no tools"), never to broader
  manager defaults.
- Operator widening (config/env) re-enables shell-class toolsets.
- The announce manifest (``acp.toolsets`` / ``announce_toolsets``) matches
  the effective whitelist, and ``validate_announce`` flags drift.
"""

from __future__ import annotations

import asyncio
import threading
import uuid
from typing import Any
from unittest.mock import AsyncMock

import pytest
from aiohttp import ClientSession, web

from extend.task_relay.acp_backend import AcpTaskBackend
from extend.task_relay.acp_rpc_server import (
    _build_arg_parser,
    _resolve_executor_profile,
    create_acp_rpc_app,
)
from extend.task_relay.executor_profile import (
    DEFAULT_EXECUTOR_TOOLSETS,
    SHELL_CLASS_TOOLSETS,
    ExecutorProfile,
)
from extend.task_relay.task_types import TaskRunPayload


class FakeAgent:
    def __init__(self):
        self.step_callback: Any = None

    def interrupt(self) -> None:
        pass

    def run_conversation(self, **kwargs: Any) -> dict[str, Any]:
        return {"final_response": "done", "messages": [], "api_calls": 1}


class FakeSessionState:
    def __init__(self):
        self.session_id = str(uuid.uuid4())
        self.agent = FakeAgent()
        self.history: list[dict[str, Any]] = []
        self.cancel_event = threading.Event()


class FakeStatelessManager:
    def __init__(self):
        self.created: list[tuple[str, list[str] | None]] = []

    def create_session(self, cwd: str = ".", toolsets: list[str] | None = None):
        self.created.append((cwd, toolsets))
        return FakeSessionState()

    def save_session(self, session_id: str) -> None:
        pass

    def remove_session(self, session_id: str) -> None:
        pass


def _run_payload(toolsets: list[str] | None = None) -> TaskRunPayload:
    return TaskRunPayload(
        task_id="t1",
        attempt=1,
        goal="test goal",
        params=None,
        context=None,
        toolsets=list(toolsets or []),
        timeout_seconds=60,
        first_progress_seconds=None,
        trace_context=None,
        resume_from_checkpoint=None,
    )


def _make_backend(manager: FakeStatelessManager, **kwargs: Any) -> AcpTaskBackend:
    return AcpTaskBackend(
        session_manager=manager,
        progress_interval_seconds=0.0,
        stateless=True,
        **kwargs,
    )


# ---- profile defaults -------------------------------------------------------


def test_default_profile_excludes_shell_class_toolsets():
    profile = ExecutorProfile()
    assert list(profile.allowed) == list(DEFAULT_EXECUTOR_TOOLSETS)
    # No shell/terminal/browser/system-control capability by default.
    assert not (set(profile.allowed) & SHELL_CLASS_TOOLSETS)
    # Retrieval + file read/write are the safe default core.
    assert {"file", "web", "todo"} <= set(profile.allowed)


def test_resolve_filters_denied_toolsets():
    profile = ExecutorProfile()
    resolved = profile.resolve(["terminal", "web", "browser", "memory", "not-real"])
    assert resolved == ["web"]


def test_resolve_without_request_returns_whitelist():
    profile = ExecutorProfile()
    assert profile.resolve(None) == list(DEFAULT_EXECUTOR_TOOLSETS)
    assert profile.resolve([]) == list(DEFAULT_EXECUTOR_TOOLSETS)


def test_resolve_fully_denied_request_stays_empty():
    profile = ExecutorProfile()
    assert profile.resolve(["terminal"]) == []


# ---- operator widening ------------------------------------------------------


def test_build_extra_widens_whitelist():
    profile = ExecutorProfile.build(extra=["terminal"])
    assert "terminal" in profile.allowed
    assert profile.resolve(["terminal", "browser"]) == ["terminal"]


def test_build_allowed_replaces_default():
    profile = ExecutorProfile.build(allowed=["web"])
    assert list(profile.allowed) == ["web"]
    assert profile.resolve(None) == ["web"]
    assert profile.resolve(["file"]) == []


def test_from_env(monkeypatch):
    monkeypatch.setenv("ACP_EXECUTOR_TOOLSETS", "web,terminal")
    monkeypatch.setenv("ACP_EXECUTOR_ALLOW_EXTRA", "vision")
    profile = ExecutorProfile.from_env()
    assert list(profile.allowed) == ["web", "terminal", "vision"]


# ---- backend enforcement at session creation --------------------------------


@pytest.mark.asyncio
async def test_backend_strips_denied_toolsets_before_session_create():
    manager = FakeStatelessManager()
    backend = _make_backend(manager)

    await backend.run(
        _run_payload(toolsets=["terminal", "web", "browser"]),
        AsyncMock(),
        AsyncMock(),
        asyncio.Event(),
    )

    # The denied shell/browser toolsets never reach the session.
    assert manager.created[0][1] == ["web"]


@pytest.mark.asyncio
async def test_backend_default_session_uses_profile_whitelist():
    manager = FakeStatelessManager()
    backend = _make_backend(manager)

    await backend.run(_run_payload(), AsyncMock(), AsyncMock(), asyncio.Event())

    # No requested toolsets → exactly the profile whitelist (no terminal).
    assert manager.created[0][1] == list(DEFAULT_EXECUTOR_TOOLSETS)


@pytest.mark.asyncio
async def test_backend_fully_denied_request_yields_empty_toolsets():
    manager = FakeStatelessManager()
    backend = _make_backend(manager)

    await backend.run(
        _run_payload(toolsets=["terminal"]),
        AsyncMock(),
        AsyncMock(),
        asyncio.Event(),
    )

    # Explicit empty list: "no tools", not a fall back to manager defaults.
    assert manager.created[0][1] == []


@pytest.mark.asyncio
async def test_backend_custom_profile_allows_terminal():
    manager = FakeStatelessManager()
    backend = _make_backend(
        manager,
        executor_profile=ExecutorProfile.build(extra=["terminal"]),
    )

    await backend.run(
        _run_payload(toolsets=["terminal"]),
        AsyncMock(),
        AsyncMock(),
        asyncio.Event(),
    )

    assert manager.created[0][1] == ["terminal"]


@pytest.mark.asyncio
async def test_backend_logs_effective_whitelist(caplog):
    manager = FakeStatelessManager()
    backend = _make_backend(manager)

    with caplog.at_level("INFO", logger="task_relay.worker.backends.acp"):
        await backend.run(
            _run_payload(toolsets=["web", "browser"]),
            AsyncMock(),
            AsyncMock(),
            asyncio.Event(),
        )

    messages = [r.getMessage() for r in caplog.records]
    line = next(m for m in messages if "executor toolsets" in m)
    assert "['web']" in line  # effective toolsets
    assert "browser" in line  # requested list is logged for audit


# ---- announce alignment -----------------------------------------------------


def test_announce_manifest_matches_effective_whitelist():
    profile = ExecutorProfile.build(extra=["terminal"])
    assert profile.announce_toolsets() == profile.resolve(None)


def test_validate_announce_flags_unservable_toolsets():
    profile = ExecutorProfile()
    assert profile.validate_announce(["file", "web"]) == []
    assert profile.validate_announce(["file", "terminal"]) == ["terminal"]


@pytest.mark.asyncio
async def test_rpc_toolsets_reports_profile_whitelist():
    profile = ExecutorProfile.build(allowed=["web", "todo"])
    app = create_acp_rpc_app(executor_profile=profile)
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", 0)
    await site.start()
    port = site._server.sockets[0].getsockname()[1]
    try:
        body = {"jsonrpc": "2.0", "id": 1, "method": "acp.toolsets", "params": {}}
        async with ClientSession() as session:
            async with session.post(f"http://127.0.0.1:{port}/rpc", json=body) as resp:
                assert resp.status == 200
                payload = await resp.json()
        assert payload["result"]["toolsets"] == ["web", "todo"]
    finally:
        await runner.cleanup()


# ---- CLI / env plumbing -----------------------------------------------------


def test_rpc_parser_accepts_executor_profile_flags():
    args = _build_arg_parser().parse_args(
        ["--executor-toolsets", "web,terminal", "--executor-allow-extra", "vision"]
    )
    assert args.executor_toolsets == "web,terminal"
    assert args.executor_allow_extra == "vision"


def test_resolve_executor_profile_cli_overrides_env(monkeypatch):
    monkeypatch.setenv("ACP_EXECUTOR_TOOLSETS", "web")
    args = _build_arg_parser().parse_args(["--executor-toolsets", "file,todo"])
    profile = _resolve_executor_profile(args)
    assert list(profile.allowed) == ["file", "todo"]


def test_resolve_executor_profile_defaults(monkeypatch):
    monkeypatch.delenv("ACP_EXECUTOR_TOOLSETS", raising=False)
    monkeypatch.delenv("ACP_EXECUTOR_ALLOW_EXTRA", raising=False)
    args = _build_arg_parser().parse_args([])
    profile = _resolve_executor_profile(args)
    assert list(profile.allowed) == list(DEFAULT_EXECUTOR_TOOLSETS)
