"""HMAC signing and verification for ContextRef (M3 hardening)."""

from __future__ import annotations

import hashlib
import hmac
from typing import Any


class ContextRefSignError(Exception):
    """ContextRef signature is missing or invalid."""


def canonical_context_ref(ref: dict[str, Any]) -> bytes:
    """Build the canonical byte sequence covered by the HMAC signature."""
    uri = str(ref.get("uri") or "")
    digest = str(ref.get("sha256") or "")
    encoding = str(ref.get("content_encoding") or "")
    return f"{uri}\n{digest}\n{encoding}".encode("utf-8")


def sign_context_ref(ref: dict[str, Any], secret: str) -> str:
    """Return a hex HMAC-SHA256 signature for a ContextRef dict."""
    if not secret:
        raise ContextRefSignError("signing secret is required")
    return hmac.new(
        secret.encode("utf-8"),
        canonical_context_ref(ref),
        hashlib.sha256,
    ).hexdigest()


def verify_context_ref(ref: dict[str, Any], secret: str) -> None:
    """Raise ContextRefSignError when the ref signature is missing or invalid."""
    signature = ref.get("signature")
    if not signature:
        raise ContextRefSignError("ContextRef.signature is required")
    expected = sign_context_ref(
        {
            "uri": ref.get("uri"),
            "sha256": ref.get("sha256"),
            "content_encoding": ref.get("content_encoding") or "",
        },
        secret,
    )
    if not hmac.compare_digest(str(signature), expected):
        raise ContextRefSignError("ContextRef.signature is invalid")
