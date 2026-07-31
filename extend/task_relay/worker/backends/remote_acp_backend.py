"""Remote ACP backend via JSON-RPC to a co-located Hermes ACP process."""

from __future__ import annotations

import asyncio
import json
import logging
import uuid
from dataclasses import dataclass
from typing import Any
from urllib import request as urlrequest

from extend.task_relay.constants import CANCEL_REASON_TIMEOUT
from extend.task_relay.worker.task_executor import (
    OnCheckpoint,
    OnProgress,
    TaskBackend,
    TaskCompletePayload,
    TaskRunPayload,
)

logger = logging.getLogger("task_relay.worker.backends.remote_acp")


@dataclass(frozen=True)
class RemoteAcpBackendConfig:
    endpoint_url: str
    request_timeout_seconds: float = 600.0
    cancel_timeout_seconds: float = 5.0


class RemoteAcpBackend(TaskBackend):
    """Execute tasks by delegating to a local JSON-RPC ACP endpoint."""

    def __init__(self, config: RemoteAcpBackendConfig):
        if not config.endpoint_url:
            raise ValueError("RemoteAcpBackend requires endpoint_url")
        self._config = config
        self._active_runs: dict[str, asyncio.Task] = {}

    async def run(
        self,
        run: TaskRunPayload,
        on_progress: OnProgress,
        on_checkpoint: OnCheckpoint,
        cancel_event: asyncio.Event,
    ) -> TaskCompletePayload:
        run_id = str(uuid.uuid4())
        worker_task = asyncio.create_task(
            self._rpc(
                "acp.run",
                {
                    "task_id": run.task_id,
                    "run_id": run_id,
                    "goal": run.goal,
                    "params": run.params or {},
                    "toolsets": run.toolsets,
                    "timeout_seconds": run.timeout_seconds,
                },
            )
        )
        self._active_runs[run_id] = worker_task
        try:
            await on_progress(f"remote ACP run started ({run_id})")
            while not worker_task.done():
                if cancel_event.is_set():
                    await self._cancel_remote(run_id, cancel_event)
                    worker_task.cancel()
                    try:
                        await worker_task
                    except asyncio.CancelledError:
                        pass
                    reason = getattr(cancel_event, "reason", None)
                    if reason == CANCEL_REASON_TIMEOUT:
                        return TaskCompletePayload(
                            status="failed",
                            summary="execution timeout",
                            error="execution timeout",
                        )
                    return TaskCompletePayload(
                        status="cancelled",
                        summary=reason or "cancelled",
                    )
                await asyncio.sleep(0.1)

            result = await worker_task
            return self._payload_from_result(result, run)
        finally:
            self._active_runs.pop(run_id, None)

    async def _cancel_remote(
        self, run_id: str, cancel_event: asyncio.Event
    ) -> None:
        reason = getattr(cancel_event, "reason", None) or "cancel requested"
        try:
            await asyncio.wait_for(
                self._rpc("acp.cancel", {"run_id": run_id, "reason": reason}),
                timeout=self._config.cancel_timeout_seconds,
            )
        except Exception:
            logger.warning("remote ACP cancel failed for run %s", run_id, exc_info=True)

    async def _rpc(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        body = json.dumps(
            {"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
        ).encode("utf-8")
        req = urlrequest.Request(
            self._config.endpoint_url,
            data=body,
            headers={"Content-Type": "application/json"},
            method="POST",
        )

        def _call() -> dict[str, Any]:
            with urlrequest.urlopen(req, timeout=self._config.request_timeout_seconds) as resp:
                payload = json.loads(resp.read().decode("utf-8"))
            if "error" in payload:
                raise RuntimeError(payload["error"])
            result = payload.get("result")
            return result if isinstance(result, dict) else {"result": result}

        return await asyncio.to_thread(_call)

    @staticmethod
    def _payload_from_result(
        result: dict[str, Any], run: TaskRunPayload
    ) -> TaskCompletePayload:
        status = str(result.get("status") or "completed")
        summary = result.get("summary") or result.get("final_response") or ""
        fields = result.get("fields")
        if isinstance(fields, dict) and run.params:
            merged = dict(fields)
            merged.setdefault("params", run.params)
            fields = merged
        return TaskCompletePayload(
            status=status,
            summary=str(summary),
            result_text=result.get("result_text") or str(summary),
            fields=fields if isinstance(fields, dict) else None,
            usage=result.get("usage") if isinstance(result.get("usage"), dict) else None,
            error=result.get("error"),
        )
