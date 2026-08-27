"""Stateless ACP session management for sub-agent executors.

A sub-agent worker executes *remote* subtasks on a machine that may also belong
to a local user. The default ACP session path shares the user's data plane
(``~/.xhermes/state.db``, global ``memories/``, ``skills/``), which lets a
remote task read local history and mutate local memories/skills, and leaves
a permanent transcript behind.

``StatelessSessionManager`` removes that coupling without touching upstream
code:

- Session records go to a throwaway SQLite under an ephemeral ``state_root``
  (a fresh ``mkdtemp`` by default), never the user's ``state.db``.
- Agents are built with ``skip_memory=True`` and a narrowed toolset list
  that excludes ``memory`` / ``skills`` / ``session_search`` / ``cronjob`` —
  so a remote task can neither read nor mutate the local user's memories,
  profile, skills, or other sessions. The user's configured MCP servers are
  also not attached (they are local user config).
- ``discard()`` removes the whole state directory; combined with the
  per-task temp workdir managed by the backend, a finished task leaves no
  durable trace on the node.

Optionally, tasks can also be executed inside a **Docker sandbox**
(``sandbox="docker"``): every session gets its own disposable container
(per-session image override → isolated container, tmpfs home, destroyed on
session removal), with configurable network (default: none), CPU and memory
limits. The sandbox boundary covers the terminal *and* the file/code tools
(``read_file``/``write_file``/``patch``/``search_files``/``execute_code``
share the terminal environment), and container-backed commands skip the
interactive approval layer entirely — the sandbox is the security boundary.
Call :func:`apply_sandbox_env` once before the first agent is built.

Process-level isolation (running the sidecar with a dedicated
``XHERMES_HOME``) is still recommended on top of this module; enable the
mode with ``python -m extend.sub_agent.acp_rpc_server --stateless``
(plus ``--sandbox docker`` for containerized execution).

For **trusted internal tasks** on nodes without Docker, ``--local-confined``
is the lightweight alternative: local execution + stateless sessions +
:func:`apply_local_confined`, which installs :data:`DEFAULT_LOCAL_DENY_RULES`
into the sidecar's ``approvals.deny``. Guardrails against accidents, not a
security boundary.
"""

from __future__ import annotations

import logging
import os
import shutil
import tempfile
import threading
from pathlib import Path
from typing import Any, List

from acp_adapter.session import SessionManager, _acp_stderr_print, _register_task_cwd

logger = logging.getLogger("sub_agent.stateless")

#: Working directory used for sessions running inside a sandbox container.
#: The container is per-session and disposable, so a single fixed path is
#: enough — there is no host workdir to clean up.
SANDBOX_CONTAINER_CWD = "/workspace"

#: Mirrors the upstream terminal-tool default image
#: (``tools/terminal_tool.py`` ``default_image``); used only when neither
#: the manager config nor ``TERMINAL_DOCKER_IMAGE`` provides one.
DEFAULT_SANDBOX_IMAGE = "nikolaik/python-nodejs:python3.11-nodejs20"

#: Default ``approvals.deny`` glob preset for local-confined mode.
#:
#: These rules fire before every approval bypass (yolo, mode=off, the
#: non-interactive fail-open path), so they hold for unattended sub-agent
#: execution. They are guardrails against accidents and obviously-destructive
#: commands — string matching is inherently bypassable (``python -c``,
#: encoded payloads, write-then-run), so this is NOT a security boundary.
#: Use for trusted internal tasks only; untrusted tasks belong in
#: ``--sandbox docker``.
DEFAULT_LOCAL_DENY_RULES: List[str] = [
    # Privilege escalation
    "sudo *",
    "doas *",
    # Destructive filesystem ops beyond the task workdir
    "rm -rf /*",
    "rm -rf ~*",
    "rm -rf $HOME*",
    "chmod -R * /",
    "chown -R * /",
    # Pipe-to-shell installers
    "curl * | sh*",
    "curl * | bash*",
    "curl *|sh*",
    "curl *|bash*",
    "wget * | sh*",
    "wget * | bash*",
    "wget *|sh*",
    "wget *|bash*",
    # Raw devices / partitioning
    "dd *of=/dev/*",
    "mkfs*",
    # Power / service / job scheduling
    "shutdown*",
    "reboot*",
    "halt*",
    "poweroff*",
    "systemctl *",
    "launchctl *",
    "crontab *",
    # Local user secrets and hermes state (defense in depth — stateless
    # mode already keeps the agent's own tools away from these)
    "*/.ssh/*",
    "*/.xhermes*",
    "*/.aws/*",
    "*/.gnupg/*",
]


def apply_local_confined(*, extra_deny_rules: List[str] | None = None) -> int:
    """Install the local-confined approval policy into the sidecar's config.

    Merges :data:`DEFAULT_LOCAL_DENY_RULES` (plus *extra_deny_rules*) into
    ``approvals.deny`` of the sidecar's own ``config.yaml``. Idempotent;
    existing user rules are preserved. Returns the number of rules added.

    ``config.yaml`` is the upstream policy surface: a deny match blocks the
    command unconditionally — before yolo, ``approvals.mode=off``, and the
    non-interactive auto-approve path.
    """
    from hermes_cli.config import load_config, save_config

    config = load_config()
    approvals = config.get("approvals")
    if not isinstance(approvals, dict):
        approvals = {}

    existing = approvals.get("deny") or []
    merged = [r for r in existing if isinstance(r, str) and r.strip()]
    seen = {r.strip().lower() for r in merged}

    added = 0
    for rule in list(DEFAULT_LOCAL_DENY_RULES) + list(extra_deny_rules or []):
        key = rule.strip().lower()
        if key and key not in seen:
            merged.append(rule)
            seen.add(key)
            added += 1

    approvals["deny"] = merged
    config["approvals"] = approvals
    save_config(config, merge_existing=True)
    logger.info(
        "local-confined: %d approval deny rules active (%d newly added)",
        len(merged),
        added,
    )
    return added


def apply_sandbox_env(
    *,
    sandbox: str,
    image: str | None = None,
    network: bool = False,
    cpu: float | None = None,
    memory_mb: int | None = None,
) -> None:
    """Configure the process-level terminal environment for sandboxed runs.

    Must be called once before the first agent (and therefore the first
    terminal environment) is created — the backend choice is process-global.

    Caveat: an explicit ``terminal:`` section in the sidecar's
    ``config.yaml`` overrides these env vars (upstream config bridge), so
    keep that section absent or consistent.
    """
    if sandbox != "docker":
        raise ValueError(f"unsupported sandbox backend: {sandbox!r}")
    os.environ["TERMINAL_ENV"] = "docker"
    os.environ["TERMINAL_DOCKER_IMAGE"] = (
        image or os.environ.get("TERMINAL_DOCKER_IMAGE") or DEFAULT_SANDBOX_IMAGE
    )
    # Untrusted remote tasks get no container network by default; the
    # agent's own LLM/web-search calls are made by the host process and
    # are unaffected.
    os.environ["TERMINAL_DOCKER_NETWORK"] = "true" if network else "false"
    # Disposable containers: tmpfs home/workspace, no cross-process reuse.
    os.environ["TERMINAL_CONTAINER_PERSISTENT"] = "false"
    os.environ["TERMINAL_DOCKER_PERSIST_ACROSS_PROCESSES"] = "false"
    if cpu is not None:
        os.environ["TERMINAL_CONTAINER_CPU"] = str(cpu)
    if memory_mb is not None:
        os.environ["TERMINAL_CONTAINER_MEMORY"] = str(memory_mb)

# Toolsets granted to remote tasks when the Hub does not request specific
# ones. Deliberately excludes anything that reads or writes local user
# state; capability can be widened via configuration, not by default.
DEFAULT_STATELESS_TOOLSETS: List[str] = [
    "terminal",
    "file",
    "web",
    "code_execution",
    "todo",
]

# Toolsets that must never be granted to a remote sub-agent task, even when
# explicitly requested: they expose or mutate the node's local user state,
# or make no sense without an interactive user.
BLOCKED_STATELESS_TOOLSETS = frozenset(
    {
        "memory",          # reads/writes global MEMORY.md / USER.md
        "skills",          # reads/writes global skills directory
        "session_search",  # reads the local user's session history
        "cronjob",         # persists scheduled jobs beyond the task
        "clarify",         # requires an interactive local user
        "messaging",       # sends messages as the local user
        "project",         # desktop GUI workspace switching
    }
)


def resolve_stateless_toolsets(requested: List[str] | None) -> List[str]:
    """Resolve the effective toolset list for a stateless task.

    Falls back to :data:`DEFAULT_STATELESS_TOOLSETS` when *requested* is
    ``None``, drops blocked and unknown toolsets, and preserves order. An
    explicit empty list means "no toolsets" and stays empty.
    """
    names = list(requested) if requested is not None else list(DEFAULT_STATELESS_TOOLSETS)

    known: set[str] | None = None
    try:
        from toolsets import TOOLSETS

        known = set(TOOLSETS)
    except Exception:
        logger.debug("toolsets registry unavailable; skipping name validation")

    resolved: List[str] = []
    for name in names:
        if not name:
            continue
        if name in BLOCKED_STATELESS_TOOLSETS:
            logger.warning("stateless task dropped blocked toolset %r", name)
            continue
        if known is not None and name not in known:
            logger.warning("stateless task dropped unknown toolset %r", name)
            continue
        if name not in resolved:
            resolved.append(name)
    return resolved


class StatelessSessionManager(SessionManager):
    """``SessionManager`` variant whose sessions live in an ephemeral store.

    Args:
        state_root: Directory holding the throwaway ``state.db``. When
            omitted, a fresh temporary directory is created and removed by
            :meth:`discard`.
        toolsets: Default toolsets for sessions; per-call overrides are
            passed via :meth:`create_session`.
        sandbox: Optional execution sandbox backend. ``"docker"`` gives every
            session its own disposable container (see :func:`apply_sandbox_env`,
            which must be called before the first session).
        sandbox_image: Docker image for sandboxed sessions. Defaults to
            ``TERMINAL_DOCKER_IMAGE`` or :data:`DEFAULT_SANDBOX_IMAGE`.
        agent_factory: Optional AIAgent factory (tests).

    Note: enabling the ``delegation`` toolset weakens per-session container
    isolation — subagents collapse onto the shared ``"default"`` container
    (upstream behavior). The default stateless toolsets exclude it.
    """

    def __init__(
        self,
        *,
        state_root: str | Path | None = None,
        toolsets: List[str] | None = None,
        sandbox: str | None = None,
        sandbox_image: str | None = None,
        agent_factory: Any | None = None,
    ) -> None:
        if sandbox is not None and sandbox != "docker":
            raise ValueError(f"unsupported sandbox backend: {sandbox!r}")
        self._sandbox = sandbox
        self._sandbox_image = (
            sandbox_image
            or os.environ.get("TERMINAL_DOCKER_IMAGE")
            or DEFAULT_SANDBOX_IMAGE
        )
        self._owns_state_root = state_root is None
        self._state_root = (
            Path(tempfile.mkdtemp(prefix="xhermes-sub-agent-state-"))
            if state_root is None
            else Path(state_root)
        )
        self._state_root.mkdir(parents=True, exist_ok=True)
        self._toolsets = resolve_stateless_toolsets(toolsets)
        self._create_lock = threading.Lock()
        self._pending_toolsets: List[str] | None = None

        db = None
        try:
            from hermes_state import SessionDB

            db = SessionDB(db_path=self._state_root / "state.db")
        except Exception:
            logger.debug(
                "SessionDB unavailable; stateless sessions stay memory-only",
                exc_info=True,
            )
        super().__init__(agent_factory=agent_factory, db=db)

    @property
    def state_root(self) -> Path:
        return self._state_root

    def create_session(self, cwd: str = ".", toolsets: List[str] | None = None):
        """Create a session, optionally narrowing toolsets for this task.

        ``create_session`` is synchronous and serialized here, so handing the
        per-task toolsets to ``_make_agent`` through an instance attribute is
        race-free.

        An explicit empty list is honored as "no toolsets" (executor profile
        denied everything); only ``None`` falls back to the manager defaults.
        """
        with self._create_lock:
            self._pending_toolsets = (
                resolve_stateless_toolsets(toolsets) if toolsets is not None else None
            )
            try:
                state = super().create_session(cwd=cwd)
            finally:
                self._pending_toolsets = None
        if self._sandbox == "docker":
            self._register_sandbox_overrides(state.session_id, state.cwd)
        return state

    def remove_session(self, session_id: str) -> bool:
        """Remove the session and destroy its sandbox container, if any."""
        existed = super().remove_session(session_id)
        if self._sandbox == "docker":
            try:
                from tools.terminal_tool import cleanup_vm

                cleanup_vm(session_id, force_remove=True)
            except Exception:
                logger.debug(
                    "sandbox cleanup failed for session %s", session_id, exc_info=True
                )
        return existed

    def _register_sandbox_overrides(self, session_id: str, cwd: str) -> None:
        """Pin the session to its own container.

        Registering a ``docker_image`` override is the upstream isolation
        signal: ``_resolve_container_task_id`` then keeps this session's
        task_id instead of collapsing onto the shared ``"default"``
        container. Must run *after* ``super().create_session`` — the upstream
        cwd registration replaces the override dict and would drop the image
        key.
        """
        try:
            from tools.terminal_tool import register_task_env_overrides

            register_task_env_overrides(
                session_id, {"docker_image": self._sandbox_image, "cwd": cwd}
            )
        except Exception:
            logger.debug(
                "failed to register sandbox overrides for session %s",
                session_id,
                exc_info=True,
            )

    def discard(self) -> None:
        """Drop all sessions and remove the ephemeral state directory."""
        try:
            self.cleanup()
        except Exception:
            logger.debug("stateless session cleanup failed", exc_info=True)
        if self._owns_state_root:
            shutil.rmtree(self._state_root, ignore_errors=True)

    # ---- internal -----------------------------------------------------------

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
        """Build an AIAgent without access to local user state.

        Mirrors ``SessionManager._make_agent`` except: ``skip_memory=True``,
        narrowed toolsets, the ephemeral session DB, and no MCP server
        toolsets (they come from the local user's config).
        """
        if self._agent_factory is not None:
            return self._agent_factory()

        from run_agent import AIAgent
        from hermes_cli.config import load_config
        from hermes_cli.runtime_provider import resolve_runtime_provider

        config = load_config()
        model_cfg = config.get("model")
        default_model = ""
        config_provider = None
        if isinstance(model_cfg, dict):
            default_model = str(model_cfg.get("default") or default_model)
            config_provider = model_cfg.get("provider")
        elif isinstance(model_cfg, str) and model_cfg.strip():
            default_model = model_cfg.strip()

        kwargs = {
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
            "model": model or default_model,
        }

        try:
            runtime = resolve_runtime_provider(requested=requested_provider or config_provider)
            kwargs.update(
                {
                    "provider": runtime.get("provider"),
                    "api_mode": api_mode or runtime.get("api_mode"),
                    "base_url": base_url or runtime.get("base_url"),
                    "api_key": runtime.get("api_key"),
                    "command": runtime.get("command"),
                    "args": list(runtime.get("args") or []),
                }
            )
        except Exception:
            logger.debug("stateless session falling back to default provider resolution", exc_info=True)

        _register_task_cwd(session_id, cwd)

        agent = AIAgent(**kwargs)
        agent.session_cwd = cwd
        agent._print_fn = _acp_stderr_print
        return agent
