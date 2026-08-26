"""Local-runtime-first model binding for relay tasks (spec §13.4 S4).

A task may carry a model binding (TaskSpec ``model`` field /
``params["model"]``, forwarded by the worker as ``acp.run``'s ``model``
param). When bound, the task must run against the **node-local** OpenAI
compatible runtime (vLLM / llama.cpp / …):

- The requested model is checked against the operator whitelist
  (``ACP_ALLOWED_MODELS``, comma-separated; unset = no static whitelist) and
  then probed against the local runtime's ``GET /models`` listing.
- A model the local runtime does not serve (or an unreachable runtime) is a
  **fail-fast**: :class:`ModelUnavailableError` → the backend settles the
  task ``failed`` with ``error_code="model_unavailable"`` so the Hub can
  rotate candidates. No waiting, no silent model swap.
- Platform-side fallback (running the bound model on a cloud provider with
  tenant credentials on the node) is intentionally NOT implemented — the
  credential security model is undecided (spec §13.4 S4).

Configuration is environment-only:

- ``ACP_LOCAL_RUNTIME_BASE_URL`` — OpenAI-compatible base URL of the local
  runtime (default ``http://127.0.0.1:8080/v1``).
- ``ACP_LOCAL_RUNTIME_API_KEY`` — bearer token for the local runtime
  (default ``"no-key-required"``; most local servers ignore it).
- ``ACP_ALLOWED_MODELS`` — optional comma-separated operator whitelist.
"""

from __future__ import annotations

import logging
import os
from dataclasses import dataclass
from typing import Any, Awaitable, Callable

logger = logging.getLogger("task_relay.worker.local_runtime")

ENV_LOCAL_BASE_URL = "ACP_LOCAL_RUNTIME_BASE_URL"
ENV_LOCAL_API_KEY = "ACP_LOCAL_RUNTIME_API_KEY"
ENV_ALLOWED_MODELS = "ACP_ALLOWED_MODELS"

DEFAULT_LOCAL_BASE_URL = "http://127.0.0.1:8080/v1"
DEFAULT_LOCAL_API_KEY = "no-key-required"

#: Structured failure code reported to the Hub when the bound model cannot
#: be served locally. Surfaced as ``error_code`` on the ``acp.run`` result
#: and prefixed into the worker's terminal ``error`` field.
ERROR_MODEL_UNAVAILABLE = "model_unavailable"

#: Seconds to wait for the local runtime's ``GET /models`` probe. Short on
#: purpose: a local endpoint answers in milliseconds or is down.
DEFAULT_PROBE_TIMEOUT_SECONDS = 2.0


class ModelUnavailableError(Exception):
    """The requested model cannot be served by the local runtime."""


@dataclass(frozen=True)
class ModelBinding:
    """Resolved per-task provider override pointing at the local runtime."""

    model: str
    base_url: str
    api_key: str
    provider: str = "custom"
    api_mode: str = "chat_completions"


#: Probe signature: returns the model ids the local runtime currently
#: serves. Injected in tests; the default hits ``GET {base_url}/models``.
ModelsProbe = Callable[[str, float, str], Awaitable[list[str]]]


async def _default_models_probe(
    base_url: str, timeout: float, api_key: str = ""
) -> list[str]:
    """List model ids from an OpenAI-compatible ``GET /models``."""
    import aiohttp

    url = base_url.rstrip("/") + "/models"
    headers = {}
    if api_key:
        headers["Authorization"] = f"Bearer {api_key}"
    async with aiohttp.ClientSession() as session:
        async with session.get(
            url, headers=headers, timeout=aiohttp.ClientTimeout(total=timeout)
        ) as resp:
            resp.raise_for_status()
            body: dict[str, Any] = await resp.json()
    data = body.get("data") or []
    return [
        str(item["id"]) for item in data if isinstance(item, dict) and item.get("id")
    ]


class LocalRuntimeResolver:
    """Validates a task's model binding against the node-local runtime.

    Args:
        base_url: OpenAI-compatible base URL of the local runtime.
        api_key: Bearer token handed to the bound session.
        allowed_models: Optional operator whitelist. ``None`` means the
            runtime probe is the only gate; an empty list allows nothing.
        probe_timeout_seconds: Bound on the ``GET /models`` probe.
        models_probe: Override for the ``GET /models`` call (tests).
    """

    def __init__(
        self,
        *,
        base_url: str = DEFAULT_LOCAL_BASE_URL,
        api_key: str = DEFAULT_LOCAL_API_KEY,
        allowed_models: list[str] | None = None,
        probe_timeout_seconds: float = DEFAULT_PROBE_TIMEOUT_SECONDS,
        models_probe: ModelsProbe | None = None,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._api_key = api_key
        self._allowed_models = (
            set(allowed_models) if allowed_models is not None else None
        )
        self._probe_timeout = probe_timeout_seconds
        self._probe = models_probe or _default_models_probe

    @classmethod
    def from_env(cls, env: dict[str, str] | None = None) -> "LocalRuntimeResolver":
        """Build a resolver from ``ACP_LOCAL_RUNTIME_*`` / ``ACP_ALLOWED_MODELS``."""
        env = os.environ if env is None else env
        allowed_raw = (env.get(ENV_ALLOWED_MODELS) or "").strip()
        allowed = [m.strip() for m in allowed_raw.split(",") if m.strip()] or None
        return cls(
            base_url=(env.get(ENV_LOCAL_BASE_URL) or "").strip()
            or DEFAULT_LOCAL_BASE_URL,
            api_key=(env.get(ENV_LOCAL_API_KEY) or "").strip() or DEFAULT_LOCAL_API_KEY,
            allowed_models=allowed,
        )

    @property
    def base_url(self) -> str:
        return self._base_url

    async def resolve(self, model: str) -> ModelBinding:
        """Return the local binding for *model* or raise ModelUnavailableError."""
        model = (model or "").strip()
        if not model:
            raise ModelUnavailableError("empty model binding")
        if self._allowed_models is not None and model not in self._allowed_models:
            raise ModelUnavailableError(
                f"model {model!r} is not in the node operator's allowed list"
            )
        try:
            served = await self._probe(
                self._base_url, self._probe_timeout, self._api_key
            )
        except Exception as exc:
            raise ModelUnavailableError(
                f"local runtime at {self._base_url} unreachable: {exc}"
            ) from exc
        if model not in served:
            raise ModelUnavailableError(
                f"model {model!r} not served by local runtime at {self._base_url} "
                f"(served: {', '.join(served) or 'none'})"
            )
        return ModelBinding(model=model, base_url=self._base_url, api_key=self._api_key)
