"""OpenAI-compatible inference over Unix domain sockets.

Config form (llama.cpp and similar local servers)::

    model:
      base_url: unix:///tmp/llm.sock
      api_key: "nokey"

``unix:///abs/path.sock`` is rewritten to a synthetic OpenAI SDK base URL
``http://localhost/v1`` while httpx connects via ``HTTPTransport(uds=...)``.
"""

from __future__ import annotations

from typing import Any, Optional, Tuple
from urllib.parse import urlparse

DEFAULT_OPENAI_BASE_URL = "http://localhost/v1"


def is_unix_base_url(value: Any) -> bool:
    return str(value or "").strip().lower().startswith("unix://")


def parse_unix_socket_url(value: Any) -> Tuple[str, str]:
    """Parse ``unix:///abs/path.sock`` into ``(socket_path, openai_base_url)``.

    Only absolute socket paths are accepted (``unix:///...``). Relative forms
    such as ``unix://tmp/llm.sock`` are rejected because ``urlparse`` treats
    the first segment as ``netloc``.
    """
    raw = str(value or "").strip()
    if not is_unix_base_url(raw):
        raise ValueError(f"not a unix:// URL: {raw!r}")
    parsed = urlparse(raw)
    if parsed.netloc:
        raise ValueError(
            f"unix:// URL must use an absolute socket path "
            f"(unix:///path/to.sock), got {raw!r}"
        )
    socket_path = parsed.path or ""
    if not socket_path.startswith("/") or socket_path == "/":
        raise ValueError(f"unix:// URL missing absolute socket path: {raw!r}")
    if parsed.query or parsed.fragment:
        raise ValueError(f"unix:// URL must not include query/fragment: {raw!r}")
    return socket_path, DEFAULT_OPENAI_BASE_URL


def resolve_openai_base_url(base_url: Any) -> Tuple[str, Optional[str]]:
    """Return ``(openai_sdk_base_url, socket_path_or_none)``."""
    raw = str(base_url or "").strip()
    if not is_unix_base_url(raw):
        return raw, None
    socket_path, openai_base = parse_unix_socket_url(raw)
    return openai_base, socket_path


def require_uds_http_client(http_client: Any, base_url: Any) -> None:
    """Fail closed: a unix:// endpoint must never fall back to TCP localhost."""
    if not is_unix_base_url(base_url):
        return
    if http_client is not None:
        return
    raise RuntimeError(
        f"Failed to build UDS HTTP client for {str(base_url or '').strip()!r}; "
        "refusing to fall back to TCP localhost."
    )


def build_unix_httpx_client(
    socket_path: str,
    *,
    async_mode: bool = False,
    verify: Any = True,
    limits: Any = None,
    timeout: Any = None,
) -> Any:
    """Build an httpx Client/AsyncClient that dials ``socket_path`` via UDS."""
    import httpx

    transport_cls = httpx.AsyncHTTPTransport if async_mode else httpx.HTTPTransport
    client_cls = httpx.AsyncClient if async_mode else httpx.Client
    if limits is None:
        limits = httpx.Limits(
            max_keepalive_connections=20,
            max_connections=100,
            keepalive_expiry=20.0,
        )
    if timeout is None:
        timeout = httpx.Timeout(connect=15.0, read=None, write=15.0, pool=10.0)
    transport = transport_cls(uds=socket_path, verify=verify)
    return client_cls(
        transport=transport,
        limits=limits,
        timeout=timeout,
        verify=verify,
    )
