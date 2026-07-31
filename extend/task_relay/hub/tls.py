"""TLS / mTLS helpers for Hub listeners (gRPC, WebSocket, HTTP token)."""

from __future__ import annotations

import logging
import ssl
from dataclasses import dataclass

logger = logging.getLogger("task_relay.hub.tls")


@dataclass(frozen=True)
class TlsConfig:
    """Server-side TLS settings. Empty cert/key means TLS is disabled."""

    cert_file: str = ""
    key_file: str = ""
    ca_file: str = ""
    require_client_cert: bool = False

    @property
    def enabled(self) -> bool:
        return bool(self.cert_file and self.key_file)


def load_server_ssl_context(tls: TlsConfig) -> ssl.SSLContext | None:
    """Build an ``SSLContext`` for server listeners, or ``None`` when disabled."""
    if not tls.enabled:
        return None

    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(tls.cert_file, tls.key_file)
    # gRPC requires h2; WebSocket and HTTP listeners need http/1.1 on the same cert.
    ctx.set_alpn_protocols(["h2", "http/1.1"])

    if tls.ca_file:
        ctx.load_verify_locations(tls.ca_file)
        ctx.verify_mode = (
            ssl.CERT_REQUIRED if tls.require_client_cert else ssl.CERT_OPTIONAL
        )
    elif tls.require_client_cert:
        raise ValueError("tls require_client_cert requires tls ca_file")

    logger.info(
        "TLS enabled (require_client_cert=%s)",
        tls.require_client_cert,
    )
    return ctx


def load_client_ssl_context(
    *,
    ca_file: str,
    cert_file: str | None = None,
    key_file: str | None = None,
    check_hostname: bool = True,
) -> ssl.SSLContext:
    """Build a client ``SSLContext`` for mTLS connections in tests/clients."""
    ctx = ssl.create_default_context(ssl.Purpose.SERVER_AUTH, cafile=ca_file)
    ctx.set_alpn_protocols(["h2", "http/1.1"])
    ctx.check_hostname = check_hostname
    if cert_file and key_file:
        ctx.load_cert_chain(cert_file, key_file)
    return ctx
