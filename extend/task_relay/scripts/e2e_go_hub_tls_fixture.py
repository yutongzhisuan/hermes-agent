#!/usr/bin/env python3
"""Start Go Task Relay Hub with TLS/mTLS for Go Master mTLS integration tests."""

from __future__ import annotations

import json
import signal
import socket
import subprocess
import sys
import tempfile
import time
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT))

from extend.task_relay.hub.auth import Auth
from extend.task_relay.tests.tls_helpers import generate_test_tls_material

SECRET = "t" * 32
HUB_GO = REPO_ROOT / "extend" / "task_relay" / "hub" / "go"
HUB_BIN = HUB_GO / "task-relay-hub"


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def main() -> None:
    shutdown = False

    def _stop(*_args: object) -> None:
        nonlocal shutdown
        shutdown = True

    signal.signal(signal.SIGTERM, _stop)
    signal.signal(signal.SIGINT, _stop)

    build = subprocess.run(
        ["go", "build", "-o", "task-relay-hub", "./cmd/task-relay-hub"],
        cwd=HUB_GO,
        check=False,
        capture_output=True,
        text=True,
    )
    if build.returncode != 0:
        sys.stderr.write(build.stderr or build.stdout)
        raise SystemExit(1)

    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        material = generate_test_tls_material(tmp_path)
        db_path = tmp_path / "relay.db"
        grpc_port = _free_port()
        ws_port = _free_port()
        hub_proc = subprocess.Popen(
            [
                str(HUB_BIN),
                f"--host=127.0.0.1",
                f"--grpc-port={grpc_port}",
                f"--ws-port={ws_port}",
                f"--db={db_path}",
                f"--jwt-secret={SECRET}",
                f"--tls-cert={material['server_cert']}",
                f"--tls-key={material['server_key']}",
                f"--tls-ca={material['ca']}",
                "--tls-require-client-cert",
            ],
            cwd=HUB_GO,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            text=True,
        )

        deadline = time.monotonic() + 5.0
        while time.monotonic() < deadline:
            if hub_proc.poll() is not None:
                err = hub_proc.stderr.read() if hub_proc.stderr else ""
                raise RuntimeError(f"go hub exited early: {err}")
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
                if sock.connect_ex(("127.0.0.1", grpc_port)) == 0:
                    break
            time.sleep(0.05)
        else:
            hub_proc.kill()
            raise RuntimeError("go hub did not become ready")

        auth = Auth(secret=SECRET, issuer="hermes-relay-hub", audience="task-relay-hub")
        master_jwt = auth.issue_master_jwt("go-hub-mtls-master", ttl_s=3600)
        print(
            json.dumps(
                {
                    "grpc_addr": f"127.0.0.1:{grpc_port}",
                    "master_jwt": master_jwt,
                    "tls_ca": str(material["ca"]),
                    "tls_cert": str(material["client_cert"]),
                    "tls_key": str(material["client_key"]),
                }
            ),
            flush=True,
        )

        while not shutdown:
            if hub_proc.poll() is not None:
                err = hub_proc.stderr.read() if hub_proc.stderr else ""
                raise RuntimeError(f"go hub exited: {err}")
            time.sleep(0.2)

        hub_proc.terminate()
        try:
            hub_proc.wait(timeout=3.0)
        except subprocess.TimeoutExpired:
            hub_proc.kill()


if __name__ == "__main__":
    main()
