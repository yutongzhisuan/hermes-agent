"""Worker-side TLS/mTLS client configuration."""

from __future__ import annotations

import ssl
from dataclasses import dataclass

from extend.task_relay.hub.tls import load_client_ssl_context


@dataclass(frozen=True)
class ClientTlsConfig:
    """Client TLS settings for worker WebSocket and token HTTP connections."""

    ca_file: str = ""
    cert_file: str = ""
    key_file: str = ""
    skip_hostname_verify: bool = False

    @property
    def enabled(self) -> bool:
        return bool(self.ca_file or self.cert_file or self.key_file)


def build_client_ssl_context(tls: ClientTlsConfig) -> ssl.SSLContext | None:
    """Return an SSL context for outbound Hub connections, or ``None`` when disabled."""
    if not tls.enabled:
        return None

    if bool(tls.cert_file) ^ bool(tls.key_file):
        raise ValueError("tls cert_file and key_file must be provided together")

    if tls.cert_file and not tls.ca_file:
        raise ValueError("tls ca_file is required when using a client certificate")

    if not tls.ca_file:
        ctx = ssl.create_default_context()
        ctx.check_hostname = not tls.skip_hostname_verify
        if tls.cert_file and tls.key_file:
            ctx.load_cert_chain(tls.cert_file, tls.key_file)
        return ctx

    return load_client_ssl_context(
        ca_file=tls.ca_file,
        cert_file=tls.cert_file or None,
        key_file=tls.key_file or None,
        check_hostname=not tls.skip_hostname_verify,
    )


def client_tls_from_args(args) -> ClientTlsConfig:
    """Build :class:`ClientTlsConfig` from worker CLI namespace."""
    return ClientTlsConfig(
        ca_file=getattr(args, "tls_ca", "") or "",
        cert_file=getattr(args, "tls_cert", "") or "",
        key_file=getattr(args, "tls_key", "") or "",
        skip_hostname_verify=bool(getattr(args, "tls_skip_hostname_verify", False)),
    )
