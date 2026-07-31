"""Worker JWT load, bootstrap exchange, and refresh-before-exp (M1 baseline)."""

from __future__ import annotations

import json
import logging
import ssl
import time
from pathlib import Path
from typing import Any
from urllib.parse import urlparse, urlunparse

import jwt

logger = logging.getLogger("task_relay.worker.jwt")

REFRESH_BUFFER_SECONDS = 300


def derive_token_url(relay_url: str, http_port: int | None = None) -> str:
    """Map a worker WebSocket URL to the Hub token HTTP endpoint."""
    parsed = urlparse(relay_url)
    scheme = "https" if parsed.scheme == "wss" else "http"
    host = parsed.hostname or "127.0.0.1"
    port = http_port if http_port is not None else (parsed.port or 9000) + 1
    netloc = f"{host}:{port}" if port else host
    return urlunparse((scheme, netloc, "/v1/worker/token", "", "", ""))


def _looks_like_jwt(value: str) -> bool:
    return value.count(".") == 2


def _jwt_exp(value: str) -> int | None:
    try:
        payload = jwt.decode(value, options={"verify_signature": False})
    except jwt.PyJWTError:
        return None
    exp = payload.get("exp")
    return int(exp) if exp is not None else None


async def _post_token_request(
    token_url: str,
    payload: dict[str, Any],
    *,
    ssl_context: ssl.SSLContext | None = None,
) -> tuple[str, int]:
    import aiohttp

    connector = aiohttp.TCPConnector(ssl=ssl_context) if ssl_context is not None else None
    async with aiohttp.ClientSession(connector=connector) as session:
        async with session.post(token_url, json=payload) as resp:
            body = await resp.json(content_type=None)
            if resp.status >= 400:
                message = body.get("error") if isinstance(body, dict) else str(body)
                raise RuntimeError(f"token endpoint returned {resp.status}: {message}")
            if not isinstance(body, dict) or "worker_jwt" not in body:
                raise RuntimeError("token endpoint response missing worker_jwt")
            token = str(body["worker_jwt"])
            expires_at = int(body.get("expires_at") or _jwt_exp(token) or 0)
            return token, expires_at


async def ensure_worker_jwt(
    *,
    worker_id: str,
    jwt_file: Path,
    token_url: str,
    bootstrap_file: Path | None = None,
    ssl_context: ssl.SSLContext | None = None,
) -> str:
    """Return a valid worker JWT, exchanging or refreshing as needed."""
    cached = _read_cached(jwt_file)
    bootstrap = _read_bootstrap(bootstrap_file) if bootstrap_file is not None else None

    if cached and _jwt_exp(cached):
        exp = _jwt_exp(cached)
        assert exp is not None
        if exp - time.time() > REFRESH_BUFFER_SECONDS:
            return cached
        try:
            token, _ = await _post_token_request(
                token_url, {"worker_jwt": cached}, ssl_context=ssl_context
            )
            _write_jwt(jwt_file, token)
            logger.info("refreshed worker JWT for %s", worker_id)
            return token
        except Exception as exc:
            logger.warning("JWT refresh failed (%s); trying bootstrap exchange", exc)

    if cached and _looks_like_jwt(cached):
        return cached

    bootstrap_token = bootstrap or (cached if cached and not _looks_like_jwt(cached) else None)
    if bootstrap_token is None:
        if cached and _looks_like_jwt(cached):
            return cached
        raise RuntimeError(
            "no valid worker JWT and no bootstrap credential; "
            "place a bootstrap token in --worker-jwt-file or --worker-bootstrap-file"
        )

    token, _ = await _post_token_request(
        token_url,
        {"bootstrap_token": bootstrap_token, "worker_id": worker_id},
        ssl_context=ssl_context,
    )
    _write_jwt(jwt_file, token)
    logger.info("exchanged bootstrap token for worker JWT (%s)", worker_id)
    return token


def _read_cached(path: Path) -> str | None:
    try:
        value = path.read_text(encoding="utf-8").strip()
    except OSError:
        return None
    return value or None


def _read_bootstrap(path: Path) -> str | None:
    try:
        value = path.read_text(encoding="utf-8").strip()
    except OSError:
        return None
    return value or None


def _write_jwt(path: Path, token: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(token + "\n", encoding="utf-8")
