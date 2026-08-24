"""Per-task model binding tests (spec §13.4 S4).

Covers: local-runtime probe hit/miss, operator whitelist rejection, the
fail-fast ``model_unavailable`` path through the backend and the JSON-RPC
server, per-task session model override, and the default (unbound) path
staying untouched.
"""

from __future__ import annotations

import asyncio
import threading
from typing import Any

import pytest
from aiohttp import ClientSession, web

from extend.task_relay.acp_backend import AcpTaskBackend
from extend.task_relay.acp_rpc_server import create_acp_rpc_app
from extend.task_relay.local_runtime import (
    ERROR_MODEL_UNAVAILABLE,
    LocalRuntimeResolver,
    ModelBinding,
    ModelUnavailableError,
)
from extend.task_relay.model_sessions import (
    BoundModelSessionManager,
    BoundModelStatelessSessionManager,
)
from extend.task_relay.task_types import TaskRunPayload

LOCAL_URL = "http://127.0.0.1:8080/v1"
BINDING = ModelBinding(
    model="qwen2.5-1.5b", base_url=LOCAL_URL, api_key="no-key-required"
)


def _resolver(
    *,
    allowed: list[str] | None = None,
    served: list[str] | None = None,
    probe_error: Exception | None = None,
) -> LocalRuntimeResolver:
    probe_calls: list[str] = []

    async def probe(base_url: str, timeout: float) -> list[str]:
        probe_calls.append(base_url)
        if probe_error is not None:
            raise probe_error
        return list(served or [])

    resolver = LocalRuntimeResolver(
        base_url=LOCAL_URL,
        api_key="no-key-required",
        allowed_models=allowed,
        models_probe=probe,
    )
    resolver._test_probe_calls = probe_calls  # type: ignore[attr-defined]
    return resolver


def _run_payload(model: str | None = None) -> TaskRunPayload:
    return TaskRunPayload(
        task_id="t1",
        attempt=1,
        goal="test goal",
        params=None,
        context=None,
        toolsets=[],
        timeout_seconds=60,
        first_progress_seconds=None,
        trace_context=None,
        resume_from_checkpoint=None,
        model=model,
    )


class FakeAgent:
    def __init__(self) -> None:
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
    def __init__(self) -> None:
        self.session_id = "fake-session-1"
        self.agent = FakeAgent()
        self.history: list[dict[str, Any]] = []
        self.cancel_event = threading.Event()


class BindingFakeManager:
    """Fake session manager with model-binding support; records create calls."""

    supports_model_binding = True

    def __init__(self) -> None:
        self.create_kwargs: list[dict[str, Any]] = []

    def create_session(self, cwd: str = ".", **kwargs: Any) -> FakeSessionState:
        self.create_kwargs.append({"cwd": cwd, **kwargs})
        return FakeSessionState()

    def save_session(self, session_id: str) -> None:
        return None


class PlainFakeManager:
    """Fake session manager WITHOUT model-binding support."""

    def __init__(self) -> None:
        self.create_kwargs: list[dict[str, Any]] = []

    def create_session(self, cwd: str = ".", **kwargs: Any) -> FakeSessionState:
        self.create_kwargs.append({"cwd": cwd, **kwargs})
        return FakeSessionState()

    def save_session(self, session_id: str) -> None:
        return None


# ---- resolver ---------------------------------------------------------------


@pytest.mark.asyncio
async def test_resolve_probe_hit_returns_binding():
    resolver = _resolver(served=["qwen2.5-1.5b", "qwen3-8b"])
    binding = await resolver.resolve("qwen2.5-1.5b")
    assert binding.model == "qwen2.5-1.5b"
    assert binding.base_url == LOCAL_URL
    assert binding.api_key == "no-key-required"
    assert binding.provider == "custom"
    assert binding.api_mode == "chat_completions"


@pytest.mark.asyncio
async def test_resolve_probe_miss_raises_model_unavailable():
    resolver = _resolver(served=["qwen2.5-1.5b"])
    with pytest.raises(ModelUnavailableError, match="not served by local runtime"):
        await resolver.resolve("qwen3-8b")


@pytest.mark.asyncio
async def test_resolve_runtime_unreachable_raises_model_unavailable():
    resolver = _resolver(probe_error=ConnectionRefusedError("refused"))
    with pytest.raises(ModelUnavailableError, match="unreachable"):
        await resolver.resolve("qwen2.5-1.5b")


@pytest.mark.asyncio
async def test_resolve_whitelist_rejects_before_probing():
    resolver = _resolver(allowed=["qwen2.5-1.5b"], served=["qwen2.5-1.5b", "qwen3-8b"])
    with pytest.raises(ModelUnavailableError, match="allowed list"):
        await resolver.resolve("qwen3-8b")
    assert resolver._test_probe_calls == []  # fail-fast: never probed


@pytest.mark.asyncio
async def test_resolve_whitelist_allows_and_probes():
    resolver = _resolver(allowed=["qwen2.5-1.5b"], served=["qwen2.5-1.5b"])
    binding = await resolver.resolve("qwen2.5-1.5b")
    assert binding.model == "qwen2.5-1.5b"
    assert resolver._test_probe_calls == [LOCAL_URL]


def test_resolver_from_env():
    resolver = LocalRuntimeResolver.from_env({
        "ACP_LOCAL_RUNTIME_BASE_URL": "http://10.0.0.2:9999/v1/",
        "ACP_LOCAL_RUNTIME_API_KEY": "secret",
        "ACP_ALLOWED_MODELS": "a, b ,,",
    })
    assert resolver.base_url == "http://10.0.0.2:9999/v1"

    default = LocalRuntimeResolver.from_env({})
    assert default.base_url == "http://127.0.0.1:8080/v1"


# ---- backend ----------------------------------------------------------------


@pytest.mark.asyncio
async def test_backend_passes_binding_to_session_creation():
    manager = BindingFakeManager()
    backend = AcpTaskBackend(
        session_manager=manager,
        progress_interval_seconds=0.0,
        local_runtime=_resolver(served=["qwen2.5-1.5b"]),
    )
    result = await backend.run(
        _run_payload(model="qwen2.5-1.5b"), _noop, _noop_cp, asyncio.Event()
    )
    assert result.status == "completed"
    binding = manager.create_kwargs[0].get("binding")
    assert isinstance(binding, ModelBinding)
    assert binding.model == "qwen2.5-1.5b"
    assert binding.base_url == LOCAL_URL


@pytest.mark.asyncio
async def test_backend_probe_miss_fails_fast_without_session():
    manager = BindingFakeManager()
    backend = AcpTaskBackend(
        session_manager=manager,
        progress_interval_seconds=0.0,
        local_runtime=_resolver(served=["qwen2.5-1.5b"]),
    )
    result = await backend.run(
        _run_payload(model="qwen3-8b"), _noop, _noop_cp, asyncio.Event()
    )
    assert result.status == "failed"
    assert result.error_code == ERROR_MODEL_UNAVAILABLE
    assert "qwen3-8b" in (result.error or "")
    assert manager.create_kwargs == []  # no session was created


@pytest.mark.asyncio
async def test_backend_whitelist_reject_fails_fast():
    manager = BindingFakeManager()
    backend = AcpTaskBackend(
        session_manager=manager,
        progress_interval_seconds=0.0,
        local_runtime=_resolver(allowed=["qwen2.5-1.5b"], served=["qwen3-8b"]),
    )
    result = await backend.run(
        _run_payload(model="qwen3-8b"), _noop, _noop_cp, asyncio.Event()
    )
    assert result.status == "failed"
    assert result.error_code == ERROR_MODEL_UNAVAILABLE
    assert manager.create_kwargs == []


@pytest.mark.asyncio
async def test_backend_unbound_task_uses_default_path():
    """No model binding: no resolver interaction, no binding kwarg."""

    class ExplodingResolver(LocalRuntimeResolver):
        async def resolve(self, model: str) -> ModelBinding:
            raise AssertionError("resolve must not be called for unbound tasks")

    manager = BindingFakeManager()
    backend = AcpTaskBackend(
        session_manager=manager,
        progress_interval_seconds=0.0,
        local_runtime=ExplodingResolver(),
    )
    result = await backend.run(
        _run_payload(model=None), _noop, _noop_cp, asyncio.Event()
    )
    assert result.status == "completed"
    assert "binding" not in manager.create_kwargs[0]


@pytest.mark.asyncio
async def test_backend_binding_requires_manager_support():
    """A manager that cannot apply the override must not silently run."""
    manager = PlainFakeManager()
    backend = AcpTaskBackend(
        session_manager=manager,
        progress_interval_seconds=0.0,
        local_runtime=_resolver(served=["qwen2.5-1.5b"]),
    )
    result = await backend.run(
        _run_payload(model="qwen2.5-1.5b"), _noop, _noop_cp, asyncio.Event()
    )
    assert result.status == "failed"
    assert result.error_code == ERROR_MODEL_UNAVAILABLE
    assert manager.create_kwargs == []


# ---- session managers --------------------------------------------------------


def test_bound_stateless_agent_kwargs():
    manager = BoundModelStatelessSessionManager(agent_factory=FakeAgent)
    kwargs = manager._bound_agent_kwargs(BINDING, session_id="s1")
    assert kwargs["model"] == "qwen2.5-1.5b"
    assert kwargs["provider"] == "custom"
    assert kwargs["api_mode"] == "chat_completions"
    assert kwargs["base_url"] == LOCAL_URL
    assert kwargs["api_key"] == "no-key-required"
    assert kwargs["skip_memory"] is True
    manager.discard()


def test_bound_stateful_agent_kwargs():
    manager = BoundModelSessionManager(agent_factory=FakeAgent, db=object())
    kwargs = manager._bound_agent_kwargs(BINDING, session_id="s1")
    assert kwargs["model"] == "qwen2.5-1.5b"
    assert kwargs["provider"] == "custom"
    assert kwargs["base_url"] == LOCAL_URL


def test_bound_managers_create_session_with_binding():
    """The binding handoff through create_session must not deadlock."""
    for manager in (
        BoundModelStatelessSessionManager(agent_factory=FakeAgent),
        BoundModelSessionManager(agent_factory=FakeAgent, db=object()),
    ):
        state = manager.create_session(cwd=".", binding=BINDING)
        assert state.agent is not None
        assert manager._pending_binding is None
        if isinstance(manager, BoundModelStatelessSessionManager):
            manager.discard()


# ---- RPC surface -------------------------------------------------------------


async def _noop(_summary: str) -> None:
    return None


async def _noop_cp(*args: Any, **kwargs: Any) -> None:
    return None


@pytest.mark.asyncio
async def test_rpc_model_unavailable_surfaces_error_code():
    backend = AcpTaskBackend(
        session_manager=BindingFakeManager(),
        progress_interval_seconds=0.0,
        local_runtime=_resolver(served=["qwen2.5-1.5b"]),
    )
    app = create_acp_rpc_app(backend=backend)
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", 0)
    await site.start()
    port = site._server.sockets[0].getsockname()[1]
    url = f"http://127.0.0.1:{port}/rpc"
    try:
        body = {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "acp.run",
            "params": {
                "run_id": "run-mu",
                "task_id": "t1",
                "goal": "g",
                "timeout_seconds": 30,
                "model": "qwen3-8b",
            },
        }
        async with ClientSession() as session:
            async with session.post(url, json=body) as resp:
                assert resp.status == 200
                payload = await resp.json()
        result = payload["result"]
        assert result["status"] == "failed"
        assert result["error_code"] == "model_unavailable"
        assert "qwen3-8b" in result["error"]
    finally:
        await runner.cleanup()
