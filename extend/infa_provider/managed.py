"""Read-only Desktop inference credentials pinned to one managed Agent run."""

from __future__ import annotations

import json
import os
import stat
import time
from pathlib import Path
from typing import Any, Mapping

from extend.infa_provider.dpop import b64url_decode, b64url_encode, jwt_unverified_claims, load_private_key

ENV_MANAGED_FILE = "INFA_MANAGED_AUTH_FILE"
ENV_MANAGED_SCOPE = "INFA_MANAGED_AUTH_SCOPE"
MANAGED_INFERENCE_VERSION = "managed-inference-v1"


def _error():
    from extend.infa_provider.session import InfaAuthError

    return InfaAuthError(
        "Desktop inference authorization is unavailable; restore the Desktop login and restart Agent.",
        code="managed_inference_authorization_required",
    )


def managed_from_environment():
    """An explicit managed locator never falls back to a global CLI credential."""
    path = os.environ.get(ENV_MANAGED_FILE, "")
    encoded_scope = os.environ.get(ENV_MANAGED_SCOPE, "")
    if not path and not encoded_scope:
        return None
    try:
        scope = json.loads(b64url_decode(encoded_scope))
    except (ValueError, TypeError, UnicodeError):
        raise _error() from None
    return load_managed_session(path, scope)


def load_managed_session(path: str, scope: Mapping[str, Any]):
    from extend.infa_provider.session import InfaSession

    try:
        target = Path(path)
        if not isinstance(scope, dict) or not target.is_absolute() or scope.get("credentialPath") != path:
            raise ValueError("locator")
        info = target.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_size > 65536:
            raise ValueError("file")
        if os.name != "nt" and stat.S_IMODE(info.st_mode) & 0o077:
            raise ValueError("permissions")
        data = json.loads(target.read_text(encoding="utf-8"))
        if not isinstance(data, dict) or any(key in data for key in ("refresh_token", "platform_access_token", "device_private_key")):
            raise ValueError("credential kind")
        if scope.get("version") != 1 or data.get("version") != 1 or data.get("managed_by") != "desktop" or data.get("auth_mode") != "inference_token":
            raise ValueError("protocol")
        for field, expected in (
            ("user_id", "userId"), ("session_id", "sessionId"),
            ("context_type", "contextType"), ("context_id", "contextId"),
            ("run_id", "runId"), ("platform_base_url", "platformBaseUrl"),
            ("gateway_base_url", "gatewayBaseUrl"),
        ):
            if not scope.get(expected) or data.get(field) != scope[expected]:
                raise ValueError("scope")
        if data["context_type"] not in ("user", "organization"):
            raise ValueError("context")
        user_id, context_id = int(data["user_id"]), int(data["context_id"])
        if user_id <= 0 or context_id <= 0 or (data["context_type"] == "user" and user_id != context_id):
            raise ValueError("owner")
        claims = jwt_unverified_claims(data["access_token"])
        expires = data["expires_at"]
        if not isinstance(expires, int) or isinstance(expires, bool) or expires <= time.time() or claims.get("exp") != expires:
            raise ValueError("expiry")
        if claims.get("use") != "inference" or claims.get("sub") != data["user_id"] or claims.get("sid") != data["session_id"] or claims.get("context_type") != data["context_type"] or claims.get("context_id") != context_id or claims.get("device_id") != data.get("device_id") or not claims.get("jti") or int(claims.get("identity_version", 0)) <= 0:
            raise ValueError("token binding")
        proof_key = load_private_key(data["proof_private_key"])
        public = b64url_encode(proof_key.public_key().public_bytes_raw())
        if public != data.get("proof_public_key") or public != claims.get("cnf_ed25519"):
            raise ValueError("proof binding")
        gateway = data["gateway_base_url"].rstrip("/")
        if data.get("inference_base_url") != gateway + "/api/v1":
            raise ValueError("endpoint")
        return InfaSession(
            session_id=data["session_id"], access_token=data["access_token"], refresh_token="",
            device_id=data["device_id"], device_private_key=data["proof_private_key"], device_public_key=public,
            platform_base_url=data["platform_base_url"], gateway_base_url=gateway,
            inference_base_url=data["inference_base_url"], expires_at=expires, managed_by="desktop",
            managed_auth_file=path, managed_scope=dict(scope),
        )
    except (OSError, ValueError, TypeError, KeyError, UnicodeError):
        raise _error() from None


def session_at_request_boundary(session):
    if session.managed_by != "desktop":
        return session
    return load_managed_session(session.managed_auth_file, session.managed_scope)
