"""Master-facing gRPC TaskRelay service (M1).

Wires the generated ``TaskRelayBase`` to the hub's :class:`TaskRouter`,
:class:`EventBus`, and :class:`WorkerRegistry`. All RPCs require a valid
Master JWT on the ``authorization: Bearer …`` metadata.
"""

from __future__ import annotations

import base64
import contextvars
import json
import time
from typing import Any, Callable

from grpclib.const import Handler, Status
from grpclib.exceptions import GRPCError
from grpclib.server import Server, Stream

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayBase
from extend.task_relay.hub.auth import Auth, AuthError
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.event_bus import (
    CursorOutOfRangeError,
    EventFilter,
    SlowConsumerError,
)
from extend.task_relay.hub.models import Task, TaskSpec, Worker
from extend.task_relay.hub.task_router import (
    BatchDispatchResponse,
    DispatchTaskResponse,
    TaskRouter,
    TaskRouterError,
)
from extend.task_relay.hub.worker_registry import WorkerRegistry


_STATUS_TO_PROTO = {
    "pending": pb.TaskStatus.TASK_STATUS_PENDING,
    "running": pb.TaskStatus.TASK_STATUS_RUNNING,
    "completed": pb.TaskStatus.TASK_STATUS_COMPLETED,
    "failed": pb.TaskStatus.TASK_STATUS_FAILED,
    "lost": pb.TaskStatus.TASK_STATUS_LOST,
    "cancelled": pb.TaskStatus.TASK_STATUS_CANCELLED,
    # ``cancelling`` is an internal row status; to a Master it looks running.
    "cancelling": pb.TaskStatus.TASK_STATUS_RUNNING,
}

_KIND_TO_PROTO = {
    "STATUS": pb.TaskEventKind.TASK_EVENT_KIND_STATUS,
    "PROGRESS": pb.TaskEventKind.TASK_EVENT_KIND_PROGRESS,
    "TERMINAL": pb.TaskEventKind.TASK_EVENT_KIND_TERMINAL,
    "CHECKPOINT": pb.TaskEventKind.TASK_EVENT_KIND_CHECKPOINT,
    "AGGREGATE": pb.TaskEventKind.TASK_EVENT_KIND_AGGREGATE,
}

_SESSION_MODE_CHAR_TO_PROTO = {
    "A": pb.SessionMode.SESSION_MODE_A,
    "B": pb.SessionMode.SESSION_MODE_B,
    "C": pb.SessionMode.SESSION_MODE_C,
}


worker_identity: contextvars.ContextVar[str | None] = contextvars.ContextVar(
    "worker_identity", default=None
)


class MasterAuthInterceptor:
    """grpclib-style server interceptor that requires ``authorization: Bearer
    <master_jwt>`` on every RPC and sets ``worker_identity`` in context."""

    def __init__(self, auth: Auth):
        self._auth = auth

    def authenticate(self, metadata: Any) -> str:
        if metadata is None:
            raise GRPCError(Status.UNAUTHENTICATED, "missing authorization metadata")
        authorization = metadata.get("authorization")
        if not authorization:
            raise GRPCError(Status.UNAUTHENTICATED, "missing authorization header")
        parts = authorization.split(None, 1)
        if len(parts) != 2 or parts[0].lower() != "bearer":
            raise GRPCError(
                Status.UNAUTHENTICATED, "authorization requires Bearer scheme"
            )
        token = parts[1]
        try:
            claims = self._auth.verify_master_jwt(token)
        except AuthError as e:
            raise GRPCError(Status.UNAUTHENTICATED, str(e)) from e
        worker_identity.set(claims.sub)
        return claims.sub

    def intercept_service(self, service: TaskRelayBase) -> TaskRelayBase:
        return _InterceptedService(self, service)


class _InterceptedService:
    def __init__(self, interceptor: MasterAuthInterceptor, service: TaskRelayBase):
        self._interceptor = interceptor
        self._service = service

    def __mapping__(self) -> dict[str, Handler]:
        mapping = self._service.__mapping__()
        return {
            path: Handler(
                self._wrap(handler.func),
                handler.cardinality,
                handler.request_type,
                handler.reply_type,
            )
            for path, handler in mapping.items()
        }

    def _wrap(self, fn: Callable[[Stream], Any]) -> Callable[[Stream], Any]:
        async def wrapper(stream: Stream) -> None:
            self._interceptor.authenticate(stream.metadata)
            await fn(stream)

        return wrapper


class TaskRelayService(TaskRelayBase):
    def __init__(
        self,
        router: TaskRouter,
        auth: Auth,
        config: HubConfig,
        db: Database,
        bus: EventBus,
        registry: WorkerRegistry,
    ):
        self._router = router
        self._auth = auth
        self._config = config
        self._db = db
        self._bus = bus
        self._registry = registry

    # ------------------------------------------------------------------
    # RPCs
    # ------------------------------------------------------------------

    async def DispatchTask(self, stream: Stream) -> None:
        request: pb.DispatchTaskRequest = await stream.recv_message()
        spec = _spec_from_proto(request.spec)
        try:
            resp = await self._router.dispatch_task(
                spec,
                master_session_id=request.master_session_id or "",
                allow_redispatch=request.allow_redispatch,
            )
        except TaskRouterError as e:
            raise GRPCError(Status.INVALID_ARGUMENT, str(e)) from e
        await stream.send_message(_dispatch_response_to_proto(resp))

    async def DispatchTaskBatch(self, stream: Stream) -> None:
        request: pb.DispatchTaskBatchRequest = await stream.recv_message()
        specs = [_spec_from_proto(s) for s in request.specs]
        policy_json = _json_dumps(_message_to_dict(request.policy)) if request.HasField("policy") else None
        try:
            resp = await self._router.dispatch_task_batch(
                specs,
                batch_id=request.batch_id,
                master_session_id=request.master_session_id or "",
                callback_topic=request.callback_topic,
                allow_redispatch=request.allow_redispatch,
                policy_json=policy_json,
            )
        except TaskRouterError as e:
            raise GRPCError(Status.INVALID_ARGUMENT, str(e)) from e
        await stream.send_message(_batch_response_to_proto(resp))

    async def GetTaskResult(self, stream: Stream) -> None:
        request: pb.TaskResultRequest = await stream.recv_message()
        task = await self._db.get_task(request.task_id)
        if task is None:
            raise GRPCError(Status.NOT_FOUND, f"task {request.task_id} not found")
        await stream.send_message(_task_to_result_proto(task))

    async def WatchTask(self, stream: Stream) -> None:
        request: pb.WatchTaskRequest = await stream.recv_message()
        filter = _filter_from_request(request)
        since_event_id = request.since_event_id
        try:
            sub = await self._bus.subscribe(filter, since_event_id=since_event_id)
        except CursorOutOfRangeError as e:
            detail = pb.CursorOutOfRange(
                requested_since_event_id=e.requested,
                oldest_available_event_id=e.oldest,
                newest_event_id=e.newest,
            )
            raise GRPCError(
                Status.FAILED_PRECONDITION,
                str(e),
                details=[detail],
            ) from e
        try:
            async for event in sub:
                await stream.send_message(_event_to_proto(event))
                # A task-specific watch naturally ends when the task terminalizes.
                if filter.task_id is not None and event.kind == "TERMINAL":
                    break
        except SlowConsumerError as e:
            detail = pb.SlowConsumer(
                delivered_event_id=e.delivered,
                newest_event_id=e.newest,
            )
            raise GRPCError(
                Status.RESOURCE_EXHAUSTED,
                str(e),
                details=[detail],
            ) from e
        finally:
            await sub.aclose()

    async def ListWorkers(self, stream: Stream) -> None:
        request: pb.ListWorkersRequest = await stream.recv_message()
        workers = await self._db.list_workers(only_schedulable=request.only_schedulable)
        require_toolsets = set(request.require_toolsets)
        response = pb.ListWorkersResponse()
        for worker in workers:
            toolsets = self._registry.toolsets(worker)
            if require_toolsets and not require_toolsets.issubset(toolsets):
                continue
            if request.HasField("require_resources"):
                if not _worker_meets_resources(worker, request.require_resources):
                    continue
            response.workers.append(_worker_to_proto(worker, toolsets, self._registry))
        await stream.send_message(response)

    async def ListTasks(self, stream: Stream) -> None:
        request: pb.ListTasksRequest = await stream.recv_message()
        limit = request.limit if request.limit else self._config.list_tasks_default_limit
        limit = max(1, min(limit, self._config.list_tasks_max_limit))
        statuses = None
        if request.statuses:
            statuses = [_proto_status_to_str(s) for s in request.statuses]
        tasks = await self._db.list_tasks(
            batch_id=request.batch_id or None,
            callback_topic=request.callback_topic or None,
            master_session_id=request.master_session_id or None,
            statuses=statuses,
            worker_id=request.worker_id or None,
            limit=limit,
        )
        response = pb.ListTasksResponse()
        for task in tasks:
            response.tasks.append(_task_to_result_proto(task))
        await stream.send_message(response)

    async def CancelTask(self, stream: Stream) -> None:
        request: pb.CancelTaskRequest = await stream.recv_message()
        response = pb.CancelTaskResponse()
        task_ids: list[str] = []
        if request.task_id:
            task = await self._db.get_task(request.task_id)
            if task is None:
                raise GRPCError(
                    Status.NOT_FOUND, f"task {request.task_id} not found"
                )
            task_ids.append(request.task_id)
        if request.batch_id:
            batch_tasks = await self._db.list_tasks_by_batch(request.batch_id)
            task_ids.extend(t.task_id for t in batch_tasks if t.task_id not in task_ids)

        terminal_statuses = {"completed", "failed", "lost", "cancelled"}
        for task_id in task_ids:
            task = await self._db.get_task(task_id)
            if task is None:
                continue
            if task.status in terminal_statuses:
                response.already_terminal_task_ids.append(task_id)
                continue
            try:
                await self._router.on_cancel(
                    task_id,
                    reason=request.reason or "cancelled by master",
                    grace_seconds=request.grace_seconds if request.grace_seconds else None,
                )
            except TaskRouterError as e:
                raise GRPCError(Status.INVALID_ARGUMENT, str(e)) from e
            response.cancelled_task_ids.append(task_id)
        await stream.send_message(response)


# ------------------------------------------------------------------
# Server factory
# ------------------------------------------------------------------

async def serve_grpc(
    router: TaskRouter,
    auth: Auth,
    config: HubConfig,
    db: Database,
    bus: EventBus,
    registry: WorkerRegistry,
    *,
    host: str = "127.0.0.1",
    port: int = 0,
) -> Server:
    """Start a gRPC server serving the Master-facing TaskRelay service."""
    service = TaskRelayService(router, auth, config, db, bus, registry)
    intercepted = MasterAuthInterceptor(auth).intercept_service(service)
    server = Server([intercepted])
    await server.start(host, port)
    return server


# ------------------------------------------------------------------
# Proto conversion
# ------------------------------------------------------------------

def _spec_from_proto(spec: pb.TaskSpec) -> TaskSpec:
    context_json = None
    if spec.HasField("context"):
        context_json = _json_dumps(_context_payload_to_dict(spec.context))
    return TaskSpec(
        task_id=spec.task_id,
        goal=spec.goal,
        callback_topic=spec.callback_topic or "default",
        params_json=_json_dumps(dict(spec.params)) if spec.params else None,
        context_json=context_json,
        toolsets_json=_json_dumps(list(spec.toolsets)) if spec.toolsets else None,
        target_worker=spec.target_worker or None,
        timeout_seconds=spec.timeout_seconds if spec.timeout_seconds else None,
        priority=spec.priority,
        depends_on_json=_json_dumps(list(spec.depends_on)) if spec.depends_on else None,
        aggregate_key=spec.aggregate_key or None,
        min_resources_json=_json_dumps(_message_to_dict(spec.min_resources))
        if spec.HasField("min_resources")
        else None,
        trace_context_json=_json_dumps(_message_to_dict(spec.trace_context))
        if spec.HasField("trace_context")
        else None,
        allowed_worker_ids_json=_json_dumps(list(spec.allowed_worker_ids))
        if spec.allowed_worker_ids
        else None,
        deny_worker_ids_json=_json_dumps(list(spec.deny_worker_ids))
        if spec.deny_worker_ids
        else None,
        queue_timeout_seconds=spec.queue_timeout_seconds if spec.queue_timeout_seconds else None,
        max_attempts=spec.max_attempts if spec.max_attempts else None,
        first_progress_seconds=spec.first_progress_seconds if spec.first_progress_seconds else None,
    )


def _context_payload_to_dict(ctx: pb.ContextPayload) -> dict:
    if ctx.HasField("inline"):
        return {"inline": ctx.inline}
    if ctx.HasField("inline_gzip"):
        return {
            "inline_gzip": {
                "gzip_data": base64.b64encode(ctx.inline_gzip.gzip_data).decode("ascii"),
                "sha256": ctx.inline_gzip.sha256,
            }
        }
    if ctx.HasField("ref"):
        return {
            "ref": {
                "uri": ctx.ref.uri,
                "sha256": ctx.ref.sha256,
                "content_encoding": ctx.ref.content_encoding,
            }
        }
    return {}


def _message_to_dict(message: Any) -> dict:
    if message is None:
        return {}
    # Generated proto messages can be converted via the descriptor.
    result: dict[str, Any] = {}
    for field in message.DESCRIPTOR.fields:
        name = field.name
        if field.message_type is not None:
            sub = getattr(message, name)
            if field.label == field.LABEL_REPEATED:
                if field.type == field.TYPE_MESSAGE:
                    result[name] = [_message_to_dict(item) for item in sub]
                else:
                    result[name] = list(sub)
            else:
                if message.HasField(name):
                    result[name] = _message_to_dict(sub)
        else:
            value = getattr(message, name)
            if field.label == field.LABEL_REPEATED:
                result[name] = list(value)
            elif field.type == field.TYPE_BYTES and isinstance(value, bytes):
                result[name] = value.hex()
            else:
                result[name] = value
    return result


def _json_dumps(obj: Any) -> str | None:
    if obj is None or obj == {} or obj == []:
        return None
    return json.dumps(obj, separators=(",", ":"))


def _safe_json_loads(data: str | bytes | None) -> Any:
    if data is None:
        return None
    if isinstance(data, bytes):
        try:
            data = data.decode("utf-8")
        except UnicodeDecodeError:
            return None
    if not data:
        return None
    try:
        return json.loads(data)
    except json.JSONDecodeError:
        return None


def _dispatch_response_to_proto(resp: DispatchTaskResponse) -> pb.DispatchTaskResponse:
    proto = pb.DispatchTaskResponse(
        task_id=resp.task_id,
        batch_id=resp.batch_id or "",
        callback_topic=resp.callback_topic,
        status=_STATUS_TO_PROTO.get(resp.status, pb.TaskStatus.TASK_STATUS_UNSPECIFIED),
        idempotent_hit=resp.idempotent_hit,
        attempt=resp.attempt,
    )
    if resp.existing_result is not None:
        proto.existing_result.MergeFrom(_existing_result_to_proto(resp.existing_result))
    return proto


def _batch_response_to_proto(resp: BatchDispatchResponse) -> pb.DispatchTaskBatchResponse:
    return pb.DispatchTaskBatchResponse(
        batch_id=resp.batch_id,
        callback_topic=resp.callback_topic,
        tasks=[_dispatch_response_to_proto(t) for t in resp.tasks],
        idempotent_hit=resp.idempotent_hit,
    )


def _existing_result_to_proto(result: dict) -> pb.TaskResult:
    proto = pb.TaskResult(
        task_id=result.get("task_id", ""),
        status=_STATUS_TO_PROTO.get(
            result.get("status"), pb.TaskStatus.TASK_STATUS_UNSPECIFIED
        ),
        summary=result.get("summary") or "",
        result_text=result.get("result_text") or "",
        error=result.get("error") or "",
        worker_id=result.get("worker_id") or "",
        attempt=result.get("attempt", 0),
        max_attempts=result.get("max_attempts", 1),
        batch_id=result.get("batch_id") or "",
        latest_checkpoint_id=result.get("latest_checkpoint_id") or "",
        schema_version=1,
    )
    if result.get("started_at") is not None:
        proto.started_at = _seconds_to_ms(result["started_at"])
    if result.get("completed_at") is not None:
        proto.completed_at = _seconds_to_ms(result["completed_at"])
    fields_json = result.get("fields_json")
    if fields_json:
        fields_dict = _safe_json_loads(fields_json)
        if isinstance(fields_dict, dict):
            proto.fields.MergeFrom(_fields_from_dict(fields_dict))
    usage_json = result.get("usage_json")
    if usage_json:
        usage_dict = _safe_json_loads(usage_json)
        if isinstance(usage_dict, dict):
            proto.usage.MergeFrom(_usage_from_dict(usage_dict))
    return proto


def _task_to_result_proto(task: Task) -> pb.TaskResult:
    proto = pb.TaskResult(
        task_id=task.task_id,
        status=_STATUS_TO_PROTO.get(task.status, pb.TaskStatus.TASK_STATUS_UNSPECIFIED),
        summary=task.summary or "",
        result_text=task.result_json or "",
        error=task.error or "",
        worker_id=task.worker_id or "",
        attempt=task.attempt,
        max_attempts=task.max_attempts,
        batch_id=task.batch_id or "",
        latest_checkpoint_id=task.resume_from_checkpoint or "",
        schema_version=1,
    )
    if task.started_at is not None:
        proto.started_at = _seconds_to_ms(task.started_at)
    if task.completed_at is not None:
        proto.completed_at = _seconds_to_ms(task.completed_at)
    if task.fields_json:
        fields_dict = _safe_json_loads(task.fields_json)
        if isinstance(fields_dict, dict):
            proto.fields.MergeFrom(_fields_from_dict(fields_dict))
    if task.usage_json:
        usage_dict = _safe_json_loads(task.usage_json)
        if isinstance(usage_dict, dict):
            proto.usage.MergeFrom(_usage_from_dict(usage_dict))
    return proto


def _fields_from_dict(data: dict) -> pb.TaskFields:
    fields = pb.TaskFields(version=data.get("version", 1))
    for metric in data.get("metrics") or []:
        if isinstance(metric, dict):
            fields.metrics.append(
                pb.Metric(
                    name=metric.get("name", ""),
                    value=metric.get("value", 0.0),
                    unit=metric.get("unit", ""),
                    description=metric.get("description", ""),
                    origin_task_id=metric.get("origin_task_id", ""),
                )
            )
    for tag in data.get("tags") or []:
        if isinstance(tag, dict):
            fields.tags.append(pb.KeyValue(key=tag.get("key", ""), value=tag.get("value", "")))
    if data.get("report") is not None:
        fields.report = data["report"]
    for key, value in (data.get("extensions") or {}).items():
        if isinstance(value, bytes):
            fields.extensions[key] = value
        elif isinstance(value, str):
            fields.extensions[key] = value.encode("utf-8")
    return fields


def _usage_from_dict(data: dict) -> pb.TaskUsage:
    prompt = data.get("prompt_tokens", 0)
    completion = data.get("completion_tokens", 0)
    total = data.get("total_tokens")
    if total is None:
        total = prompt + completion
    return pb.TaskUsage(
        prompt_tokens=prompt,
        completion_tokens=completion,
        total_tokens=total,
        api_calls=data.get("api_calls", 0),
        tool_calls=data.get("tool_calls", 0),
        wall_seconds=data.get("wall_seconds", 0.0),
        cost_usd=data.get("cost_usd", 0.0),
        model=data.get("model", ""),
    )


def _filter_from_request(request: pb.WatchTaskRequest) -> EventFilter:
    if request.HasField("topic"):
        return EventFilter(topic=request.topic)
    if request.HasField("batch_id"):
        return EventFilter(batch_id=request.batch_id)
    if request.HasField("task_id"):
        return EventFilter(task_id=request.task_id)
    raise GRPCError(Status.INVALID_ARGUMENT, "WatchTask requires oneof topic/batch_id/task_id")


def _event_to_proto(event) -> pb.TaskEvent:
    proto = pb.TaskEvent(
        event_id=event.event_id,
        event_at=_seconds_to_ms(event.event_at),
        task_id=event.task_id or "",
        batch_id=event.batch_id or "",
        kind=_KIND_TO_PROTO.get(event.kind, pb.TaskEventKind.TASK_EVENT_KIND_UNSPECIFIED),
    )
    payload = _safe_json_loads(event.payload_json) or {}
    attempt = payload.get("attempt", 0)
    if event.kind == "TERMINAL":
        proto.result.CopyFrom(
            pb.TaskResult(
                task_id=event.task_id or "",
                status=_STATUS_TO_PROTO.get(
                    payload.get("status", ""), pb.TaskStatus.TASK_STATUS_UNSPECIFIED
                ),
                summary=payload.get("summary") or "",
                error=payload.get("error") or "",
                attempt=attempt,
            )
        )
    elif event.kind == "PROGRESS":
        proto.progress_summary = payload.get("summary") or ""
        proto.result.attempt = attempt
    elif event.kind == "STATUS":
        proto.result.task_id = event.task_id or ""
        proto.result.status = _STATUS_TO_PROTO.get(
            payload.get("status", ""), pb.TaskStatus.TASK_STATUS_UNSPECIFIED
        )
        proto.result.attempt = attempt
    elif event.kind == "CHECKPOINT":
        proto.checkpoint.task_id = event.task_id or ""
        proto.checkpoint.checkpoint_id = payload.get("checkpoint_id") or ""
        proto.checkpoint.event_id = event.event_id
        proto.checkpoint.checkpoint_at = _seconds_to_ms(event.event_at)
        proto.checkpoint.summary = payload.get("summary") or ""
        fields_json = payload.get("fields_json")
        if fields_json:
            fields_dict = _safe_json_loads(fields_json)
            if isinstance(fields_dict, dict):
                proto.checkpoint.fields.MergeFrom(_fields_from_dict(fields_dict))
    return proto


def _worker_to_proto(
    worker: Worker,
    toolsets: set[str],
    registry: WorkerRegistry,
) -> pb.WorkerInfo:
    info = pb.WorkerInfo(
        worker_id=worker.worker_id,
        status=worker.status,
        toolsets=sorted(toolsets),
        max_concurrent=worker.max_concurrent,
        running_tasks=worker.running_tasks,
        wake_url_present=bool(worker.wake_url),
    )
    for char in worker.session_modes.upper():
        mode = _SESSION_MODE_CHAR_TO_PROTO.get(char)
        if mode is not None:
            info.session_modes.append(mode)

    caps = _safe_json_loads(worker.capabilities_json) or {}
    if caps.get("os"):
        info.os = caps["os"]
    if caps.get("arch"):
        info.arch = caps["arch"]
    if caps.get("region"):
        info.region = caps["region"]
    for fmt in caps.get("resume_formats") or []:
        info.resume_formats.append(fmt)

    resources = _safe_json_loads(worker.resources_json) or {}
    if resources:
        info.resources.CopyFrom(_worker_resources_from_dict(resources))

    load = _safe_json_loads(worker.load_json) or {}
    if load:
        info.load.CopyFrom(_worker_load_from_dict(load))

    if worker.last_announce_at is not None:
        info.last_announce_at = _seconds_to_ms(worker.last_announce_at)
    if worker.last_heartbeat_at is not None:
        info.last_heartbeat_at = _seconds_to_ms(worker.last_heartbeat_at)

    return info


def _worker_resources_from_dict(data: dict) -> pb.WorkerResources:
    return pb.WorkerResources(
        cpu_cores=data.get("cpu_cores", data.get("cpu", 0)),
        memory_gb=data.get("memory_gb", data.get("memory", 0)),
        gpu_count=data.get("gpu_count", data.get("gpu", 0)),
        gpu_model=data.get("gpu_model", ""),
        disk_gb=data.get("disk_gb", data.get("disk", 0)),
        network_profile=data.get("network_profile", ""),
    )


def _worker_load_from_dict(data: dict) -> pb.WorkerLoad:
    return pb.WorkerLoad(
        running_tasks=data.get("running_tasks", 0),
        cpu_percent=data.get("cpu_percent", data.get("cpu", 0.0)),
        memory_percent=data.get("memory_percent", data.get("memory", 0.0)),
    )


def _worker_meets_resources(worker: Worker, requirements: pb.ResourceRequirements) -> bool:
    resources = _safe_json_loads(worker.resources_json) or {}
    if requirements.min_cpu_cores and resources.get("cpu_cores", resources.get("cpu", 0)) < requirements.min_cpu_cores:
        return False
    if requirements.min_memory_gb and resources.get("memory_gb", resources.get("memory", 0)) < requirements.min_memory_gb:
        return False
    if requirements.requires_gpu and resources.get("gpu_count", resources.get("gpu", 0)) < 1:
        return False
    if requirements.required_network_profiles:
        worker_profiles = set(resources.get("network_profiles") or [resources.get("network_profile", "")])
        if not set(requirements.required_network_profiles).issubset(worker_profiles):
            return False
    return True


def _proto_status_to_str(status: pb.TaskStatus) -> str:
    mapping = {
        pb.TaskStatus.TASK_STATUS_PENDING: "pending",
        pb.TaskStatus.TASK_STATUS_RUNNING: "running",
        pb.TaskStatus.TASK_STATUS_COMPLETED: "completed",
        pb.TaskStatus.TASK_STATUS_FAILED: "failed",
        pb.TaskStatus.TASK_STATUS_LOST: "lost",
        pb.TaskStatus.TASK_STATUS_CANCELLED: "cancelled",
    }
    return mapping.get(status, "pending")


def _seconds_to_ms(value: float) -> int:
    return int(value * 1000)
