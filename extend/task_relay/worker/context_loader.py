"""Resolve TaskSpec context payloads (M2): inline, inline_gzip, ContextRef."""

from __future__ import annotations

import gzip
import hashlib
import logging
from typing import Any

logger = logging.getLogger("task_relay.worker.context")


class ContextLoadError(Exception):
    """Context fetch, decode, or sha256 verification failed."""


async def resolve_context_payload(context: Any) -> Any:
    """Return plaintext-ready context after fetch/decode/verify."""
    if context is None:
        return None
    if not isinstance(context, dict):
        return context
    if "ref" in context:
        return await _resolve_ref(context["ref"])
    if "inline_gzip" in context:
        return _resolve_inline_gzip(context["inline_gzip"])
    return context


async def _resolve_ref(ref: dict[str, Any]) -> str:
    uri = ref.get("uri")
    expected_sha = ref.get("sha256", "")
    encoding = ref.get("content_encoding") or ""
    if not uri:
        raise ContextLoadError("ContextRef.uri is required")

    import aiohttp

    async with aiohttp.ClientSession() as session:
        async with session.get(uri, timeout=120) as resp:
            if resp.status >= 400:
                raise ContextLoadError(f"ContextRef fetch failed: HTTP {resp.status}")
            raw = await resp.read()

    if encoding == "gzip":
        raw = gzip.decompress(raw)
    plaintext = raw.decode("utf-8")
    if expected_sha:
        digest = hashlib.sha256(plaintext.encode("utf-8")).hexdigest()
        if digest != expected_sha:
            raise ContextLoadError("ContextRef sha256 mismatch after decode")
    return plaintext


def _resolve_inline_gzip(inline_gzip: dict[str, Any]) -> str:
    data = inline_gzip.get("gzip_data")
    expected_sha = inline_gzip.get("sha256", "")
    if isinstance(data, str):
        import base64

        data = base64.b64decode(data, validate=True)
    if not isinstance(data, (bytes, bytearray)):
        raise ContextLoadError("inline_gzip.gzip_data must be bytes")
    plaintext = gzip.decompress(data).decode("utf-8")
    if expected_sha:
        digest = hashlib.sha256(plaintext.encode("utf-8")).hexdigest()
        if digest != expected_sha:
            raise ContextLoadError("inline_gzip sha256 mismatch after decode")
    return plaintext
