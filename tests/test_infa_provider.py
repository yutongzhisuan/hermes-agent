"""INFA consumer-session provider: DPoP, login, and HTTP retry."""

from __future__ import annotations

import base64
import json
import time
import httpx
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey


def _b64url_decode(text: str) -> bytes:
    pad = "=" * ((4 - len(text) % 4) % 4)
    return base64.urlsafe_b64decode(text + pad)


def test_token_hash_matches_unpadded_sha256_base64url():
    from extend.infa_provider.dpop import token_hash

    digest = __import__("hashlib").sha256(b"access-token").digest()
    expected = base64.urlsafe_b64encode(digest).rstrip(b"=").decode("ascii")
    assert token_hash("access-token") == expected


def test_encode_proof_verifies_with_device_public_key():
    from extend.infa_provider.dpop import (
        body_hash,
        encode_proof,
        generate_device_keypair,
        signing_message,
        token_hash,
    )

    public_b64, private_b64 = generate_device_keypair()
    token = "access-token"
    body = b'{"model":"Qwen"}'
    encoded = encode_proof(
        {
            "sid": "session-1",
            "ath": token_hash(token),
            "htm": "POST",
            "htu": "/api/v1/chat/completions",
            "body_sha256": body_hash(body),
            "nonce": "nonce-1",
            "token_jti": "token-1",
            "jti": "proof-1",
            "iat": 1000,
        },
        private_b64,
    )
    proof = json.loads(_b64url_decode(encoded))
    signature = _b64url_decode(proof["signature"])
    message = signing_message(proof).encode("utf-8")
    Ed25519PublicKey.from_public_bytes(_b64url_decode(public_b64)).verify(signature, message)


def _fake_jwt(*, sid: str = "cs1", jti: str = "tok1", exp: int | None = None) -> str:
    header = base64.urlsafe_b64encode(b'{"alg":"EdDSA","typ":"JWT"}').rstrip(b"=").decode()
    payload = base64.urlsafe_b64encode(
        json.dumps({"sid": sid, "jti": jti, "exp": exp or int(time.time()) + 3600}).encode()
    ).rstrip(b"=").decode()
    return f"{header}.{payload}.sig"


def test_login_creates_consumer_session_and_persists_device_key(tmp_path, monkeypatch):
    from extend.infa_provider import session as infa_session
    from extend.infa_provider.dpop import generate_device_keypair

    monkeypatch.setenv("XHERMES_HOME", str(tmp_path))
    public_b64, private_b64 = generate_device_keypair()
    consumer_jwt = _fake_jwt()

    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/api/v1/auth/login":
            body = json.loads(request.content)
            assert body["email"] == "user@example.com"
            return httpx.Response(200, json={"access_token": "user-jwt", "refresh_token": "ur", "expires_in": 3600})
        if request.url.path == "/api/v1/consumer/sessions":
            assert request.headers["Authorization"] == "Bearer user-jwt"
            body = json.loads(request.content)
            assert body["device_public_key"] == public_b64
            return httpx.Response(
                200,
                json={"session_id": "cs1", "access_token": consumer_jwt, "refresh_token": "cr", "expires_in": 300},
            )
        return httpx.Response(404)

    client = httpx.Client(transport=httpx.MockTransport(handler))
    result = infa_session.login(
        email="user@example.com",
        password="secret-password",
        platform_base_url="https://platform.example.com",
        gateway_base_url="https://gateway.example.com",
        client=client,
        device_id="dev-1",
        keypair=(public_b64, private_b64),
    )
    assert result.session_id == "cs1"
    assert result.access_token == consumer_jwt
    assert result.inference_base_url == "https://gateway.example.com/api/v1"
    infa_session.save_session(result)
    loaded = infa_session.load_session()
    assert loaded is not None
    assert loaded.device_private_key == private_b64
    assert loaded.session_id == "cs1"


def test_dpop_http_retries_after_nonce_challenge():
    from extend.infa_provider.dpop import generate_device_keypair, jwt_unverified_claims
    from extend.infa_provider.http import attach_dpop
    from extend.infa_provider.session import InfaSession

    public_b64, private_b64 = generate_device_keypair()
    token = _fake_jwt()
    claims = jwt_unverified_claims(token)
    seen = {"dpop": 0, "challenge": 0}

    def handler(request: httpx.Request) -> httpx.Response:
        if request.headers.get("DPoP"):
            seen["dpop"] += 1
            return httpx.Response(200, json={"id": "chatcmpl-1"}, headers={"DPoP-Nonce": "n2"})
        seen["challenge"] += 1
        return httpx.Response(
            401,
            json={"error": {"type": "consumer_proof_required"}},
            headers={"DPoP-Nonce": "n1"},
        )

    session = InfaSession(
        session_id=str(claims.get("sid") or "cs1"),
        access_token=token,
        refresh_token="cr",
        device_id="dev-1",
        device_private_key=private_b64,
        device_public_key=public_b64,
        platform_base_url="https://platform.example.com",
        gateway_base_url="https://gateway.example.com",
        inference_base_url="https://gateway.example.com/api/v1",
        expires_at=int(time.time()) + 3600,
    )
    client = httpx.Client(transport=httpx.MockTransport(handler))
    attach_dpop(client, session=session)
    response = client.post(
        "https://gateway.example.com/api/v1/chat/completions",
        json={"model": "Qwen", "messages": [{"role": "user", "content": "hi"}]},
        headers={"Authorization": f"Bearer {token}"},
    )
    assert response.status_code == 200
    assert seen["challenge"] == 1
    assert seen["dpop"] == 1
