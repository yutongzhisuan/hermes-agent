"""Shared Hub wiring for production entry points and integration tests."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Awaitable

from extend.task_relay.hub.auth import Auth
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import Database
from extend.task_relay.hub.delivery import DeliveryCoordinator
from extend.task_relay.hub.task_router import TaskRouter
from extend.task_relay.hub.wake_scheduler import WakeScheduler
from extend.task_relay.hub.worker_registry import WorkerRegistry
from extend.task_relay.hub.ws_server import WsHubServer, serve_ws


@dataclass(frozen=True)
class DeliveryRuntime:
    """Delivery coordinator and Mode B wake scheduler wired to a router."""

    delivery: DeliveryCoordinator
    wake: WakeScheduler


def wire_delivery(
    router: TaskRouter,
    registry: WorkerRegistry,
    db: Database,
    auth: Auth,
    config: HubConfig,
    *,
    relay_ws_url: str,
) -> DeliveryRuntime:
    """Attach delivery + wake to ``router`` and return the runtime handles."""
    delivery = DeliveryCoordinator(router, registry, db, auth, config)
    router.set_delivery(delivery)
    wake = WakeScheduler(
        db,
        registry,
        auth,
        config,
        relay_ws_url=relay_ws_url,
    )
    delivery._wake = wake
    return DeliveryRuntime(delivery=delivery, wake=wake)


async def start_ws_server(
    router: TaskRouter,
    auth: Auth,
    registry: WorkerRegistry,
    db: Database,
    config: HubConfig,
    *,
    delivery: DeliveryCoordinator | None = None,
    wake: WakeScheduler | None = None,
    relay_ws_url: str | None = None,
    host: str = "127.0.0.1",
    port: int = 0,
    **kwargs: Any,
) -> Any:
    """Start a WS server with delivery wired; returns the websockets server."""
    if delivery is None:
        ws_url = relay_ws_url or f"ws://{host}:{port if port else 9000}"
        runtime = wire_delivery(
            router, registry, db, auth, config, relay_ws_url=ws_url
        )
        delivery = runtime.delivery
        wake = wake or runtime.wake

    _, coro = serve_ws(
        router,
        auth,
        registry,
        db,
        config,
        delivery=delivery,
        wake=wake,
        host=host,
        port=port,
        **kwargs,
    )
    return await coro


def serve_ws_with_delivery(
    router: TaskRouter,
    auth: Auth,
    registry: WorkerRegistry,
    db: Database,
    config: HubConfig,
    *,
    relay_ws_url: str,
    host: str = "127.0.0.1",
    port: int = 0,
    **kwargs: Any,
) -> tuple[WsHubServer, Awaitable, DeliveryRuntime]:
    """Like :func:`start_ws_server` but returns ``(hub, coro, runtime)`` for main."""
    runtime = wire_delivery(
        router, registry, db, auth, config, relay_ws_url=relay_ws_url
    )
    hub, coro = serve_ws(
        router,
        auth,
        registry,
        db,
        config,
        delivery=runtime.delivery,
        wake=runtime.wake,
        host=host,
        port=port,
        **kwargs,
    )
    return hub, coro, runtime
