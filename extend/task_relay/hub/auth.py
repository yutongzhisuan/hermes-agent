"""Hub auth (M1): bootstrap-token exchange + short-lived JWTs, HS256.

M1 baseline per the design spec §Security:
- Worker JWT (scoped ``allowed_toolsets`` / ``max_concurrent``) travels as a
  bearer on the WS upgrade, never in the ``worker.announce`` body.
- Master JWT travels on gRPC metadata for master-facing RPCs.
- Workers obtain their JWT by presenting a long-lived bootstrap credential
  (from ``HubConfig.bootstrap_tokens``) once, then refresh before ``exp``.

Optional mTLS transport hardening lives in ``hub/tls.py`` (M3); JWT remains
the application-layer auth baseline here.

Worker JWT claim keys (exact, per spec):

    {
      "sub": "worker-01",
      "aud": "task-relay-hub",
      "iss": "xhermes-relay-hub",
      "allowed_toolsets": ["terminal", "file"],
      "max_concurrent": 2,
      "exp": 1710003600
    }

Master JWTs carry the standard ``sub``/``aud``/``iss``/``exp`` plus
``role: "master"`` so the two token populations cannot be confused.
"""

from dataclasses import dataclass
import time

import jwt

from extend.task_relay.hub.config import BootstrapEntry, HubConfig

ALGORITHM = "HS256"
_MASTER_ROLE = "master"


class AuthError(Exception):
    """Any verification or exchange failure (bad signature, wrong aud/iss,
    expired, unknown bootstrap token, ...)."""


@dataclass(frozen=True)
class WorkerClaims:
    sub: str
    allowed_toolsets: list[str]
    max_concurrent: int
    exp: int


@dataclass(frozen=True)
class MasterClaims:
    sub: str
    exp: int


class Auth:
    def __init__(
        self,
        secret: str,
        issuer: str,
        audience: str,
        bootstrap_tokens: dict[str, BootstrapEntry] | None = None,
        default_ttl_s: int = 3600,
    ):
        if not secret:
            raise AuthError("empty jwt secret: refusing to sign/verify HS256 with no key")
        self._secret = secret
        self._issuer = issuer
        self._audience = audience
        self._bootstrap_tokens = dict(bootstrap_tokens or {})
        self._default_ttl_s = default_ttl_s

    @classmethod
    def from_config(cls, cfg: HubConfig) -> "Auth":
        return cls(
            secret=cfg.jwt_secret,
            issuer=cfg.jwt_issuer,
            audience=cfg.jwt_audience,
            bootstrap_tokens=dict(cfg.bootstrap_tokens),
            default_ttl_s=cfg.jwt_ttl_seconds,
        )

    # --- issue --------------------------------------------------------------

    def issue_worker_jwt(
        self,
        worker_id: str,
        allowed_toolsets: list[str],
        max_concurrent: int,
        ttl_s: int | None = None,
    ) -> str:
        ttl = self._default_ttl_s if ttl_s is None else ttl_s
        claims = {
            "sub": worker_id,
            "aud": self._audience,
            "iss": self._issuer,
            "allowed_toolsets": list(allowed_toolsets),
            "max_concurrent": max_concurrent,
            "exp": int(time.time()) + ttl,
        }
        return jwt.encode(claims, self._secret, algorithm=ALGORITHM)

    def issue_master_jwt(self, master_id: str, ttl_s: int | None = None) -> str:
        ttl = self._default_ttl_s if ttl_s is None else ttl_s
        claims = {
            "sub": master_id,
            "aud": self._audience,
            "iss": self._issuer,
            "role": _MASTER_ROLE,
            "exp": int(time.time()) + ttl,
        }
        return jwt.encode(claims, self._secret, algorithm=ALGORITHM)

    # --- verify -------------------------------------------------------------

    def _decode(self, token: str) -> dict:
        try:
            return jwt.decode(
                token,
                self._secret,
                algorithms=[ALGORITHM],
                audience=self._audience,
                issuer=self._issuer,
                options={"require": ["sub", "aud", "iss", "exp"]},
            )
        except jwt.PyJWTError as e:
            raise AuthError(str(e)) from e

    def verify_worker_jwt(self, token: str) -> WorkerClaims:
        payload = self._decode(token)
        if "allowed_toolsets" not in payload or "max_concurrent" not in payload:
            raise AuthError("not a worker token: missing scope claims")
        return WorkerClaims(
            sub=payload["sub"],
            allowed_toolsets=list(payload["allowed_toolsets"]),
            max_concurrent=payload["max_concurrent"],
            exp=payload["exp"],
        )

    def verify_master_jwt(self, token: str) -> MasterClaims:
        payload = self._decode(token)
        if payload.get("role") != _MASTER_ROLE:
            raise AuthError("not a master token")
        return MasterClaims(sub=payload["sub"], exp=payload["exp"])

    # --- bootstrap exchange ---------------------------------------------------

    def exchange_bootstrap(self, bootstrap_token: str, worker_id: str) -> str:
        entry = self._bootstrap_tokens.get(bootstrap_token)
        if entry is None:
            raise AuthError("unknown bootstrap token")
        if entry.worker_id != worker_id:
            raise AuthError("bootstrap token not issued for this worker_id")
        return self.issue_worker_jwt(
            worker_id,
            list(entry.allowed_toolsets),
            entry.max_concurrent,
        )
