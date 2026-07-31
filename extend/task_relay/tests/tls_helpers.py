"""TLS/mTLS test helpers."""

from __future__ import annotations

import subprocess
from pathlib import Path


def generate_test_tls_material(tmp_path: Path) -> dict[str, Path]:
    """Generate a CA, server cert, and client cert for mTLS tests."""
    ca_key = tmp_path / "ca.key"
    ca_crt = tmp_path / "ca.crt"
    server_key = tmp_path / "server.key"
    server_csr = tmp_path / "server.csr"
    server_crt = tmp_path / "server.crt"
    client_key = tmp_path / "client.key"
    client_csr = tmp_path / "client.csr"
    client_crt = tmp_path / "client.crt"

    server_ext = tmp_path / "server.ext"
    server_ext.write_text("subjectAltName=IP:127.0.0.1,DNS:localhost\n", encoding="utf-8")

    def _run(args: list[str]) -> None:
        subprocess.run(args, check=True, capture_output=True)

    _run(
        [
            "openssl",
            "req",
            "-x509",
            "-newkey",
            "rsa:2048",
            "-nodes",
            "-keyout",
            str(ca_key),
            "-out",
            str(ca_crt),
            "-days",
            "1",
            "-subj",
            "/CN=test-ca",
        ]
    )
    _run(
        [
            "openssl",
            "req",
            "-newkey",
            "rsa:2048",
            "-nodes",
            "-keyout",
            str(server_key),
            "-out",
            str(server_csr),
            "-subj",
            "/CN=localhost",
        ]
    )
    _run(
        [
            "openssl",
            "x509",
            "-req",
            "-in",
            str(server_csr),
            "-CA",
            str(ca_crt),
            "-CAkey",
            str(ca_key),
            "-CAcreateserial",
            "-out",
            str(server_crt),
            "-days",
            "1",
            "-extfile",
            str(server_ext),
        ]
    )
    _run(
        [
            "openssl",
            "req",
            "-newkey",
            "rsa:2048",
            "-nodes",
            "-keyout",
            str(client_key),
            "-out",
            str(client_csr),
            "-subj",
            "/CN=test-client",
        ]
    )
    _run(
        [
            "openssl",
            "x509",
            "-req",
            "-in",
            str(client_csr),
            "-CA",
            str(ca_crt),
            "-CAkey",
            str(ca_key),
            "-CAcreateserial",
            "-out",
            str(client_crt),
            "-days",
            "1",
        ]
    )

    return {
        "ca": ca_crt,
        "server_cert": server_crt,
        "server_key": server_key,
        "client_cert": client_crt,
        "client_key": client_key,
    }
