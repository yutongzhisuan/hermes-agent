"""HTTP token endpoint for worker JWT issuance and refresh (M1 baseline).

Workers exchange a long-lived bootstrap credential once, cache the issued JWT,
then refresh before ``exp`` by presenting the current JWT.
"""

from __future__ import annotations

import json
import logging
import time
from typing import Any

from aiohttp import web

from extend.task_relay.hub.auth import Auth, AuthError

logger = logging.getLogger("task_relay.hub.token")

TOKEN_PATH = "/v1/worker/token"


def _json_response(payload: dict, *, status: int = 200) -> web.Response:
    return web.Response(
        status=status,
        content_type="application/json",
        text=json.dumps(payload),
    )


def _error_response(message: str, *, status: int = 401) -> web.Response:
    return _json_response({"error": message}, status=status)


def _jwt_expires_at(token: str) -> int:
    import jwt as pyjwt

    payload = pyjwt.decode(token, options={"verify_signature": False})
    return int(payload["exp"])


async def _handle_issue_worker_token(request: web.Request) -> web.Response:
    auth: Auth = request.app["auth"]
    try:
        body = await request.json()
    except json.JSONDecodeError:
        return _error_response("invalid JSON body", status=400)

    if not isinstance(body, dict):
        return _error_response("JSON body must be an object", status=400)

    worker_jwt = body.get("worker_jwt")
    if worker_jwt:
        try:
            claims = auth.verify_worker_jwt(worker_jwt)
            token = auth.issue_worker_jwt(
                claims.sub,
                list(claims.allowed_toolsets),
                claims.max_concurrent,
            )
            return _json_response(
                {"worker_jwt": token, "expires_at": _jwt_expires_at(token)}
            )
        except AuthError as exc:
            return _error_response(str(exc))

    bootstrap_token = body.get("bootstrap_token")
    worker_id = body.get("worker_id")
    if not bootstrap_token or not worker_id:
        return _error_response(
            "bootstrap_token and worker_id are required when worker_jwt is absent",
            status=400,
        )

    try:
        token = auth.exchange_bootstrap(str(bootstrap_token), str(worker_id))
    except AuthError as exc:
        return _error_response(str(exc))

    return _json_response({"worker_jwt": token, "expires_at": _jwt_expires_at(token)})


def create_token_app(auth: Auth) -> web.Application:
    app = web.Application()
    app["auth"] = auth
    app.router.add_post(TOKEN_PATH, _handle_issue_worker_token)
    return app


async def serve_token_http(
    auth: Auth,
    *,
    host: str = "127.0.0.1",
    port: int = 9001,
) -> web.AppRunner:
    """Start the worker token HTTP server and return its runner."""
    app = create_token_app(auth)
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, host, port)
    await site.start()
    logger.info("worker token HTTP listening on http://%s:%d%s", host, port, TOKEN_PATH)
    return runner
