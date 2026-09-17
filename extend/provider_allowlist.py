"""Single-provider gate for this fork.

Upstream XHermes ships ~34 inference providers. This deployment only supports
the INFA consumer session, so every provider surface (model pickers, provider
catalogs, ``xhermes auth add``) is filtered through the allowlist below rather
than by deleting upstream code — the fork stays mergeable with upstream.

Set ``XHERMES_PROVIDER_ALLOWLIST`` to a comma-separated slug list — or to
``*`` for every upstream provider — to widen the gate without a code change
(escape hatch for debugging, staged rollback, and the upstream test suite,
which asserts the full provider catalog). Unset or empty means INFA only.
"""

from __future__ import annotations

import os
from typing import Any, FrozenSet, Iterable

ALLOWLIST_ENV_VAR = "XHERMES_PROVIDER_ALLOWLIST"

# Env value that disables the gate entirely.
ALLOW_ALL = "*"

# Slugs allowed when the env override is absent.
DEFAULT_ALLOWED_PROVIDERS = ("infa",)

# Alias -> canonical slug, limited to the allowed providers. Aliases of denied
# providers need no entry: they canonicalize to themselves and miss the
# allowlist, which is the desired outcome.
_ALIASES = {
    "infa-oauth": "infa",
    "xhermes-infa": "infa",
}


def canonical_slug(name: Any) -> str:
    """Lowercase ``name`` and resolve known aliases to their canonical slug."""
    slug = str(name or "").strip().lower()
    return _ALIASES.get(slug, slug)


def allowed_providers() -> FrozenSet[str]:
    """Canonical slugs this install may use."""
    raw = os.environ.get(ALLOWLIST_ENV_VAR, "")
    names: Iterable[str] = (
        [part for part in raw.split(",") if part.strip()]
        if raw.strip()
        else DEFAULT_ALLOWED_PROVIDERS
    )
    return frozenset(canonical_slug(name) for name in names)


def is_provider_allowed(name: Any) -> bool:
    """True when ``name`` (slug or alias) resolves to an allowed provider.

    Unknown names — custom endpoints, ``custom:<label>`` pool keys, empty
    values — are denied, so new upstream providers are opt-in rather than
    silently exposed.
    """
    slug = canonical_slug(name)
    if not slug:
        return False
    allowed = allowed_providers()
    return ALLOW_ALL in allowed or slug in allowed


def denied_provider_message(name: Any) -> str:
    """Operator-facing explanation for a blocked provider."""
    allowed = ", ".join(sorted(allowed_providers())) or "(none)"
    return (
        f"Provider '{str(name or '').strip() or '(empty)'}' is disabled in this build. "
        f"Allowed: {allowed}. Set {ALLOWLIST_ENV_VAR} to override."
    )
