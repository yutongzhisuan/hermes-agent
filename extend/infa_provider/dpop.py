"""Device-bound DPoP helpers matching server/pkg/consumerauth."""

from __future__ import annotations

import base64
import hashlib
import json
import secrets
from typing import Any, Mapping

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

PROVIDER_ID = "infa"


def b64url_encode(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode("ascii")


def b64url_decode(text: str) -> bytes:
    pad = "=" * ((4 - len(text) % 4) % 4)
    return base64.urlsafe_b64decode(text + pad)


def token_hash(token: str) -> str:
    return b64url_encode(hashlib.sha256(token.encode("utf-8")).digest())


def body_hash(body: bytes) -> str:
    return hashlib.sha256(body).hexdigest()


def signing_message(proof: Mapping[str, Any]) -> str:
    return "\n".join(
        [
            str(proof.get("sid") or ""),
            str(proof.get("ath") or ""),
            str(proof.get("htm") or "").upper(),
            str(proof.get("htu") or ""),
            str(proof.get("body_sha256") or ""),
            str(proof.get("nonce") or ""),
            str(proof.get("token_jti") or ""),
            str(proof.get("jti") or ""),
            str(proof.get("iat") or ""),
        ]
    )


def generate_device_keypair() -> tuple[str, str]:
    key = Ed25519PrivateKey.generate()
    private_raw = key.private_bytes_raw()
    public_raw = key.public_key().public_bytes_raw()
    return b64url_encode(public_raw), b64url_encode(private_raw)


def load_private_key(private_b64: str) -> Ed25519PrivateKey:
    raw = b64url_decode(private_b64)
    if len(raw) == 64:
        raw = raw[:32]
    return Ed25519PrivateKey.from_private_bytes(raw)


def encode_proof(fields: Mapping[str, Any], private_b64: str) -> str:
    proof = {
        "sid": fields["sid"],
        "ath": fields["ath"],
        "htm": str(fields["htm"]).upper(),
        "htu": fields["htu"],
        "body_sha256": fields["body_sha256"],
        "nonce": fields["nonce"],
        "token_jti": fields["token_jti"],
        "jti": fields["jti"],
        "iat": int(fields["iat"]),
    }
    signature = load_private_key(private_b64).sign(signing_message(proof).encode("utf-8"))
    proof["signature"] = b64url_encode(signature)
    return b64url_encode(json.dumps(proof, separators=(",", ":"), ensure_ascii=False).encode("utf-8"))


def new_proof_id() -> str:
    return b64url_encode(secrets.token_bytes(12))


def jwt_unverified_claims(token: str) -> dict[str, Any]:
    parts = token.split(".")
    if len(parts) < 2:
        return {}
    try:
        payload = json.loads(b64url_decode(parts[1]))
    except (ValueError, json.JSONDecodeError):
        return {}
    return payload if isinstance(payload, dict) else {}
