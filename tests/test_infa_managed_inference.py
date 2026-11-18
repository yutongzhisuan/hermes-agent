"""Exercise the real provider imports, file reload and proof transports."""

import base64
import copy
import json
import os
import subprocess
import sys
import time
from pathlib import Path

import httpx
import pytest
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

from extend.infa_provider.dpop import b64url_decode, b64url_encode, generate_device_keypair, signing_message
from extend.infa_provider.http import attach_dpop
from extend.infa_provider.managed import managed_from_environment
from extend.infa_provider.session import InfaAuthError, resolve_runtime_credentials, save_session


def write_payload(path, payload):
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(payload), encoding="utf-8")
    temporary.chmod(0o600)
    temporary.replace(path)


@pytest.fixture
def managed(tmp_path, monkeypatch):
    path = tmp_path / "run-a" / "auth.json"
    path.parent.mkdir(mode=0o700)
    public, private = generate_device_keypair()
    scope = {
        "version": 1, "credentialPath": str(path), "userId": "7", "sessionId": "root-a",
        "contextType": "organization", "contextId": "19", "runId": "run-a",
        "platformBaseUrl": "https://platform.example.invalid", "gatewayBaseUrl": "https://gateway.example.invalid",
    }
    claims = {"sub": "7", "sid": "root-a", "jti": "token-a", "use": "inference", "identity_version": 1,
              "context_type": "organization", "context_id": 19, "device_id": "parent-device", "cnf_ed25519": public,
              "iss": "platform", "aud": "gateway", "iat": int(time.time()), "exp": int(time.time()) + 300}
    payload = {
        "version": 1, "managed_by": "desktop", "auth_mode": "inference_token", "user_id": "7", "session_id": "root-a",
        "context_type": "organization", "context_id": "19", "run_id": "run-a", "device_id": "parent-device",
        "expires_at": claims["exp"], "proof_private_key": private, "proof_public_key": public,
        "platform_base_url": scope["platformBaseUrl"], "gateway_base_url": scope["gatewayBaseUrl"],
        "inference_base_url": scope["gatewayBaseUrl"] + "/api/v1",
    }
    def publish(token_id="token-a", **changes):
        next_claims = dict(claims, jti=token_id)
        next_claims.update(changes.pop("claims", {}))
        current = dict(payload, access_token="e30." + b64url_encode(json.dumps(next_claims).encode()) + ".test-signature")
        current.update(changes)
        write_payload(path, current)
        return current
    publish()
    monkeypatch.setenv("INFA_MANAGED_AUTH_FILE", str(path))
    monkeypatch.setenv("INFA_MANAGED_AUTH_SCOPE", b64url_encode(json.dumps(scope).encode()))
    return path, scope, publish


def test_resolver_never_exports_refresh_or_saves_managed_credentials(managed):
    creds = resolve_runtime_credentials(force_refresh=True)
    assert creds["source"] == "desktop-managed-inference"
    assert creds["session"].refresh_token == ""
    with pytest.raises(InfaAuthError, match="read-only"):
        save_session(creds["session"])


def test_long_lived_client_reloads_token_and_preserves_challenge_request_key(managed):
    path, scope, publish = managed
    session = managed_from_environment()
    observed = []
    def handle(request):
        observed.append(request)
        if not request.headers.get("DPoP"):
            return httpx.Response(401, headers={"DPoP-Nonce": "nonce"})
        proof = json.loads(b64url_decode(request.headers["DPoP"]))
        signature = b64url_decode(proof["signature"])
        Ed25519PublicKey.from_public_bytes(b64url_decode(session.device_public_key)).verify(signature, signing_message(proof).encode())
        expected = "token-b" if len(observed) > 2 else "token-a"
        assert proof["token_jti"] == expected
        assert proof["sid"] == scope["sessionId"]
        return httpx.Response(200, json={"ok": True})
    with httpx.Client(transport=httpx.MockTransport(handle)) as client:
        attach_dpop(client, session=session)
        url = scope["gatewayBaseUrl"] + "/api/v1/chat/completions"
        client.post(url, json={"model": "test"})
        first_key = observed[0].headers["Idempotency-Key"]
        assert observed[1].headers["Idempotency-Key"] == first_key
        publish("token-b")
        client.post(url, json={"model": "test"})
    assert len(observed) == 4  # Each token obtains its own nonce.
    assert observed[2].headers["Authorization"] != observed[0].headers["Authorization"]
    assert observed[3].headers["Idempotency-Key"] == observed[2].headers["Idempotency-Key"]
    assert observed[3].headers["Idempotency-Key"] != first_key


@pytest.mark.asyncio
async def test_async_transport_reloads_at_each_request_boundary(managed):
    _, scope, publish = managed
    observed = []
    async def handle(request):
        observed.append(request.headers["Authorization"])
        return httpx.Response(200, json={"ok": True})
    async with httpx.AsyncClient(transport=httpx.MockTransport(handle)) as client:
        attach_dpop(client, session=managed_from_environment())
        url = scope["gatewayBaseUrl"] + "/api/v1/models"
        await client.get(url)
        publish("token-b")
        await client.get(url)
    assert observed[0] != observed[1]


@pytest.mark.parametrize("change", [
    {"user_id": "8"}, {"session_id": "root-b"}, {"context_id": "20"}, {"run_id": "run-b"},
    {"gateway_base_url": "https://other.invalid"}, {"proof_public_key": "wrong-key"},
    {"refresh_token": "prohibited-secret"}, {"device_private_key": "prohibited-secret"},
    {"claims": {"use": "platform"}}, {"claims": {"context_id": 20}},
    {"expires_at": 1, "claims": {"exp": 1}},
])
def test_wrong_identity_scope_purpose_key_and_long_secrets_are_rejected(managed, change):
    _, _, publish = managed
    publish(**copy.deepcopy(change))
    with pytest.raises(InfaAuthError) as error:
        resolve_runtime_credentials()
    assert "prohibited-secret" not in str(error.value)


def test_old_run_cannot_follow_a_new_environment_locator(managed, tmp_path, monkeypatch):
    path, _, _ = managed
    old = managed_from_environment()
    path.unlink()
    monkeypatch.setenv("INFA_MANAGED_AUTH_FILE", str(tmp_path / "new-account.json"))
    monkeypatch.setenv("INFA_MANAGED_AUTH_SCOPE", b64url_encode(b'{"userId":"8"}'))
    from extend.infa_provider.managed import session_at_request_boundary
    with pytest.raises(InfaAuthError):
        session_at_request_boundary(old)


def test_missing_file_never_uses_an_independent_cli_credential(managed, monkeypatch):
    path, _, _ = managed
    home = Path(os.environ["XHERMES_HOME"])
    home.mkdir(parents=True, exist_ok=True)
    auth_path = home / "auth.json"
    original = '{"providers":{"infa":{"access_token":"other-account","refresh_token":"other-refresh","device_private_key":"other-key"}}}'
    auth_path.write_text(original)
    path.unlink()
    with pytest.raises(InfaAuthError):
        resolve_runtime_credentials()
    assert auth_path.read_text() == original


@pytest.mark.parametrize("url", ["https://other.invalid/api/v1/models", "https://other.invalid/unknown", "https://gateway.example.invalid/api/v1/auth/refresh"])
def test_managed_transport_rejects_another_gateway(managed, url):
    with httpx.Client(transport=httpx.MockTransport(lambda request: pytest.fail("cross-source request escaped"))) as client:
        attach_dpop(client, session=managed_from_environment())
        with pytest.raises(InfaAuthError):
            client.get(url)


def test_private_file_permissions_are_required(managed):
    if os.name == "nt":
        pytest.skip("Windows ACL acceptance is exercised on Windows")
    path, _, _ = managed
    path.chmod(0o644)
    with pytest.raises(InfaAuthError):
        managed_from_environment()


def test_installed_entrypoint_reports_managed_protocol_without_auth_mutation(tmp_path):
    environment = dict(os.environ, XHERMES_HOME=str(tmp_path))
    completed = subprocess.run([sys.executable, "-m", "hermes_cli.main", "--managed-inference-version"], env=environment, capture_output=True, text=True, timeout=15)
    assert completed.returncode == 0, completed.stderr
    assert completed.stdout.strip() == "managed-inference-v1"
    assert not (tmp_path / "auth.json").exists()
