"""Session managers that honor a per-task model binding (spec §13.4 S4).

Both variants wrap the upstream managers without modifying them: a task's
resolved :class:`~extend.task_relay.local_runtime.ModelBinding` is handed to
``create_session(binding=...)`` and consumed by ``_make_agent`` through a
pending-attribute guarded by the create lock — the same race-free pattern
``StatelessSessionManager`` uses for per-task toolsets.

A bound session is built with explicit ``model`` / ``provider="custom"`` /
``api_mode="chat_completions"`` / ``base_url`` / ``api_key`` kwargs pointing
at the node-local runtime; the upstream provider-resolution machinery (env
keys, credential pools, cloud fallbacks) is deliberately bypassed so a bound
task can never silently run on a cloud provider.

When no binding is pending, construction delegates to the parent unchanged.
"""

from __future__ import annotations

import threading
from typing import Any, List

from acp_adapter.session import (
    SessionManager,
    _acp_stderr_print,
    _expand_acp_enabled_toolsets,
    _register_task_cwd,
)

from extend.task_relay.local_runtime import ModelBinding
from extend.task_relay.stateless import (
    StatelessSessionManager,
    resolve_stateless_toolsets,
)


def _finalize_agent(agent: Any, cwd: str) -> Any:
    agent.session_cwd = cwd
    agent._print_fn = _acp_stderr_print
    return agent


class BoundModelStatelessSessionManager(StatelessSessionManager):
    """Stateless manager whose sessions can be pinned to a local model.

    ``create_session`` additionally accepts ``binding=``. The parent's
    ``_create_lock`` serializes creation, so the pending binding rides the
    same race-free handoff as ``_pending_toolsets``.
    """

    supports_model_binding = True

    def __init__(self, *args: Any, **kwargs: Any) -> None:
        super().__init__(*args, **kwargs)
        self._pending_binding: ModelBinding | None = None

    def create_session(
        self,
        cwd: str = ".",
        toolsets: List[str] | None = None,
        binding: ModelBinding | None = None,
    ):
        # Replicates StatelessSessionManager.create_session rather than
        # delegating: the parent's body re-acquires ``_create_lock`` (a plain
        # threading.Lock), which would deadlock.
        with self._create_lock:
            self._pending_toolsets = (
                resolve_stateless_toolsets(toolsets) if toolsets is not None else None
            )
            self._pending_binding = binding
            try:
                state = SessionManager.create_session(self, cwd=cwd)
            finally:
                self._pending_toolsets = None
                self._pending_binding = None
        if self._sandbox == "docker":
            self._register_sandbox_overrides(state.session_id, state.cwd)
        return state

    def _bound_agent_kwargs(
        self, binding: ModelBinding, *, session_id: str
    ) -> dict[str, Any]:
        """AIAgent kwargs for a bound session (split out for testability)."""
        return {
            "platform": "acp",
            "enabled_toolsets": (
                self._pending_toolsets
                if self._pending_toolsets is not None
                else self._toolsets
            ),
            "quiet_mode": True,
            "skip_memory": True,
            "session_id": session_id,
            "session_db": self._get_db(),
            "model": binding.model,
            "provider": binding.provider,
            "api_mode": binding.api_mode,
            "base_url": binding.base_url,
            "api_key": binding.api_key,
        }

    def _make_agent(
        self,
        *,
        session_id: str,
        cwd: str,
        model: str | None = None,
        requested_provider: str | None = None,
        base_url: str | None = None,
        api_mode: str | None = None,
    ):
        binding = self._pending_binding
        if binding is None:
            return super()._make_agent(
                session_id=session_id,
                cwd=cwd,
                model=model,
                requested_provider=requested_provider,
                base_url=base_url,
                api_mode=api_mode,
            )
        if self._agent_factory is not None:
            return self._agent_factory()
        from run_agent import AIAgent

        _register_task_cwd(session_id, cwd)
        agent = AIAgent(**self._bound_agent_kwargs(binding, session_id=session_id))
        return _finalize_agent(agent, cwd)


class BoundModelSessionManager(SessionManager):
    """Plain (stateful) manager variant honoring a per-task model binding.

    The base ``SessionManager`` has no create lock, so this subclass adds one
    for the pending-binding handoff.
    """

    supports_model_binding = True

    def __init__(self, *args: Any, **kwargs: Any) -> None:
        super().__init__(*args, **kwargs)
        self._binding_lock = threading.Lock()
        self._pending_binding: ModelBinding | None = None

    def create_session(self, cwd: str = ".", binding: ModelBinding | None = None):
        with self._binding_lock:
            self._pending_binding = binding
            try:
                return super().create_session(cwd=cwd)
            finally:
                self._pending_binding = None

    def _bound_agent_kwargs(
        self, binding: ModelBinding, *, session_id: str
    ) -> dict[str, Any]:
        """AIAgent kwargs for a bound session (split out for testability).

        Unlike the base ``_make_agent``, a bound session does not attach the
        local user's configured MCP servers and does not consult the
        credential pool: the binding pins model + endpoint explicitly.
        """
        return {
            "platform": "acp",
            "enabled_toolsets": _expand_acp_enabled_toolsets(["xhermes-acp"]),
            "quiet_mode": True,
            "session_id": session_id,
            "session_db": self._get_db(),
            "model": binding.model,
            "provider": binding.provider,
            "api_mode": binding.api_mode,
            "base_url": binding.base_url,
            "api_key": binding.api_key,
        }

    def _make_agent(
        self,
        *,
        session_id: str,
        cwd: str,
        model: str | None = None,
        requested_provider: str | None = None,
        base_url: str | None = None,
        api_mode: str | None = None,
    ):
        binding = self._pending_binding
        if binding is None:
            return super()._make_agent(
                session_id=session_id,
                cwd=cwd,
                model=model,
                requested_provider=requested_provider,
                base_url=base_url,
                api_mode=api_mode,
            )
        if self._agent_factory is not None:
            return self._agent_factory()
        from run_agent import AIAgent

        _register_task_cwd(session_id, cwd)
        agent = AIAgent(**self._bound_agent_kwargs(binding, session_id=session_id))
        return _finalize_agent(agent, cwd)
