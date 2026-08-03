#!/usr/bin/env python3
"""Start Go Task Relay Hub + Python stub worker for Go Master SDK E2E tests.

Prints one JSON config line to stdout, then runs until SIGTERM.
"""

from __future__ import annotations

import asyncio
import json
import signal
import socket
import subprocess
import sys
import tempfile
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT))

from extend.task_relay.hub.auth import Auth
from extend.task_relay.worker.backends.stub_backend import StubBackend, StubBackendConfig
from extend.task_relay.worker.task_worker import TaskWorker

SECRET = "t" * 32
WORKER_ID = "go-e2e-worker"
HUB_GO = REPO_ROOT / "extend" / "task_relay" / "hub" / "go"


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


async def main() -> None:
    shutdown = asyncio.Event()

    def _stop(*_args: object) -> None:
        shutdown.set()

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

    grpc_port = _free_port()
    ws_port = _free_port()

    with tempfile.TemporaryDirectory() as tmp:
        db_path = Path(tmp) / "relay.db"
        hub_bin = HUB_GO / "task-relay-hub"
        hub_proc = subprocess.Popen(
            [
                str(hub_bin),
                f"--host=127.0.0.1",
                f"--grpc-port={grpc_port}",
                f"--ws-port={ws_port}",
                f"--db={db_path}",
                f"--jwt-secret={SECRET}",
            ],
            cwd=HUB_GO,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
        )

        async def wait_for_hub() -> None:
            for _ in range(100):
                if hub_proc.poll() is not None:
                    err = hub_proc.stderr.read().decode() if hub_proc.stderr else ""
                    raise RuntimeError(f"go hub exited early: {err}")
                with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
                    if sock.connect_ex(("127.0.0.1", grpc_port)) == 0:
                        return
                await asyncio.sleep(0.05)
            raise RuntimeError("go hub did not become ready")

        await wait_for_hub()

        ws_url = f"ws://127.0.0.1:{ws_port}"
        grpc_addr = f"127.0.0.1:{grpc_port}"
        auth = Auth(secret=SECRET, issuer="xhermes-relay-hub", audience="task-relay-hub")
        worker_jwt = auth.issue_worker_jwt(WORKER_ID, [], max_concurrent=1, ttl_s=3600)
        master_jwt = auth.issue_master_jwt("go-e2e-master", ttl_s=3600)

        worker = TaskWorker(
            worker_id=WORKER_ID,
            relay_url=ws_url,
            jwt=worker_jwt,
            backend=StubBackend(StubBackendConfig(sleep_seconds=0.01)),
            session_modes=["a"],
            max_concurrent=1,
        )
        worker_task = asyncio.create_task(worker.run())

        print(
            json.dumps(
                {
                    "grpc_addr": grpc_addr,
                    "ws_url": ws_url,
                    "master_jwt": master_jwt,
                    "worker_id": WORKER_ID,
                }
            ),
            flush=True,
        )

        await shutdown.wait()

        await worker.shutdown()
        worker_task.cancel()
        try:
            await worker_task
        except asyncio.CancelledError:
            pass

        hub_proc.terminate()
        try:
            hub_proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            hub_proc.kill()


if __name__ == "__main__":
    asyncio.run(main())
