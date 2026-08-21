"""HTTP JSON-RPC server exposing XHermes ACP execution to RemoteAcpTaskBackend.

Owned by XHermes (migrated from swarm-network ``worker/acp_rpc_server.py``):
runs as a node-local sidecar (default 127.0.0.1:9105) wrapping
:class:`~extend.task_relay.acp_backend.AcpTaskBackend`. Start it with
``python -m extend.task_relay.acp_rpc_server``.

Untrusted remote tasks should be served with ``--stateless`` (no access to
the local user's memories, skills, or session history; disposable session
and workdir) and, on Docker-capable nodes, ``--sandbox docker`` (each task
in its own network-less, resource-capped disposable container).
"""

from __future__ import annotations

import argparse
import asyncio
import json
import logging
from dataclasses import dataclass, field
from typing import Any

from aiohttp import web

from extend.task_relay.task_types import TaskBackend, TaskCancelEvent, TaskRunPayload

logger = logging.getLogger("task_relay.worker.acp_rpc")


@dataclass
class _ActiveRun:
    task: asyncio.Task
    cancel_event: TaskCancelEvent
    backend: TaskBackend


@dataclass
class AcpRpcState:
    backend: TaskBackend
    runs: dict[str, _ActiveRun] = field(default_factory=dict)


async def _handle_rpc(request: web.Request) -> web.Response:
    state: AcpRpcState = request.app["state"]
    try:
        payload = await request.json()
    except json.JSONDecodeError:
        return web.json_response(
            {"jsonrpc": "2.0", "id": None, "error": {"code": -32700, "message": "parse error"}},
            status=400,
        )

    msg_id = payload.get("id")
    method = payload.get("method")
    params = payload.get("params") or {}
    if not isinstance(params, dict):
        params = {}

    try:
        if method == "acp.run":
            result = await _acp_run(state, params)
        elif method == "acp.cancel":
            result = await _acp_cancel(state, params)
        elif method == "acp.status":
            result = _acp_status(state, params)
        else:
            return web.json_response(
                {
                    "jsonrpc": "2.0",
                    "id": msg_id,
                    "error": {"code": -32601, "message": f"method not found: {method}"},
                }
            )
    except Exception as exc:
        logger.exception("ACP RPC handler failed for %s", method)
        return web.json_response(
            {
                "jsonrpc": "2.0",
                "id": msg_id,
                "error": {"code": -32000, "message": str(exc)},
            },
            status=500,
        )

    return web.json_response({"jsonrpc": "2.0", "id": msg_id, "result": result})


async def _acp_run(state: AcpRpcState, params: dict[str, Any]) -> dict[str, Any]:
    run_id = str(params.get("run_id") or "")
    if not run_id:
        raise ValueError("run_id is required")

    if run_id in state.runs:
        raise ValueError(f"run_id already active: {run_id}")

    run = TaskRunPayload(
        task_id=str(params.get("task_id") or run_id),
        attempt=int(params.get("attempt") or 1),
        goal=str(params.get("goal") or ""),
        params=params.get("params") if isinstance(params.get("params"), dict) else {},
        context=params.get("context") if isinstance(params.get("context"), dict) else None,
        toolsets=list(params.get("toolsets") or []),
        timeout_seconds=int(params.get("timeout_seconds") or 600),
        first_progress_seconds=params.get("first_progress_seconds"),
        trace_context=params.get("trace_context"),
        resume_from_checkpoint=params.get("resume_from_checkpoint"),
        resume_blob=params.get("resume_blob"),
    )
    cancel_event = TaskCancelEvent()

    async def _noop_progress(_summary: str) -> None:
        return None

    last_checkpoint: dict[str, Any] | None = None

    async def _capture_checkpoint(*args: Any, **kwargs: Any) -> None:
        nonlocal last_checkpoint
        if args:
            kwargs.setdefault("checkpoint_id", args[0])
            if len(args) > 1:
                kwargs.setdefault("summary", args[1])
            if len(args) > 2:
                kwargs.setdefault("fields", args[2])
            if len(args) > 3:
                kwargs.setdefault("resume_blob", args[3])
        checkpoint_id = str(kwargs.get("checkpoint_id") or kwargs.get("id") or "")
        if not checkpoint_id:
            return
        blob = kwargs.get("resume_blob")
        if isinstance(blob, bytes):
            blob = blob.decode("utf-8", errors="replace")
        fields = kwargs.get("fields")
        last_checkpoint = {
            "checkpoint_id": checkpoint_id,
            "summary": kwargs.get("summary") or "",
            "fields": fields if isinstance(fields, dict) else {},
            "resume_blob": blob or "",
        }

    async def _execute() -> dict[str, Any]:
        payload = await state.backend.run(run, _noop_progress, _capture_checkpoint, cancel_event)
        result = {
            "status": payload.status,
            "summary": payload.summary,
            "result_text": payload.result_text,
            "fields": payload.fields,
            "usage": payload.usage,
            "error": payload.error,
        }
        if last_checkpoint is not None:
            result["checkpoint"] = last_checkpoint
        return result

    task = asyncio.create_task(_execute())
    state.runs[run_id] = _ActiveRun(task=task, cancel_event=cancel_event, backend=state.backend)
    try:
        return await task
    finally:
        state.runs.pop(run_id, None)


async def _acp_cancel(state: AcpRpcState, params: dict[str, Any]) -> dict[str, Any]:
    run_id = str(params.get("run_id") or "")
    active = state.runs.get(run_id)
    if active is None:
        return {"cancelled": False, "reason": "run not found"}
    reason = str(params.get("reason") or "cancel requested")
    active.cancel_event.set(reason)
    return {"cancelled": True}


def _acp_status(state: AcpRpcState, params: dict[str, Any]) -> dict[str, Any]:
    run_id = str(params.get("run_id") or "")
    active = state.runs.get(run_id)
    if active is None:
        return {"running": False}
    return {"running": not active.task.done()}


def create_acp_rpc_app(
    *,
    backend: TaskBackend | None = None,
    progress_interval_seconds: float = 5.0,
    stateless: bool = False,
    stateless_toolsets: list[str] | None = None,
    state_root: str | None = None,
    workdir_root: str | None = None,
    sandbox: str | None = None,
    sandbox_image: str | None = None,
) -> web.Application:
    app = web.Application()
    if backend is None:
        from extend.task_relay.acp_backend import AcpTaskBackend

        backend = AcpTaskBackend(
            progress_interval_seconds=progress_interval_seconds,
            stateless=stateless,
            stateless_toolsets=stateless_toolsets,
            state_root=state_root,
            workdir_root=workdir_root,
            sandbox=sandbox,
            sandbox_image=sandbox_image,
        )
    app["state"] = AcpRpcState(backend=backend)
    app.router.add_post("/rpc", _handle_rpc)
    app.router.add_post("/", _handle_rpc)
    return app


async def serve_acp_rpc(
    *,
    host: str = "127.0.0.1",
    port: int = 9105,
    progress_interval_seconds: float = 5.0,
    stateless: bool = False,
    stateless_toolsets: list[str] | None = None,
    state_root: str | None = None,
    workdir_root: str | None = None,
    sandbox: str | None = None,
    sandbox_image: str | None = None,
) -> web.AppRunner:
    app = create_acp_rpc_app(
        progress_interval_seconds=progress_interval_seconds,
        stateless=stateless,
        stateless_toolsets=stateless_toolsets,
        state_root=state_root,
        workdir_root=workdir_root,
        sandbox=sandbox,
        sandbox_image=sandbox_image,
    )
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, host, port=port)
    await site.start()
    logger.info("ACP JSON-RPC listening on http://%s:%d/rpc", host, port)
    return runner


def _build_arg_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="XHermes ACP JSON-RPC server for Task Relay workers"
    )
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=9105)
    parser.add_argument(
        "--acp-progress-interval-seconds",
        type=float,
        default=5.0,
        help="minimum seconds between ACP progress frames",
    )
    parser.add_argument(
        "--stateless",
        action="store_true",
        help=(
            "run each task as a disposable session: no access to the local "
            "user's memories, skills, or session history; transcript and "
            "workdir are deleted when the task ends"
        ),
    )
    parser.add_argument(
        "--stateless-toolsets",
        default=None,
        help=(
            "comma-separated toolsets granted to stateless tasks when the "
            "task requests none (default: terminal,file,web,code_execution,todo)"
        ),
    )
    parser.add_argument(
        "--state-root",
        default=None,
        help="directory for the ephemeral stateless session store (default: fresh temp dir)",
    )
    parser.add_argument(
        "--workdir-root",
        default=None,
        help="parent directory for per-task temp workdirs (default: system temp)",
    )
    parser.add_argument(
        "--sandbox",
        choices=["docker"],
        default=None,
        help=(
            "run stateless tasks inside their own disposable Docker container "
            "(implies --stateless; requires Docker on the node)"
        ),
    )
    parser.add_argument(
        "--sandbox-image",
        default=None,
        help="Docker image for sandboxed tasks (default: hermes terminal default image)",
    )
    parser.add_argument(
        "--sandbox-network",
        action="store_true",
        help="allow container network access (default: no network)",
    )
    parser.add_argument(
        "--sandbox-cpu",
        type=float,
        default=None,
        help="CPU limit for sandboxed task containers (e.g. 2.0)",
    )
    parser.add_argument(
        "--sandbox-memory-mb",
        type=int,
        default=None,
        help="memory limit in MB for sandboxed task containers",
    )
    parser.add_argument(
        "--local-confined",
        action="store_true",
        help=(
            "trusted-task lightweight mode: implies --stateless and installs "
            "a default approvals.deny preset into the sidecar config "
            "(guardrails, not a security boundary)"
        ),
    )
    parser.add_argument(
        "--local-confined-extra-deny",
        default=None,
        help="comma-separated extra approvals.deny globs for --local-confined",
    )
    return parser


def _split_toolsets(raw: str | None) -> list[str] | None:
    if not raw:
        return None
    return [part.strip() for part in raw.split(",") if part.strip()]


async def _async_main(argv: list[str] | None) -> int:
    args = _build_arg_parser().parse_args(argv)
    logging.basicConfig(level=logging.INFO)
    stateless = args.stateless or bool(args.sandbox) or args.local_confined
    if args.local_confined:
        from extend.task_relay.stateless import apply_local_confined

        # Before serving: config.yaml is the approval policy surface.
        added = apply_local_confined(
            extra_deny_rules=_split_toolsets(args.local_confined_extra_deny)
        )
        logger.info("local-confined enabled: %d deny rules added", added)
    if args.sandbox:
        from extend.task_relay.stateless import apply_sandbox_env

        # Must run before the first agent/terminal environment is created.
        apply_sandbox_env(
            sandbox=args.sandbox,
            image=args.sandbox_image,
            network=args.sandbox_network,
            cpu=args.sandbox_cpu,
            memory_mb=args.sandbox_memory_mb,
        )
        logger.info(
            "sandbox enabled: backend=%s network=%s cpu=%s memory_mb=%s",
            args.sandbox,
            "on" if args.sandbox_network else "off",
            args.sandbox_cpu,
            args.sandbox_memory_mb,
        )
    runner = await serve_acp_rpc(
        host=args.host,
        port=args.port,
        progress_interval_seconds=args.acp_progress_interval_seconds,
        stateless=stateless,
        stateless_toolsets=_split_toolsets(args.stateless_toolsets),
        state_root=args.state_root,
        workdir_root=args.workdir_root,
        sandbox=args.sandbox,
        sandbox_image=args.sandbox_image,
    )
    try:
        await asyncio.Event().wait()
    except asyncio.CancelledError:
        pass
    finally:
        await runner.cleanup()
    return 0


def main(argv: list[str] | None = None) -> int:
    try:
        return asyncio.run(_async_main(list(argv) if argv is not None else None))
    except KeyboardInterrupt:
        return 0


if __name__ == "__main__":
    raise SystemExit(main())
