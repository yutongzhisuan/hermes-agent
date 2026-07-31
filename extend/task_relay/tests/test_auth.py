"""Tests for the Task Relay Hub auth (bootstrap + JWT, M1 HS256)."""

import time

import jwt as pyjwt
import pytest

from extend.task_relay.hub.auth import Auth, AuthError, WorkerClaims, MasterClaims
from extend.task_relay.hub.config import BootstrapEntry, HubConfig
from extend.task_relay.tests.conftest import AUDIENCE, ISSUER, SECRET, make_auth

def raw_token(claims: dict, secret: str = SECRET) -> str:
    return pyjwt.encode(claims, secret, algorithm="HS256")


# --- worker JWT -------------------------------------------------------------


def test_worker_jwt_roundtrip():
    auth = make_auth()
    tok = auth.issue_worker_jwt("worker-01", ["terminal"], max_concurrent=2, ttl_s=3600)
    claims = auth.verify_worker_jwt(tok)
    assert isinstance(claims, WorkerClaims)
    assert claims.sub == "worker-01"
    assert claims.allowed_toolsets == ["terminal"]
    assert claims.max_concurrent == 2


def test_worker_jwt_exact_claim_keys():
    auth = make_auth()
    tok = auth.issue_worker_jwt("worker-01", ["terminal", "file"], max_concurrent=2, ttl_s=3600)
    payload = pyjwt.decode(tok, SECRET, algorithms=["HS256"], audience=AUDIENCE, issuer=ISSUER)
    assert set(payload) == {"sub", "aud", "iss", "allowed_toolsets", "max_concurrent", "exp"}
    assert payload["sub"] == "worker-01"
    assert payload["aud"] == AUDIENCE
    assert payload["iss"] == ISSUER
    assert payload["allowed_toolsets"] == ["terminal", "file"]
    assert payload["max_concurrent"] == 2
    assert payload["exp"] > int(time.time())


def test_reject_missing_audience():
    auth = make_auth()
    tok = raw_token(
        {
            "sub": "worker-01",
            "iss": ISSUER,
            "allowed_toolsets": ["terminal"],
            "max_concurrent": 1,
            "exp": int(time.time()) + 600,
        }
    )
    with pytest.raises(AuthError):
        auth.verify_worker_jwt(tok)


def test_reject_wrong_audience():
    auth = make_auth()
    tok = raw_token(
        {
            "sub": "worker-01",
            "aud": "someone-else",
            "iss": ISSUER,
            "allowed_toolsets": ["terminal"],
            "max_concurrent": 1,
            "exp": int(time.time()) + 600,
        }
    )
    with pytest.raises(AuthError):
        auth.verify_worker_jwt(tok)


def test_reject_wrong_issuer():
    auth = make_auth()
    tok = raw_token(
        {
            "sub": "worker-01",
            "aud": AUDIENCE,
            "iss": "evil-issuer",
            "allowed_toolsets": ["terminal"],
            "max_concurrent": 1,
            "exp": int(time.time()) + 600,
        }
    )
    with pytest.raises(AuthError):
        auth.verify_worker_jwt(tok)


def test_reject_bad_signature():
    auth = make_auth()
    tok = raw_token(
        {
            "sub": "worker-01",
            "aud": AUDIENCE,
            "iss": ISSUER,
            "allowed_toolsets": ["terminal"],
            "max_concurrent": 1,
            "exp": int(time.time()) + 600,
        },
        secret="w" * 32,
    )
    with pytest.raises(AuthError):
        auth.verify_worker_jwt(tok)


def test_reject_expired():
    auth = make_auth()
    tok = auth.issue_worker_jwt("worker-01", ["terminal"], max_concurrent=1, ttl_s=-10)
    with pytest.raises(AuthError):
        auth.verify_worker_jwt(tok)


def test_reject_worker_token_missing_scope_claims():
    auth = make_auth()
    tok = raw_token(
        {
            "sub": "worker-01",
            "aud": AUDIENCE,
            "iss": ISSUER,
            "exp": int(time.time()) + 600,
        }
    )
    with pytest.raises(AuthError):
        auth.verify_worker_jwt(tok)


# --- master JWT -------------------------------------------------------------


def test_master_jwt_roundtrip():
    auth = make_auth()
    tok = auth.issue_master_jwt("master-01", ttl_s=3600)
    claims = auth.verify_master_jwt(tok)
    assert isinstance(claims, MasterClaims)
    assert claims.sub == "master-01"


def test_master_jwt_rejected_as_worker_and_vice_versa():
    auth = make_auth()
    master_tok = auth.issue_master_jwt("master-01")
    worker_tok = auth.issue_worker_jwt("worker-01", ["terminal"], max_concurrent=1)
    with pytest.raises(AuthError):
        auth.verify_worker_jwt(master_tok)
    with pytest.raises(AuthError):
        auth.verify_master_jwt(worker_tok)


# --- bootstrap exchange -----------------------------------------------------


def test_exchange_bootstrap_issues_scoped_worker_jwt():
    auth = make_auth(
        bootstrap_tokens={
            "boot-abc": BootstrapEntry(
                worker_id="worker-01", allowed_toolsets=("terminal", "file"), max_concurrent=2
            )
        }
    )
    tok = auth.exchange_bootstrap("boot-abc", "worker-01")
    claims = auth.verify_worker_jwt(tok)
    assert claims.sub == "worker-01"
    assert claims.allowed_toolsets == ["terminal", "file"]
    assert claims.max_concurrent == 2


def test_exchange_bootstrap_rejects_unknown_token():
    auth = make_auth(
        bootstrap_tokens={"boot-abc": BootstrapEntry(worker_id="worker-01")}
    )
    with pytest.raises(AuthError):
        auth.exchange_bootstrap("boot-nope", "worker-01")


def test_exchange_bootstrap_rejects_worker_id_mismatch():
    auth = make_auth(
        bootstrap_tokens={"boot-abc": BootstrapEntry(worker_id="worker-01")}
    )
    with pytest.raises(AuthError):
        auth.exchange_bootstrap("boot-abc", "worker-02")


# --- empty secret (fail-closed) ---------------------------------------------


def test_reject_empty_secret_direct_construction():
    with pytest.raises(AuthError, match="empty"):
        Auth(secret="", issuer=ISSUER, audience=AUDIENCE)


def test_reject_empty_secret_from_config():
    with pytest.raises(AuthError, match="empty"):
        Auth.from_config(HubConfig())


# --- HubConfig wiring -------------------------------------------------------


def test_auth_from_hub_config():
    cfg = HubConfig(
        jwt_secret=SECRET,
        bootstrap_tokens={"boot-abc": BootstrapEntry(worker_id="worker-01")},
    )
    auth = Auth.from_config(cfg)
    tok = auth.exchange_bootstrap("boot-abc", "worker-01")
    claims = auth.verify_worker_jwt(tok)
    assert claims.sub == "worker-01"


def test_hub_config_jwt_defaults():
    cfg = HubConfig()
    assert cfg.jwt_secret == ""
    assert cfg.jwt_issuer == "hermes-relay-hub"
    assert cfg.jwt_audience == "task-relay-hub"
    assert cfg.jwt_ttl_seconds == 3600
    assert cfg.bootstrap_tokens == {}
