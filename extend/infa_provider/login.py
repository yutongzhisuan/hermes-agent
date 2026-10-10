"""INFA email/password login and consumer-session refresh."""

from __future__ import annotations

import os
import time
import uuid
from typing import Any, Mapping, Optional
from urllib.parse import urljoin

import httpx

from extend.infa_provider.dpop import generate_device_keypair, jwt_unverified_claims
from extend.infa_provider.session import (
    ENV_GATEWAY_URL,
    ENV_PLATFORM_URL,
    InfaAuthError,
    InfaSession,
    _strip_slash,
    inference_url,
)


def _json_get(payload: Mapping[str, Any], *keys: str) -> Any:
    for key in keys:
        if key in payload:
            return payload[key]
    return None


def _raise_http(message: str, response: httpx.Response) -> None:
    detail = (response.text or "").strip()
    raise InfaAuthError(f"{message}: HTTP {response.status_code} {detail[:300]}", code="infa_http_error")


def login(
    *,
    email: str,
    password: str,
    platform_base_url: str,
    gateway_base_url: str,
    client: Optional[httpx.Client] = None,
    device_id: Optional[str] = None,
    keypair: Optional[tuple[str, str]] = None,
    now: Optional[float] = None,
) -> InfaSession:
    platform = _strip_slash(platform_base_url)
    gateway = _strip_slash(gateway_base_url)
    if not platform or not gateway:
        raise InfaAuthError("INFA platform and gateway URLs are required.", code="missing_base_url")
    public_b64, private_b64 = keypair or generate_device_keypair()
    owned = client is None
    http = client or httpx.Client(timeout=20.0)
    try:
        login_resp = http.post(
            urljoin(platform + "/", "api/v1/auth/login"),
            json={"email": email, "password": password},
        )
        if login_resp.status_code >= 400:
            _raise_http("INFA login failed", login_resp)
        login_body = login_resp.json()
        user_token = str(_json_get(login_body, "access_token", "accessToken") or "").strip()
        if not user_token:
            raise InfaAuthError("INFA login did not return an access token.", code="missing_user_token")
        created_device_id = device_id or uuid.uuid4().hex
        session_resp = http.post(
            urljoin(platform + "/", "api/v1/consumer/sessions"),
            json={
                "device_id": created_device_id,
                "device_public_key": public_b64,
                "platform": "xhermes",
                "device_name": "xhermes-cli",
                "trust_level": "software",
            },
            headers={"Authorization": f"Bearer {user_token}"},
        )
        if session_resp.status_code >= 400:
            _raise_http("INFA consumer session create failed", session_resp)
        body = session_resp.json() if session_resp.content else {}
        access_token = str(_json_get(body, "access_token", "accessToken") or "").strip()
        refresh_token = str(_json_get(body, "refresh_token", "refreshToken") or "").strip()
        session_id = str(_json_get(body, "session_id", "sessionId") or "").strip()
        if not access_token or not refresh_token or not session_id:
            raise InfaAuthError("INFA consumer session is incomplete.", code="incomplete_session")
        claims = jwt_unverified_claims(access_token)
        expires_in = int(_json_get(body, "expires_in", "expiresIn") or 0)
        stamp = now if now is not None else time.time()
        return InfaSession(
            session_id=session_id,
            access_token=access_token,
            refresh_token=refresh_token,
            device_id=str(_json_get(body, "device_id", "deviceId") or created_device_id),
            device_private_key=private_b64,
            device_public_key=public_b64,
            platform_base_url=platform,
            gateway_base_url=gateway,
            inference_base_url=inference_url(gateway),
            expires_at=int(claims.get("exp") or (stamp + max(expires_in, 0))),
        )
    finally:
        if owned:
            http.close()


def refresh_session(session: InfaSession, *, client: Optional[httpx.Client] = None, now: Optional[float] = None) -> InfaSession:
    owned = client is None
    http = client or httpx.Client(timeout=20.0)
    try:
        response = http.post(
            urljoin(session.platform_base_url + "/", "api/v1/consumer/sessions/refresh"),
            json={"session_id": session.session_id, "refresh_token": session.refresh_token},
        )
        if response.status_code >= 400:
            _raise_http("INFA consumer session refresh failed", response)
        body = response.json()
        access_token = str(_json_get(body, "access_token", "accessToken") or "").strip()
        refresh_token = str(_json_get(body, "refresh_token", "refreshToken") or session.refresh_token).strip()
        session_id = str(_json_get(body, "session_id", "sessionId") or session.session_id).strip()
        if not access_token:
            raise InfaAuthError("INFA refresh did not return an access token.", code="missing_access_token")
        claims = jwt_unverified_claims(access_token)
        stamp = now if now is not None else time.time()
        expires_in = int(_json_get(body, "expires_in", "expiresIn") or 0)
        session.session_id = session_id
        session.access_token = access_token
        session.refresh_token = refresh_token
        session.expires_at = int(claims.get("exp") or (stamp + max(expires_in, 0)))
        return session
    finally:
        if owned:
            http.close()


def default_gateway_url() -> str:
    return os.getenv(ENV_GATEWAY_URL, "").strip() or os.getenv(ENV_PLATFORM_URL, "").strip()


def default_platform_url() -> str:
    return os.getenv(ENV_PLATFORM_URL, "").strip() or default_gateway_url()


def interactive_login(
    *,
    email: Optional[str] = None,
    password: Optional[str] = None,
    platform_base_url: Optional[str] = None,
    gateway_base_url: Optional[str] = None,
) -> InfaSession:
    import getpass

    email = (email or input("INFA email: ")).strip()
    password = password if password is not None else getpass.getpass("INFA password: ")
    platform_default = default_platform_url()
    gateway_default = default_gateway_url() or platform_default
    if not platform_base_url:
        prompt = f"Platform URL [{platform_default}]: " if platform_default else "Platform URL: "
        platform_base_url = input(prompt).strip() or platform_default
    if not gateway_base_url:
        prompt = f"Gateway URL [{gateway_default or platform_base_url}]: "
        gateway_base_url = input(prompt).strip() or gateway_default or platform_base_url
    if not email or not password:
        raise InfaAuthError("INFA email and password are required.", code="missing_credentials")
    return login(
        email=email,
        password=password,
        platform_base_url=str(platform_base_url),
        gateway_base_url=str(gateway_base_url),
    )
