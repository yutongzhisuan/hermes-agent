"""httpx DPoP transport for INFA consumer inference."""

from __future__ import annotations

import time
from typing import Any, Optional
from urllib.parse import urlparse

import httpx

from extend.infa_provider.dpop import (
    body_hash,
    encode_proof,
    jwt_unverified_claims,
    new_proof_id,
    token_hash,
)
from extend.infa_provider.session import InfaSession, load_session

_SIGNED_PREFIXES = (
    "/api/v1/chat/completions",
    "/api/v1/responses",
    "/api/v1/models",
    "/api/v1/capabilities",
)


def should_sign(path: str) -> bool:
    return any(path == prefix or path.startswith(prefix + "/") for prefix in _SIGNED_PREFIXES)


def _dpop_nonce(headers: httpx.Headers) -> str:
    return str(headers.get("DPoP-Nonce") or headers.get("dpop-nonce") or "").strip()


def _clone_request(request: httpx.Request) -> httpx.Request:
    headers = httpx.Headers(request.headers)
    headers.pop("DPoP", None)
    return httpx.Request(
        method=request.method,
        url=request.url,
        headers=headers,
        content=request.content,
        extensions=dict(request.extensions),
    )


def apply_dpop_header(request: httpx.Request, session: InfaSession, nonce: str) -> None:
    claims = jwt_unverified_claims(session.access_token)
    request.headers["DPoP"] = encode_proof(
        {
            "sid": session.session_id or str(claims.get("sid") or ""),
            "ath": token_hash(session.access_token),
            "htm": request.method,
            "htu": request.url.path,
            "body_sha256": body_hash(request.content or b""),
            "nonce": nonce,
            "token_jti": str(claims.get("jti") or ""),
            "jti": new_proof_id(),
            "iat": int(time.time()),
        },
        session.device_private_key,
    )


def _retry_after_challenge(response: httpx.Response, request: httpx.Request, session: InfaSession, nonce: str, send) -> httpx.Response:
    try:
        response.read()
    except Exception:
        pass
    response.close()
    retry = _clone_request(request)
    apply_dpop_header(retry, session, nonce)
    return send(retry)


class _DPoPTransport(httpx.BaseTransport):
    def __init__(self, inner: httpx.BaseTransport, session: InfaSession, nonce: list[str]):
        self._inner = inner
        self._session = session
        self._nonce = nonce

    def handle_request(self, request: httpx.Request) -> httpx.Response:
        if not should_sign(request.url.path):
            return self._inner.handle_request(request)
        current = request
        if self._nonce[0]:
            current = _clone_request(request)
            apply_dpop_header(current, self._session, self._nonce[0])
        response = self._inner.handle_request(current)
        challenge = _dpop_nonce(response.headers)
        if challenge:
            self._nonce[0] = challenge
        if response.status_code == 401 and challenge:
            return _retry_after_challenge(
                response, request, self._session, challenge, self._inner.handle_request
            )
        return response


class _AsyncDPoPTransport(httpx.AsyncBaseTransport):
    def __init__(self, inner: httpx.AsyncBaseTransport, session: InfaSession, nonce: list[str]):
        self._inner = inner
        self._session = session
        self._nonce = nonce

    async def handle_async_request(self, request: httpx.Request) -> httpx.Response:
        if not should_sign(request.url.path):
            return await self._inner.handle_async_request(request)
        current = request
        if self._nonce[0]:
            current = _clone_request(request)
            apply_dpop_header(current, self._session, self._nonce[0])
        response = await self._inner.handle_async_request(current)
        challenge = _dpop_nonce(response.headers)
        if challenge:
            self._nonce[0] = challenge
        if response.status_code == 401 and challenge:
            try:
                await response.aread()
            except Exception:
                pass
            await response.aclose()
            retry = _clone_request(request)
            apply_dpop_header(retry, self._session, challenge)
            return await self._inner.handle_async_request(retry)
        return response


def attach_dpop(client: httpx.Client, *, session: InfaSession) -> None:
    if getattr(client, "_infa_dpop_attached", False):
        return
    nonce = [""]
    inner = getattr(client, "_transport", None)
    if inner is not None:
        if isinstance(client, httpx.AsyncClient):
            client._transport = _AsyncDPoPTransport(inner, session, nonce)
        else:
            client._transport = _DPoPTransport(inner, session, nonce)
    mounts = getattr(client, "_mounts", None) or {}
    for pattern, transport in list(mounts.items()):
        if isinstance(client, httpx.AsyncClient):
            mounts[pattern] = _AsyncDPoPTransport(transport, session, nonce)
        else:
            mounts[pattern] = _DPoPTransport(transport, session, nonce)
    client._infa_dpop_attached = True


def maybe_attach_infa_dpop(client: Any, *, provider: str = "", base_url: str = "", session: Optional[InfaSession] = None) -> None:
    if client is None:
        return
    try:
        from extend.unix_socket_http import is_unix_base_url

        if is_unix_base_url(base_url):
            return
    except Exception:
        if str(base_url or "").strip().lower().startswith("unix://"):
            return
    loaded = session or load_session()
    if loaded is None or not loaded.device_private_key:
        return
    provider_id = (provider or "").strip().lower()
    if provider_id not in {"infa", "infa-oauth"}:
        inferred = str(base_url or loaded.inference_base_url).rstrip("/")
        session_base = loaded.inference_base_url.rstrip("/")
        if not inferred or urlparse(inferred).netloc != urlparse(session_base).netloc:
            return
    attach_dpop(client, session=loaded)


def fetch_models(session: InfaSession, *, client: Optional[httpx.Client] = None) -> list[str]:
    owned = client is None
    http = client or httpx.Client(timeout=20.0)
    try:
        attach_dpop(http, session=session)
        response = http.get(
            session.inference_base_url.rstrip("/") + "/models",
            headers={"Authorization": f"Bearer {session.access_token}"},
        )
        if response.status_code >= 400:
            return []
        return _model_ids_from_payload(response.json())
    except Exception:
        return []
    finally:
        if owned:
            http.close()


def _model_ids_from_payload(payload: Any) -> list[str]:
    rows = payload.get("data") if isinstance(payload, dict) else payload
    if not isinstance(rows, list):
        return []
    models = []
    for row in rows:
        if isinstance(row, str) and row.strip():
            models.append(row.strip())
        elif isinstance(row, dict):
            model_id = str(row.get("id") or row.get("model") or "").strip()
            if model_id:
                models.append(model_id)
    return models


def fetch_models_api_key(base_url: str, api_key: str, *, client: Optional[httpx.Client] = None) -> list[str]:
    """Fetch /models with Bearer auth and no DPoP (config.yaml API-key mode)."""
    owned = client is None
    http = client or httpx.Client(timeout=20.0)
    try:
        response = http.get(
            str(base_url).rstrip("/") + "/models",
            headers={"Authorization": f"Bearer {api_key}"},
        )
        if response.status_code >= 400:
            return []
        return _model_ids_from_payload(response.json())
    except Exception:
        return []
    finally:
        if owned:
            http.close()


def live_model_ids() -> list[str]:
    """INFA catalog: auth.json session (DPoP) wins over config.yaml API key."""
    try:
        from extend.infa_provider.session import resolve_runtime_credentials

        creds = resolve_runtime_credentials(refresh_if_expiring=True)
        session = creds.get("session")
        if session is not None:
            return fetch_models(session)
        api_key = str(creds.get("api_key") or "").strip()
        base_url = str(creds.get("base_url") or "").strip()
        if api_key and base_url:
            return fetch_models_api_key(base_url, api_key)
        return []
    except Exception:
        return []
