#!/usr/bin/env python3
"""Start a live Task Relay Hub + stub worker for Go Master SDK E2E tests.

Prints one JSON config line to stdout, then runs until SIGTERM.
"""

from __future__ import annotations

import asyncio
import json
import signal
import sys
import tempfile
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT))

from extend.task_relay.hub.auth import Auth
from extend.task_relay.hub.bootstrap import start_ws_server, wire_orchestration
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.event_bus import EventBus
from extend.task_relay.hub.grpc_server import serve_grpc
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.worker.backends.stub_backend import StubBackend, StubBackendConfig
from extend.task_relay.worker.task_worker import TaskWorker

SECRET = "t" * 32
WORKER_ID = "go-e2e-worker"


async def main() -> None:
    shutdown = asyncio.Event()

    def _stop(*_args: object) -> None:
        shutdown.set()

    signal.signal(signal.SIGTERM, _stop)
    signal.signal(signal.SIGINT, _stop)

    with tempfile.TemporaryDirectory() as tmp:
        db = await open_db(str(Path(tmp) / "relay.db"))
        cfg = HubConfig(jwt_secret=SECRET)
        auth = Auth(secret=SECRET, issuer=cfg.jwt_issuer, audience=cfg.jwt_audience)
        bus = EventBus(db, cfg)
        registry = WorkerRegistry(db)
        router = TaskRouter(db, bus, cfg, registry)
        wire_orchestration(router, db, bus)

        grpc_server = await serve_grpc(
            router, auth, cfg, db, bus, registry, host="127.0.0.1", port=0
        )
        ws_server = await start_ws_server(
            router, auth, registry, db, cfg, host="127.0.0.1", port=0
        )

        grpc_port = grpc_server._server.sockets[0].getsockname()[1]
        ws_port = ws_server.sockets[0].getsockname()[1]
        ws_url = f"ws://127.0.0.1:{ws_port}"
        grpc_addr = f"127.0.0.1:{grpc_port}"

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

        async def ticker() -> None:
            while not shutdown.is_set():
                await router.tick_timeouts()
                try:
                    await asyncio.wait_for(shutdown.wait(), timeout=0.25)
                except asyncio.TimeoutError:
                    pass

        ticker_task = asyncio.create_task(ticker())

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

        shutdown.set()
        await ticker_task
        ws_server.close()
        grpc_server.close()
        await ws_server.wait_closed()
        await grpc_server.wait_closed()
        await db.close()


if __name__ == "__main__":
    asyncio.run(main())
