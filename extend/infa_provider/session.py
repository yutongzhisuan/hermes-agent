"""INFA consumer-session store in ~/.xhermes/auth.json."""

from __future__ import annotations

import time
from dataclasses import asdict, dataclass
from typing import Any, Mapping, Optional

from extend.infa_provider.dpop import PROVIDER_ID, jwt_unverified_claims

ENV_GATEWAY_URL = "INFA_GATEWAY_BASE_URL"
ENV_PLATFORM_URL = "INFA_PLATFORM_BASE_URL"
REFRESH_SKEW_SECONDS = 120
DESKTOP_MANAGED_BY = "desktop"


class InfaAuthError(Exception):
    def __init__(self, message: str, *, code: str = "infa_auth_failed"):
        super().__init__(message)
        self.code = code


@dataclass
class InfaSession:
    session_id: str
    access_token: str
    refresh_token: str
    device_id: str
    device_private_key: str
    device_public_key: str
    platform_base_url: str
    gateway_base_url: str
    inference_base_url: str
    expires_at: int = 0
    managed_by: str = ""

    def to_dict(self) -> dict[str, Any]:
        payload = asdict(self)
        payload["logged_in"] = True
        payload["auth_mode"] = "consumer_session"
        if not payload.get("managed_by"):
            payload.pop("managed_by", None)
        return payload

    @classmethod
    def from_dict(cls, data: Mapping[str, Any]) -> InfaSession:
        return cls(
            session_id=str(data.get("session_id") or ""),
            access_token=str(data.get("access_token") or ""),
            refresh_token=str(data.get("refresh_token") or ""),
            device_id=str(data.get("device_id") or ""),
            device_private_key=str(data.get("device_private_key") or ""),
            device_public_key=str(data.get("device_public_key") or ""),
            platform_base_url=_strip_slash(data.get("platform_base_url")),
            gateway_base_url=_strip_slash(data.get("gateway_base_url")),
            inference_base_url=_strip_slash(data.get("inference_base_url")),
            expires_at=int(data.get("expires_at") or 0),
            managed_by=str(data.get("managed_by") or ""),
        )


def _strip_slash(value: Any) -> str:
    return str(value or "").strip().rstrip("/")


def inference_url(gateway_base_url: str) -> str:
    return _strip_slash(gateway_base_url) + "/api/v1"


def access_token_is_expiring(
    session: InfaSession,
    *,
    skew_seconds: int = REFRESH_SKEW_SECONDS,
    now: Optional[float] = None,
) -> bool:
    stamp = now if now is not None else time.time()
    exp = session.expires_at or int(jwt_unverified_claims(session.access_token).get("exp") or 0)
    return not exp or float(exp) <= stamp + max(0, skew_seconds)


def _auth_json_path():
    from hermes_cli.config import get_hermes_home

    return get_hermes_home() / "auth.json"


def save_session(session: InfaSession, *, store: Optional[dict[str, Any]] = None) -> None:
    payload = session.to_dict()
    if store is not None:
        store.clear()
        store.update(payload)
        return
    from hermes_cli.auth import _auth_store_lock, _load_auth_store, _save_auth_store, _save_provider_state

    path = _auth_json_path()
    with _auth_store_lock(target_path=path):
        auth_store = _load_auth_store(path)
        _save_provider_state(auth_store, PROVIDER_ID, payload)
        _save_auth_store(auth_store, target_path=path)


def load_session(*, store: Optional[Mapping[str, Any]] = None) -> Optional[InfaSession]:
    if store is not None:
        if not store.get("access_token"):
            return None
        return InfaSession.from_dict(store)
    try:
        from hermes_cli.auth import _load_auth_store
    except Exception:
        return None
    providers = _load_auth_store(_auth_json_path()).get("providers")
    state = providers.get(PROVIDER_ID) if isinstance(providers, dict) else None
    if not isinstance(state, dict) or not state.get("access_token") or not state.get("device_private_key"):
        return None
    return InfaSession.from_dict(state)


def _config_api_credentials() -> Optional[dict[str, str]]:
    """Read OpenAI-compatible api_key + base_url from config.yaml, if both set."""
    try:
        from hermes_cli.config import load_config

        cfg = load_config()
    except Exception:
        return None
    model = cfg.get("model") if isinstance(cfg, dict) else None
    if not isinstance(model, dict):
        return None
    api_key = str(model.get("api_key") or "").strip()
    base_url = _strip_slash(model.get("base_url"))
    if not api_key or not base_url:
        return None
    # Local UDS endpoints are not INFA consumer credentials.
    try:
        from extend.unix_socket_http import is_unix_base_url

        if is_unix_base_url(base_url):
            return None
    except Exception:
        if str(base_url).lower().startswith("unix://"):
            return None
    return {"api_key": api_key, "base_url": base_url}


def _desktop_owns_refresh(session: InfaSession) -> bool:
    return session.managed_by == DESKTOP_MANAGED_BY


def resolve_runtime_credentials(*, force_refresh: bool = False, refresh_if_expiring: bool = True) -> dict[str, Any]:
    from extend.infa_provider.login import refresh_session

    session = load_session()
    if session is not None:
        # Desktop writes auth.json and rotates the shared refresh token.
        # Refreshing it here presents the previous token and revokes the session.
        if not _desktop_owns_refresh(session) and (
            force_refresh or (refresh_if_expiring and access_token_is_expiring(session))
        ):
            session = refresh_session(session)
            save_session(session)
        return {
            "provider": PROVIDER_ID,
            "api_key": session.access_token,
            "base_url": session.inference_base_url,
            "source": "xhermes-auth-store",
            "session": session,
        }
    config_creds = _config_api_credentials()
    if config_creds:
        return {
            "provider": PROVIDER_ID,
            "api_key": config_creds["api_key"],
            "base_url": config_creds["base_url"],
            "source": "config",
            "session": None,
        }
    raise InfaAuthError(
        "No INFA consumer session. Run `xhermes auth add infa`, "
        "or set model.api_key and model.base_url in config.yaml.",
        code="not_logged_in",
    )


from extend.infa_provider.login import interactive_login, login, refresh_session  # noqa: E402
