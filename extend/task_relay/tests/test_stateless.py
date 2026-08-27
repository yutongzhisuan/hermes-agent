"""Tests for the stateless relay execution mode.

Behavior contracts covered:

- Requested toolsets are filtered: anything that reads or mutates local user
  state (memory / skills / session_search / cronjob / ...) is dropped, as are
  unknown toolset names.
- A stateless run executes in a temporary workdir, never persists the
  session, and removes both session and workdir when the task ends.
- ``StatelessSessionManager`` keeps its transcript in an ephemeral SQLite
  under ``state_root`` and builds agents with ``skip_memory=True`` plus the
  narrowed toolset list.
"""

from __future__ import annotations

import asyncio
import threading
import uuid
from typing import Any
from unittest.mock import AsyncMock

import pytest

from extend.task_relay.acp_backend import AcpTaskBackend
from extend.task_relay.stateless import (
    BLOCKED_STATELESS_TOOLSETS,
    DEFAULT_STATELESS_TOOLSETS,
    StatelessSessionManager,
    resolve_stateless_toolsets,
)
from extend.task_relay.task_types import TaskRunPayload


class FakeAgent:
    """Fake XHermes AIAgent that returns immediately."""

    def __init__(self):
        self.interrupted = False
        self.step_callback: Any = None

    def interrupt(self) -> None:
        self.interrupted = True

    def run_conversation(self, **kwargs: Any) -> dict[str, Any]:
        return {
            "final_response": "all done",
            "messages": [],
            "api_calls": 1,
            "session_id": "fake-xhermes-session",
        }


class FakeSessionState:
    def __init__(self):
        self.session_id = str(uuid.uuid4())
        self.agent = FakeAgent()
        self.history: list[dict[str, Any]] = []
        self.cancel_event = threading.Event()


class FakeStatelessManager:
    """Fake manager recording the stateless lifecycle calls."""

    def __init__(self):
        self.created: list[tuple[str, list[str] | None]] = []
        self.saved: list[str] = []
        self.removed: list[str] = []
        self.states: list[FakeSessionState] = []

    def create_session(self, cwd: str = ".", toolsets: list[str] | None = None):
        self.created.append((cwd, toolsets))
        state = FakeSessionState()
        self.states.append(state)
        return state

    def save_session(self, session_id: str) -> None:
        self.saved.append(session_id)

    def remove_session(self, session_id: str) -> None:
        self.removed.append(session_id)


def _run_payload(goal: str = "test goal", toolsets: list[str] | None = None) -> TaskRunPayload:
    return TaskRunPayload(
        task_id="t1",
        attempt=1,
        goal=goal,
        params=None,
        context=None,
        toolsets=list(toolsets or []),
        timeout_seconds=60,
        first_progress_seconds=None,
        trace_context=None,
        resume_from_checkpoint=None,
    )


# ---- toolset resolution -----------------------------------------------------


def test_default_toolsets_exclude_local_user_state():
    resolved = resolve_stateless_toolsets(None)
    assert resolved == list(DEFAULT_STATELESS_TOOLSETS)
    assert not (set(resolved) & BLOCKED_STATELESS_TOOLSETS)
    # A worker must be able to execute and inspect files out of the box.
    assert {"terminal", "file"} <= set(resolved)


def test_requested_toolsets_drop_blocked_and_unknown():
    resolved = resolve_stateless_toolsets(
        ["memory", "skills", "session_search", "cronjob", "web", "not-a-real-toolset"]
    )
    assert resolved == ["web"]


def test_requested_toolsets_preserve_order_and_dedupe():
    resolved = resolve_stateless_toolsets(["todo", "terminal", "todo"])
    assert resolved == ["todo", "terminal"]


# ---- backend lifecycle ------------------------------------------------------


@pytest.mark.asyncio
async def test_stateless_run_discards_session_and_workdir(tmp_path):
    manager = FakeStatelessManager()
    backend = AcpTaskBackend(
        session_manager=manager,
        progress_interval_seconds=0.0,
        stateless=True,
        workdir_root=str(tmp_path),
    )

    result = await backend.run(_run_payload(), AsyncMock(), AsyncMock(), asyncio.Event())

    assert result.status == "completed"
    cwd, _toolsets = manager.created[0]
    # The task ran in a fresh temp workdir under workdir_root, not ".".
    assert cwd != "."
    assert str(tmp_path) in cwd
    # Session was never persisted and was discarded with its workdir.
    assert manager.saved == []
    assert manager.removed == [manager.states[0].session_id]
    import os

    assert not os.path.exists(cwd)


@pytest.mark.asyncio
async def test_stateless_run_discards_session_on_failure(tmp_path):
    class ExplodingManager(FakeStatelessManager):
        def create_session(self, cwd: str = ".", toolsets: list[str] | None = None):
            state = super().create_session(cwd=cwd, toolsets=toolsets)

            def _boom(**kwargs: Any) -> dict[str, Any]:
                raise RuntimeError("agent exploded")

            state.agent.run_conversation = _boom
            return state

    manager = ExplodingManager()
    backend = AcpTaskBackend(
        session_manager=manager,
        progress_interval_seconds=0.0,
        stateless=True,
        workdir_root=str(tmp_path),
    )

    result = await backend.run(_run_payload(), AsyncMock(), AsyncMock(), asyncio.Event())

    assert result.status == "failed"
    assert manager.saved == []
    assert manager.removed == [manager.states[0].session_id]
    import os

    assert not os.path.exists(manager.created[0][0])


@pytest.mark.asyncio
async def test_stateless_run_forwards_requested_toolsets(tmp_path):
    manager = FakeStatelessManager()
    backend = AcpTaskBackend(
        session_manager=manager,
        progress_interval_seconds=0.0,
        stateless=True,
        workdir_root=str(tmp_path),
    )

    await backend.run(
        _run_payload(toolsets=["web", "todo"]),
        AsyncMock(),
        AsyncMock(),
        asyncio.Event(),
    )

    # Whitelisted requested toolsets reach the session verbatim.
    assert manager.created[0][1] == ["web", "todo"]


@pytest.mark.asyncio
async def test_stateful_run_still_persists_session():
    manager = FakeStatelessManager()
    backend = AcpTaskBackend(session_manager=manager, progress_interval_seconds=0.0)

    # Stateful mode salvages updated history; give the agent some to persist.
    original_create = manager.create_session

    def _create_with_history(cwd: str = ".", toolsets: list[str] | None = None):
        state = original_create(cwd=cwd, toolsets=toolsets)

        def _run(**kwargs: Any) -> dict[str, Any]:
            return {
                "final_response": "all done",
                "messages": [{"role": "assistant", "content": "all done"}],
                "api_calls": 1,
                "session_id": "fake-xhermes-session",
            }

        state.agent.run_conversation = _run
        return state

    manager.create_session = _create_with_history

    result = await backend.run(_run_payload(), AsyncMock(), AsyncMock(), asyncio.Event())

    assert result.status == "completed"
    cwd, toolsets = manager.created[0]
    assert cwd == "."
    assert toolsets is None
    # Stateful mode keeps the existing salvage behavior and never discards.
    assert manager.saved == [manager.states[0].session_id]
    assert manager.removed == []


# ---- sandbox (docker) mode ---------------------------------------------------


def test_apply_sandbox_env_configures_terminal_backend(monkeypatch):
    from extend.task_relay.stateless import apply_sandbox_env

    for var in (
        "TERMINAL_ENV",
        "TERMINAL_DOCKER_IMAGE",
        "TERMINAL_DOCKER_NETWORK",
        "TERMINAL_CONTAINER_PERSISTENT",
        "TERMINAL_DOCKER_PERSIST_ACROSS_PROCESSES",
        "TERMINAL_CONTAINER_CPU",
        "TERMINAL_CONTAINER_MEMORY",
    ):
        monkeypatch.delenv(var, raising=False)

    apply_sandbox_env(sandbox="docker", image="img:x", cpu=2.0, memory_mb=1024)

    import os

    assert os.environ["TERMINAL_ENV"] == "docker"
    assert os.environ["TERMINAL_DOCKER_IMAGE"] == "img:x"
    # Untrusted remote tasks default to no container network and disposable,
    # non-shared containers.
    assert os.environ["TERMINAL_DOCKER_NETWORK"] == "false"
    assert os.environ["TERMINAL_CONTAINER_PERSISTENT"] == "false"
    assert os.environ["TERMINAL_DOCKER_PERSIST_ACROSS_PROCESSES"] == "false"
    assert os.environ["TERMINAL_CONTAINER_CPU"] == "2.0"
    assert os.environ["TERMINAL_CONTAINER_MEMORY"] == "1024"


def test_apply_sandbox_env_rejects_unknown_backend():
    from extend.task_relay.stateless import apply_sandbox_env

    with pytest.raises(ValueError):
        apply_sandbox_env(sandbox="nope")


def test_sandbox_manager_registers_container_overrides_and_cleans_up(
    tmp_path, monkeypatch
):
    terminal_tool = pytest.importorskip("tools.terminal_tool")
    monkeypatch.setenv("XHERMES_HOME", str(tmp_path / "user-home"))

    registered: list[tuple[str, dict[str, Any]]] = []
    cleaned: list[tuple[str, bool]] = []

    monkeypatch.setattr(
        terminal_tool,
        "register_task_env_overrides",
        lambda task_id, overrides: registered.append((task_id, overrides)),
    )
    monkeypatch.setattr(
        terminal_tool,
        "cleanup_vm",
        lambda task_id, *, force_remove=False: cleaned.append((task_id, force_remove)),
    )

    manager = StatelessSessionManager(
        state_root=tmp_path / "relay-state",
        agent_factory=FakeAgent,
        sandbox="docker",
        sandbox_image="img:x",
    )
    state = manager.create_session(cwd="/workspace")

    # The final override registration pins the session to its own container.
    assert registered[-1] == (
        state.session_id,
        {"docker_image": "img:x", "cwd": "/workspace"},
    )

    manager.remove_session(state.session_id)
    # The disposable container is force-removed with the session.
    assert cleaned == [(state.session_id, True)]


def test_sandbox_manager_rejects_unknown_backend(tmp_path):
    with pytest.raises(ValueError):
        StatelessSessionManager(state_root=tmp_path, sandbox="nope")


@pytest.mark.asyncio
async def test_sandbox_run_uses_container_cwd_and_no_host_workdir(tmp_path):
    manager = FakeStatelessManager()
    backend = AcpTaskBackend(
        session_manager=manager,
        progress_interval_seconds=0.0,
        sandbox="docker",
        workdir_root=str(tmp_path),
    )

    # --sandbox alone implies stateless.
    assert backend._stateless is True

    result = await backend.run(_run_payload(), AsyncMock(), AsyncMock(), asyncio.Event())

    assert result.status == "completed"
    from extend.task_relay.stateless import SANDBOX_CONTAINER_CWD

    cwd, _toolsets = manager.created[0]
    assert cwd == SANDBOX_CONTAINER_CWD
    assert manager.removed == [manager.states[0].session_id]
    assert manager.saved == []
    # No host-side workdir was created (the container is the workdir).
    assert list(tmp_path.iterdir()) == []


# ---- StatelessSessionManager ------------------------------------------------


def test_manager_persists_to_ephemeral_db(tmp_path, monkeypatch):
    pytest.importorskip("hermes_state")
    monkeypatch.setenv("XHERMES_HOME", str(tmp_path / "user-home"))

    state_root = tmp_path / "relay-state"
    manager = StatelessSessionManager(state_root=state_root, agent_factory=FakeAgent)
    state = manager.create_session(cwd=str(tmp_path))

    db = manager._get_db()
    assert db is not None
    row = db.get_session(state.session_id)
    assert row is not None
    # The transcript lives under state_root, not the user's XHERMES_HOME.
    assert (state_root / "state.db").exists()
    assert not (tmp_path / "user-home" / "state.db").exists()

    manager.remove_session(state.session_id)
    assert db.get_session(state.session_id) is None


def test_manager_discard_removes_owned_state_root(monkeypatch, tmp_path):
    monkeypatch.setenv("XHERMES_HOME", str(tmp_path / "user-home"))
    manager = StatelessSessionManager(agent_factory=FakeAgent)
    state_root = manager.state_root
    assert state_root.exists()

    manager.discard()

    assert not state_root.exists()


def test_manager_discard_keeps_explicit_state_root(tmp_path, monkeypatch):
    monkeypatch.setenv("XHERMES_HOME", str(tmp_path / "user-home"))
    state_root = tmp_path / "relay-state"
    manager = StatelessSessionManager(state_root=state_root, agent_factory=FakeAgent)

    manager.discard()

    assert state_root.exists()


def test_make_agent_uses_stateless_kwargs(tmp_path, monkeypatch):
    run_agent = pytest.importorskip("run_agent")
    monkeypatch.setenv("XHERMES_HOME", str(tmp_path / "user-home"))

    captured: dict[str, Any] = {}

    class CapturingAgent(FakeAgent):
        def __init__(self, **kwargs: Any):
            super().__init__()
            captured.update(kwargs)
            self.model = kwargs.get("model") or ""

    monkeypatch.setattr(run_agent, "AIAgent", CapturingAgent)

    import hermes_cli.config
    import hermes_cli.runtime_provider

    monkeypatch.setattr(hermes_cli.config, "load_config", lambda: {})

    def _no_runtime(requested: Any = None) -> dict[str, Any]:
        raise RuntimeError("no provider configured in tests")

    monkeypatch.setattr(hermes_cli.runtime_provider, "resolve_runtime_provider", _no_runtime)

    manager = StatelessSessionManager(
        state_root=tmp_path / "relay-state", agent_factory=None
    )
    manager.create_session(cwd=str(tmp_path), toolsets=["web", "memory", "skills"])

    assert captured["platform"] == "acp"
    assert captured["quiet_mode"] is True
    assert captured["skip_memory"] is True
    # Blocked toolsets are filtered before the agent is built.
    assert captured["enabled_toolsets"] == ["web"]
    assert not (set(captured["enabled_toolsets"]) & BLOCKED_STATELESS_TOOLSETS)
    # The agent's own persistence (if any) lands in the ephemeral store.
    assert captured["session_db"] is manager._get_db()


# ---- local-confined preset ---------------------------------------------------


def test_apply_local_confined_merges_and_is_idempotent(tmp_path, monkeypatch):
    pytest.importorskip("hermes_cli.config")
    monkeypatch.setenv("XHERMES_HOME", str(tmp_path))

    from extend.task_relay.stateless import (
        DEFAULT_LOCAL_DENY_RULES,
        apply_local_confined,
    )
    from hermes_cli.config import load_config

    added = apply_local_confined(extra_deny_rules=["custom-cmd *"])
    assert added == len(DEFAULT_LOCAL_DENY_RULES) + 1

    deny = load_config().get("approvals", {}).get("deny") or []
    for rule in DEFAULT_LOCAL_DENY_RULES:
        assert rule in deny
    assert "custom-cmd *" in deny

    # Second run adds nothing.
    assert apply_local_confined(extra_deny_rules=["custom-cmd *"]) == 0


def test_apply_local_confined_preserves_user_rules(tmp_path, monkeypatch):
    pytest.importorskip("hermes_cli.config")
    monkeypatch.setenv("XHERMES_HOME", str(tmp_path / "home"))
    (tmp_path / "home").mkdir()
    (tmp_path / "home" / "config.yaml").write_text(
        "approvals:\n  deny:\n    - 'my-rule *'\n"
    )

    from extend.task_relay.stateless import (
        DEFAULT_LOCAL_DENY_RULES,
        apply_local_confined,
    )
    from hermes_cli.config import load_config

    apply_local_confined()
    deny = load_config().get("approvals", {}).get("deny") or []
    assert "my-rule *" in deny
    assert set(DEFAULT_LOCAL_DENY_RULES) <= set(deny)


def test_local_confined_deny_blocks_before_any_bypass(tmp_path, monkeypatch):
    approval = pytest.importorskip("tools.approval")
    pytest.importorskip("hermes_cli.config")
    monkeypatch.setenv("XHERMES_HOME", str(tmp_path))

    from extend.task_relay.stateless import apply_local_confined

    apply_local_confined()

    # Deny matches fire regardless of yolo / fail-open paths.
    assert approval._match_user_deny_rule("sudo rm -rf /tmp/x") == "sudo *"
    assert approval._match_user_deny_rule("cat ~/.ssh/id_rsa") == "*/.ssh/*"
    # Ordinary task commands stay allowed.
    assert approval._match_user_deny_rule("ls -la") is None
    assert approval._match_user_deny_rule("python3 train.py") is None


def test_rpc_parser_accepts_local_confined_flags():
    from extend.task_relay.acp_rpc_server import _build_arg_parser

    args = _build_arg_parser().parse_args(
        ["--local-confined", "--local-confined-extra-deny", "foo *,bar *"]
    )
    assert args.local_confined is True
    assert args.local_confined_extra_deny == "foo *,bar *"


def test_rpc_parser_defaults_to_uds():
    from extend.task_relay.acp_rpc_server import _build_arg_parser
    from extend.task_relay.constants import DEFAULT_ACP_RPC_SOCKET

    args = _build_arg_parser().parse_args([])
    assert args.http is False
    assert args.socket is None
    assert DEFAULT_ACP_RPC_SOCKET.endswith("relay/acp.sock")


def test_rpc_parser_accepts_http_flag():
    from extend.task_relay.acp_rpc_server import _build_arg_parser

    args = _build_arg_parser().parse_args(["--http", "--port", "9200"])
    assert args.http is True
    assert args.port == 9200
