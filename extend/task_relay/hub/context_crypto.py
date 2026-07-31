"""Encrypt inline TaskSpec context at rest (M3 hardening)."""

from __future__ import annotations

import base64
import hashlib
import json
import os
from typing import Any

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

from extend.task_relay.hub.json_util import safe_json_loads

_ENVELOPE_KEY = "encrypted_inline"
_VERSION = 1


class ContextCryptoError(Exception):
    """Inline context encryption or decryption failed."""


def _derive_key(secret: str) -> bytes:
    if not secret:
        raise ContextCryptoError("encryption secret is required")
    return hashlib.sha256(secret.encode("utf-8")).digest()


def _encrypt_bytes(plaintext: bytes, secret: str) -> str:
    nonce = os.urandom(12)
    ciphertext = AESGCM(_derive_key(secret)).encrypt(nonce, plaintext, None)
    return base64.b64encode(nonce + ciphertext).decode("ascii")


def _decrypt_bytes(blob: str, secret: str) -> bytes:
    raw = base64.b64decode(blob.encode("ascii"))
    if len(raw) < 13:
        raise ContextCryptoError("ciphertext is too short")
    nonce, ciphertext = raw[:12], raw[12:]
    return AESGCM(_derive_key(secret)).decrypt(nonce, ciphertext, None)


def should_encrypt_context(context_json: str | None) -> bool:
    """Return True when context_json carries in-band inline payload."""
    if not context_json:
        return False
    payload = safe_json_loads(context_json) or {}
    return "inline" in payload or "inline_gzip" in payload


def encrypt_context_json(context_json: str, secret: str) -> str:
    """Wrap inline context in an encrypted envelope for storage."""
    if not should_encrypt_context(context_json):
        return context_json
    blob = _encrypt_bytes(context_json.encode("utf-8"), secret)
    return json.dumps({_ENVELOPE_KEY: {"v": _VERSION, "data": blob}})


def decrypt_context_json(context_json: str | None, secret: str) -> Any:
    """Return worker-ready context dict, decrypting stored envelopes when needed."""
    if not context_json:
        return None
    payload = safe_json_loads(context_json)
    if not isinstance(payload, dict):
        return payload
    envelope = payload.get(_ENVELOPE_KEY)
    if not isinstance(envelope, dict):
        return payload
    blob = envelope.get("data")
    if not isinstance(blob, str):
        raise ContextCryptoError("encrypted_inline.data is required")
    plaintext = _decrypt_bytes(blob, secret).decode("utf-8")
    return safe_json_loads(plaintext)
