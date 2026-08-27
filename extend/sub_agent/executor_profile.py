"""Executor-side toolset whitelist (sandbox profile) for sub-agent executors.

Security context: once the platform is open, goals dispatched by external
tenants (planners) execute on compute nodes through the ACP sidecar. An
executor that exposes shell/browser tools to a remote goal is remote code
execution. Per the M2-W4 review decision (spec §12): the Worker executor
profile defaults to **no shell/browser**, mirroring the planner-side
least-privilege principle, with the toolset whitelist enforced at two layers.

Layer 1 (this module): :class:`ExecutorProfile` intersects the
task-requested toolsets with the node operator's whitelist before the ACP
session is created — this is tool *registration/filtering* enforcement
(``AIAgent(enabled_toolsets=...)``), not prompt-level guidance.

Layer 2 (:mod:`extend.sub_agent.stateless`): ``BLOCKED_STATELESS_TOOLSETS``
always drops toolsets that touch the local user's state (memory / skills /
session_search / ...), even if the operator whitelists them here.

The node operator chooses the trust level: the whitelist can be widened via
``--executor-toolsets`` / ``--executor-allow-extra`` (or the
``ACP_EXECUTOR_TOOLSETS`` / ``ACP_EXECUTOR_ALLOW_EXTRA`` env vars) on
``extend.sub_agent.acp_rpc_server``. Widening to include ``terminal``,
``code_execution``, ``browser`` or ``delegation`` hands remote tenants
corresponding capabilities — combine with ``--sandbox docker``.

Announce alignment: whatever the sidecar can actually run must equal what
the Worker announces upstream (``task-relay-worker --toolsets`` → daemon
announce). Use :func:`ExecutorProfile.announce_toolsets` (or
``python -m extend.sub_agent.executor_profile``) to generate the exact CSV
for the worker flag, and :func:`ExecutorProfile.validate_announce` to catch
drift. The sidecar also serves the manifest over RPC (``acp.toolsets``).
"""

from __future__ import annotations

import logging
import os
from dataclasses import dataclass, field
from typing import Iterable, List, Mapping

logger = logging.getLogger("sub_agent.executor_profile")

#: Toolsets granted to remote tasks by default: inference support, retrieval
#: and file read/write only. Shell/terminal/browser/system-control classes
#: are deliberately absent — see :data:`SHELL_CLASS_TOOLSETS`.
DEFAULT_EXECUTOR_TOOLSETS: List[str] = [
    "file",  # read_file / write_file / patch / search_files
    "web",   # web_search / web_extract
    "todo",  # task planning
]

#: Capability classes excluded from the default whitelist. Granting any of
#: these to an untrusted remote goal is equivalent to remote code execution
#: (or host control); they require an explicit operator opt-in.
SHELL_CLASS_TOOLSETS = frozenset(
    {
        "terminal",        # shell command execution
        "code_execution",  # Python execution via the terminal environment
        "browser",         # browser automation
        "computer_use",    # desktop mouse/keyboard control
        "delegation",      # subagent fan-out (escapes per-session sandbox)
        "homeassistant",   # smart-home device control
        "kanban",          # multi-agent coordination control plane
    }
)

ENV_EXECUTOR_TOOLSETS = "ACP_EXECUTOR_TOOLSETS"
ENV_EXECUTOR_ALLOW_EXTRA = "ACP_EXECUTOR_ALLOW_EXTRA"


def _split_csv(raw: str | None) -> List[str] | None:
    if not raw:
        return None
    return [part.strip() for part in raw.split(",") if part.strip()]


def _known_toolsets() -> set[str] | None:
    try:
        from toolsets import TOOLSETS

        return set(TOOLSETS)
    except Exception:
        logger.debug("toolsets registry unavailable; skipping name validation")
        return None


@dataclass(frozen=True)
class ExecutorProfile:
    """The node operator's toolset whitelist for remotely dispatched tasks.

    ``allowed`` is the complete policy: a task (or the stateless default)
    only ever receives toolsets in this list. The default
    (:data:`DEFAULT_EXECUTOR_TOOLSETS`) excludes every entry of
    :data:`SHELL_CLASS_TOOLSETS`; an operator who trusts their tenants may
    widen it explicitly.
    """

    allowed: tuple[str, ...] = field(default_factory=lambda: tuple(DEFAULT_EXECUTOR_TOOLSETS))

    @classmethod
    def build(
        cls,
        *,
        allowed: Iterable[str] | None = None,
        extra: Iterable[str] | None = None,
    ) -> "ExecutorProfile":
        """Build a profile, replacing or extending the default whitelist.

        Args:
            allowed: Full replacement whitelist. When omitted, the default
                whitelist is used as the base.
            extra: Toolsets added on top of the base whitelist (operator
                opt-in for e.g. ``terminal`` on a trusted node).
        """
        base = list(allowed) if allowed is not None else list(DEFAULT_EXECUTOR_TOOLSETS)
        merged: List[str] = []
        for name in list(base) + list(extra or []):
            if name and name not in merged:
                merged.append(name)

        known = _known_toolsets()
        if known is not None:
            for name in merged:
                if name not in known:
                    logger.warning("executor profile whitelists unknown toolset %r", name)
        risky = SHELL_CLASS_TOOLSETS & set(merged)
        if risky:
            logger.warning(
                "executor profile grants shell-class toolsets %s — remote "
                "tasks can execute commands/control the host; use --sandbox docker",
                sorted(risky),
            )
        return cls(allowed=tuple(merged))

    @classmethod
    def from_env(cls, env: Mapping[str, str] | None = None) -> "ExecutorProfile":
        """Build a profile from ``ACP_EXECUTOR_TOOLSETS`` / ``ACP_EXECUTOR_ALLOW_EXTRA``."""
        environ = os.environ if env is None else env
        return cls.build(
            allowed=_split_csv(environ.get(ENV_EXECUTOR_TOOLSETS)),
            extra=_split_csv(environ.get(ENV_EXECUTOR_ALLOW_EXTRA)),
        )

    def resolve(self, requested: List[str] | None) -> List[str]:
        """Intersect *requested* toolsets with the whitelist.

        An empty/None request falls back to the full whitelist (the task
        gets exactly what this node offers — nothing more). Anything outside
        the whitelist is dropped with a warning; the result may be empty,
        which the session layer must honor as "no tools", not "defaults".
        """
        names = list(requested) if requested else list(self.allowed)
        resolved: List[str] = []
        for name in names:
            if not name:
                continue
            if name not in self.allowed:
                logger.warning("executor profile dropped toolset %r (not whitelisted)", name)
                continue
            if name not in resolved:
                resolved.append(name)
        return resolved

    def announce_toolsets(self) -> List[str]:
        """The toolset list this node may announce upstream.

        Pass this verbatim to ``task-relay-worker --toolsets`` so the
        capabilities declared to the Hub/daemon match what the sidecar can
        actually execute.
        """
        return list(self.allowed)

    def validate_announce(self, announced: Iterable[str]) -> List[str]:
        """Return the announced toolsets this profile cannot serve.

        An empty result means the worker's announced capabilities are a
        subset of the sidecar whitelist (announced ⊆ allowed).
        """
        return sorted({name for name in announced if name not in self.allowed})


def main() -> int:
    """Print the CSV to hand to ``task-relay-worker --toolsets``."""
    profile = ExecutorProfile.from_env()
    print(",".join(profile.announce_toolsets()))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
