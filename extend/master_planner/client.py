"""HTTP client for the Gateway-API AgentRelayService.

Pure stdlib (urllib) — matches the hermes plugin convention (see
``plugins/platforms/a2a``). No async: every method is a blocking call.

Authoritative contract: ``server/api/gateway-api/v1/agent_relay.proto``
(served by the gateway-api kratos v3 HTTP server)::

    POST /v1/agent/tasks                      DispatchTask
    POST /v1/agent/tasks:batch                DispatchTaskBatch
    POST /v1/agent/tasks:watch                WatchTask     (SSE, text/event-stream)
    POST /v1/agent/tasks/{task_id}:result     GetTaskResult
    GET  /v1/agent/tasks                      ListTasks     (query params)
    POST /v1/agent/workers:list               ListWorkers
    POST /v1/agent/tasks/{task_id}:cancel     CancelTask

Wire format (verified against the server build):

  * Request bodies are decoded with protojson ``DiscardUnknown: false``
    (``server/internal/server/json_request.go``) — an unknown field is a
    400. Bodies must use proto field names (snake_case). Consequences:
    ``master_session_id`` lives on the request message, NOT inside
    TaskSpec, and ``WatchTaskRequest`` has no ``wait_seconds`` — waiting
    is a pure client-side concern and must never be sent.
  * The client sends ``Accept: application/protojson`` so responses are
    marshalled by the server's protojson codec with
    ``UseProtoNames: true, EmitUnpopulated: true``
    (``server/internal/server/json.go``): snake_case keys, int64 as JSON
    strings (``"event_id": "3"``), enums as string names
    (``"TASK_STATUS_COMPLETED"``). :func:`normalize_response` still
    rewrites any lowerCamelCase keys defensively, and the enum readers
    below accept both enum names and numbers.

SSE frames (kratos v3 ``transport/http/stream.go`` ``sendSSE``)::

    event: message
    data: {"event_id":"3","kind":"TASK_EVENT_KIND_PROGRESS",...}

    event: error
    data: {"code":412,"reason":"CURSOR_OUT_OF_RANGE","message":"...",
           "metadata":{"requested_since_event_id":"...",
                       "oldest_available_event_id":"...",
                       "newest_event_id":"..."}}

Normal frames carry ``event: message`` and NO ``id:`` line — the resume
cursor is ``data.event_id``. Errors have TWO wire shapes, both verified
against the live server:

  * mid-stream: an ``event: error`` frame terminates the stream; its payload
    is a kratos ``errors.Error`` whose ``metadata`` map carries the cursor
    positions (``server/internal/biz/gatewayapi/errors.go``);
  * before the first frame (e.g. a rejected ``since_event_id`` at subscribe
    time): a plain non-2xx HTTP response with the same kratos error JSON
    body — ``watch`` raises it as a :class:`GatewayError`.

``reason=CURSOR_OUT_OF_RANGE`` means the resume cursor fell off the
retained event window — the caller must fall back to ``GetTaskResult``
per task instead of replaying.

WatchTask is a long stream on the wire; the planner's impedance match is a
blocking *short* wait (``wait_seconds <= 60``): :meth:`GatewayClient.watch`
pumps the SSE stream on a reader thread while the calling thread polls an
interrupt callback once per ``poll_interval_s`` (hermes hard requirement —
a blocking handler that never polls cannot be interrupted by the user).
"""

from __future__ import annotations

import json
import logging
import os
import queue
import re
import socket
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Callable, Optional

logger = logging.getLogger(__name__)

DEFAULT_BASE_URL = "https://gateway.infa.example.com"  # placeholder, override via env
DEFAULT_TIMEOUT_S = 30.0
DEFAULT_POLL_INTERVAL_S = 1.0  # interrupt poll cadence inside blocking waits
MAX_WAIT_SECONDS = 60  # spec §4.2: safe under every hermes execution path

ENV_API_KEY = "INFA_GATEWAY_API_KEY"
ENV_BASE_URL = "INFA_GATEWAY_BASE_URL"
ENV_TIMEOUT_S = "INFA_GATEWAY_TIMEOUT_S"

# Pins the server's protojson codec (see module docstring). For the watch
# stream kratos picks the first decodable Accept entry after
# "text/event-stream" misses, so protojson must be listed there too.
ACCEPT_PROTOJSON = "application/protojson"
ACCEPT_SSE = "text/event-stream, application/protojson"


class GatewayError(Exception):
    """Structured error from the gateway (or the transport below it)."""

    def __init__(self, message: str, *, status: int = 0, code: str = ""):
        super().__init__(message)
        self.status = status
        self.code = code

    def to_dict(self) -> dict[str, Any]:
        return {"error": str(self), "status": self.status, "code": self.code}


# ---------------------------------------------------------------------------
# Response normalization (protojson shapes)
# ---------------------------------------------------------------------------

_CAMEL_KEY_RE = re.compile(r"[a-z][a-zA-Z0-9]*")
_CAMEL_BOUNDARY_RE = re.compile(r"(?<!^)(?=[A-Z])")


def _normalize_key(key: str) -> str:
    """Rewrite only pure lowerCamelCase keys (protojson JSON names).

    snake_case keys, UPPER_CASE names and user-supplied map keys (e.g.
    error ``metadata``) pass through untouched.
    """
    if _CAMEL_KEY_RE.fullmatch(key) and any(ch.isupper() for ch in key):
        return _CAMEL_BOUNDARY_RE.sub("_", key).lower()
    return key


def normalize_response(value: Any) -> Any:
    """Recursively convert response keys from camelCase to snake_case."""
    if isinstance(value, dict):
        return {_normalize_key(str(k)): normalize_response(v) for k, v in value.items()}
    if isinstance(value, list):
        return [normalize_response(item) for item in value]
    return value


_TASK_STATUS_SHORT = {
    1: "pending",
    2: "running",
    3: "completed",
    4: "failed",
    5: "lost",
    6: "cancelled",
}


def task_status_name(value: Any) -> str:
    """Normalize a TaskStatus (enum name, number, or short name) to its short name."""
    if value is None or isinstance(value, bool):
        return ""
    if isinstance(value, int):
        return _TASK_STATUS_SHORT.get(value, "")
    text = str(value).strip()
    if not text:
        return ""
    if text.isdigit():
        return _TASK_STATUS_SHORT.get(int(text), "")
    text = text.lower()
    if text.startswith("task_status_"):
        text = text[len("task_status_"):]
    return "" if text == "unspecified" else text


def task_status_enum(value: Any) -> str:
    """Map any accepted status spelling onto the proto enum name (query filters)."""
    short = task_status_name(value)
    return f"TASK_STATUS_{short.upper()}" if short else ""


_TASK_EVENT_KIND_TYPE = {
    1: "status",
    2: "progress",
    3: "terminal",
    4: "checkpoint",
    5: "aggregate",
}


def task_event_kind_type(value: Any) -> str:
    """Normalize a TaskEventKind (enum name or number) to an event type string."""
    if isinstance(value, int) and not isinstance(value, bool):
        return _TASK_EVENT_KIND_TYPE.get(value, "event")
    text = str(value or "").strip()
    if not text:
        return "event"
    if text.isdigit():
        return _TASK_EVENT_KIND_TYPE.get(int(text), "event")
    text = text.lower()
    if text.startswith("task_event_kind_"):
        text = text[len("task_event_kind_"):]
    return "event" if text == "unspecified" else text


def _normalize_sse_error(data: Any) -> dict[str, Any]:
    """kratos ``errors.Error`` JSON → flat dict with a snake_case ``code``.

    The cursor positions ride in the ``metadata`` map; they are flattened
    to the top level so callers can read ``requested_since_event_id`` /
    ``oldest_available_event_id`` directly.
    """
    if not isinstance(data, dict):
        return {"code": "unknown", "raw": data}
    data = normalize_response(data)
    reason = str(data.get("reason") or "")
    http_code = data.get("code") or 0
    out: dict[str, Any] = {
        "code": reason.lower() if reason else (str(http_code) or "unknown"),
        "reason": reason,
        "http_code": http_code,
        "message": str(data.get("message") or ""),
    }
    metadata = data.get("metadata")
    if isinstance(metadata, dict):
        out.update(metadata)
    return out


def _default_should_stop() -> bool:
    """Interrupt poll used when the caller does not supply one.

    Uses the hermes per-thread interrupt registry when available (production);
    falls back to "never stop" so the client stays usable standalone.
    """
    try:
        from tools.interrupt import is_interrupted

        return bool(is_interrupted())
    except Exception:
        return False


class GatewayClient:
    """Blocking client for the AgentRelayService."""

    def __init__(
        self,
        base_url: str,
        api_key: str,
        *,
        timeout_s: float = DEFAULT_TIMEOUT_S,
        poll_interval_s: float = DEFAULT_POLL_INTERVAL_S,
    ):
        if not api_key:
            raise GatewayError(
                f"{ENV_API_KEY} is not configured", code="missing_api_key"
            )
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.timeout_s = float(timeout_s)
        self.poll_interval_s = float(poll_interval_s)

    @classmethod
    def from_env(
        cls, *, poll_interval_s: float = DEFAULT_POLL_INTERVAL_S
    ) -> "GatewayClient":
        timeout_raw = os.getenv(ENV_TIMEOUT_S, "").strip()
        try:
            timeout_s = float(timeout_raw) if timeout_raw else DEFAULT_TIMEOUT_S
        except ValueError:
            timeout_s = DEFAULT_TIMEOUT_S
        return cls(
            base_url=os.getenv(ENV_BASE_URL, "").strip() or DEFAULT_BASE_URL,
            api_key=os.getenv(ENV_API_KEY, "").strip(),
            timeout_s=timeout_s,
            poll_interval_s=poll_interval_s,
        )

    # ------------------------------------------------------------------
    # Unary RPCs
    # ------------------------------------------------------------------

    def dispatch_task(
        self, spec: dict[str, Any], *, master_session_id: str = ""
    ) -> dict[str, Any]:
        """DispatchTask — submit a single TaskSpec. ``spec.task_id`` is the idempotency key."""
        body: dict[str, Any] = {"spec": spec}
        if master_session_id:
            body["master_session_id"] = master_session_id
        return self._post_json("/v1/agent/tasks", body)

    def dispatch_batch(
        self,
        specs: list[dict[str, Any]],
        *,
        batch_id: str = "",
        master_session_id: str = "",
        join_policy: str = "",
    ) -> dict[str, Any]:
        """DispatchTaskBatch — submit multiple TaskSpecs as one batch."""
        body: dict[str, Any] = {"specs": specs}
        if batch_id:
            body["batch_id"] = batch_id
        if master_session_id:
            body["master_session_id"] = master_session_id
        completion_mode = _JOIN_COMPLETION_MODES.get(join_policy.strip().lower())
        if completion_mode:
            body["policy"] = {"completion_mode": completion_mode}
        return self._post_json("/v1/agent/tasks:batch", body)

    def get_task_result(
        self, task_id: str, *, include_latest_checkpoint: bool = True
    ) -> dict[str, Any]:
        """GetTaskResult — terminal result incl. latest checkpoint."""
        body = {"include_latest_checkpoint": bool(include_latest_checkpoint)}
        return self._post_json(
            f"/v1/agent/tasks/{urllib.parse.quote(task_id, safe='')}:result", body
        )

    def list_tasks(
        self,
        *,
        master_session_id: str = "",
        batch_id: str = "",
        statuses: tuple[str, ...] = (),
        worker_id: str = "",
        limit: int = 0,
        page_token: str = "",
    ) -> dict[str, Any]:
        """ListTasks — inventory for recovery. Always tenant-filtered server-side.

        ``statuses`` carries proto enum names (``TASK_STATUS_COMPLETED`` …).
        """
        params: list[tuple[str, str]] = []
        if batch_id:
            params.append(("batch_id", batch_id))
        if master_session_id:
            params.append(("master_session_id", master_session_id))
        for status in statuses:
            params.append(("statuses", status))
        if worker_id:
            params.append(("worker_id", worker_id))
        if limit:
            params.append(("limit", str(int(limit))))
        if page_token:
            params.append(("page_token", page_token))
        query = urllib.parse.urlencode(params)
        path = "/v1/agent/tasks" + (f"?{query}" if query else "")
        return self._get_json(path)

    def list_workers(
        self, require_toolsets: Optional[list[str]] = None
    ) -> dict[str, Any]:
        """ListWorkers — probe platform capabilities before planning."""
        body: dict[str, Any] = {}
        if require_toolsets:
            body["require_toolsets"] = list(require_toolsets)
        return self._post_json("/v1/agent/workers:list", body)

    def cancel_task(
        self,
        *,
        task_id: str,
        batch_id: str = "",
        reason: str = "",
        grace_seconds: int = 0,
    ) -> dict[str, Any]:
        """CancelTask — cancel one task, or every non-terminal task in a batch.

        The HTTP mapping puts ``task_id`` in the path. For a batch cancel the
        caller supplies any task_id of the batch for the path and passes the
        ``batch_id`` in the body.
        """
        if not task_id:
            raise GatewayError(
                "task_id is required (cancel path parameter)", code="invalid_args"
            )
        body: dict[str, Any] = {}
        if batch_id:
            body["batch_id"] = batch_id
        if reason:
            body["reason"] = reason
        if grace_seconds:
            body["grace_seconds"] = int(grace_seconds)
        return self._post_json(
            f"/v1/agent/tasks/{urllib.parse.quote(task_id, safe='')}:cancel", body
        )

    # ------------------------------------------------------------------
    # WatchTask (SSE stream with interrupt-aware short wait)
    # ------------------------------------------------------------------

    def watch(
        self,
        *,
        task_id: str = "",
        batch_id: str = "",
        since_event_id: str = "",
        wait_seconds: float = 60.0,
        should_stop: Optional[Callable[[], bool]] = None,
    ) -> dict[str, Any]:
        """Block up to ``wait_seconds`` for the next batch of task events.

        Returns a dict::

            {
              "events": [{"type": str, "id": str, "data": dict}, ...],
              "cursor": str,            # last event_id seen ("" if none)
              "error": dict | None,     # SSE error frame payload, if any
              "interrupted": bool,      # True when should_stop() fired
              "reason": "terminal" | "timeout" | "interrupted" | "error"
                        | "stream_closed",
            }

        ``type`` is derived from the TaskEvent ``kind``
        (progress/terminal/status/checkpoint/aggregate) and ``id`` from
        ``event_id`` — kratos SSE frames carry no ``id:`` line.

        The wait is clamped to ``MAX_WAIT_SECONDS``. The calling thread polls
        ``should_stop`` (default: hermes ``is_interrupted()``) every
        ``poll_interval_s`` so a blocked watch never wedges the agent loop.
        """
        wait_seconds = max(1.0, min(float(wait_seconds), float(MAX_WAIT_SECONDS)))
        stop = should_stop or _default_should_stop

        # WatchTaskRequest is exactly {oneof filter(topic|batch_id|task_id),
        # since_event_id}. The server decodes with protojson
        # DiscardUnknown:false and waiting is client-side — never send
        # wait_seconds.
        body: dict[str, Any] = {}
        if task_id:
            body["task_id"] = task_id
        elif batch_id:
            body["batch_id"] = batch_id
        if since_event_id:
            body["since_event_id"] = str(since_event_id)

        req = self._new_request("/v1/agent/tasks:watch", body, accept=ACCEPT_SSE)
        req.add_header("Cache-Control", "no-cache")

        frames: "queue.Queue[dict[str, Any]]" = queue.Queue()
        # The socket read timeout must outlive the whole wait window; the
        # server may legitimately stay silent until wait_seconds elapses.
        open_timeout = self.timeout_s + wait_seconds + 15.0
        holder: dict[str, Any] = {}  # shares the live response with the caller

        def _reader() -> None:
            try:
                resp = urllib.request.urlopen(req, timeout=open_timeout)  # noqa: S310 (operator-configured gateway)
                holder["resp"] = resp
                for frame in _iter_sse_frames(resp):
                    frames.put({"kind": "frame", "frame": frame})
                frames.put({"kind": "closed"})
            except urllib.error.HTTPError as exc:
                # The stream failed before the first SSE frame: kratos answers
                # with a plain errors.Error JSON (e.g. 412 CURSOR_OUT_OF_RANGE
                # when the resume cursor is rejected at subscribe time), not an
                # `event: error` frame. Surface it as a structured GatewayError.
                frames.put({"kind": "http_error", "error": self._http_error(exc)})
            except Exception as exc:  # reader thread must never raise naked
                frames.put({"kind": "transport_error", "error": exc})
            finally:
                resp = holder.pop("resp", None)
                if resp is not None:
                    _force_close(resp)

        thread = threading.Thread(target=_reader, daemon=True, name="gw-watch-sse")

        events: list[dict[str, Any]] = []
        cursor = ""
        error_frame: Optional[dict[str, Any]] = None
        reason = "timeout"
        interrupted = False

        thread.start()
        deadline = time.monotonic() + wait_seconds
        try:
            while True:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    reason = "timeout"
                    break
                try:
                    item = frames.get(timeout=min(self.poll_interval_s, remaining))
                except queue.Empty:
                    if stop():
                        interrupted = True
                        reason = "interrupted"
                        break
                    continue

                kind = item.get("kind")
                if kind == "frame":
                    frame = item["frame"]
                    etype = frame.get("event") or ""  # "message" | "error"
                    data = frame.get("data")
                    if etype == "error":
                        error_frame = _normalize_sse_error(data)
                        reason = "error"
                        break
                    if isinstance(data, dict):
                        data = normalize_response(data)
                        eid = str(data.get("event_id") or "")
                        if eid == "0":
                            # protojson EmitUnpopulated renders a zero int64 as
                            # "0"; only real (positive) ids advance the cursor.
                            eid = ""
                        etype = task_event_kind_type(data.get("kind"))
                    else:
                        eid = ""
                        etype = etype or "event"
                    if eid:
                        cursor = eid
                    events.append({"type": etype, "id": eid, "data": data})
                    if etype == "terminal":
                        reason = "terminal"
                        break
                elif kind == "closed":
                    reason = (
                        "terminal"
                        if any(e["type"] == "terminal" for e in events)
                        else "stream_closed"
                    )
                    break
                elif kind == "http_error":
                    raise item["error"]
                elif kind == "transport_error":
                    exc = item["error"]
                    if isinstance(exc, (socket.timeout, TimeoutError)):
                        reason = "timeout"
                        break
                    raise self._wrap_transport_error(exc)
        finally:
            # Unblock the reader thread: shutdown() on the raw socket wakes a
            # pending recv immediately; a plain cross-thread close() would
            # block on the buffered reader's lock until the server speaks.
            resp = holder.get("resp")
            if resp is not None:
                _force_close(resp)
            thread.join(timeout=2.0)

        return {
            "events": events,
            "cursor": cursor,
            "error": error_frame,
            "interrupted": interrupted,
            "reason": reason,
        }

    # ------------------------------------------------------------------
    # Internals
    # ------------------------------------------------------------------

    def _new_request(
        self, path: str, body: dict[str, Any], *, accept: str = ACCEPT_PROTOJSON
    ) -> urllib.request.Request:
        data = json.dumps(body).encode("utf-8")
        headers = {
            "Content-Type": "application/json",
            "Accept": accept,
            "Authorization": f"Bearer {self.api_key}",
            "User-Agent": "xhermes-master-planner/0.1",
        }
        return urllib.request.Request(
            self.base_url + path, data=data, headers=headers, method="POST"
        )

    def _post_json(self, path: str, body: dict[str, Any]) -> dict[str, Any]:
        req = self._new_request(path, body)
        return self._send_json(req)

    def _get_json(self, path: str) -> dict[str, Any]:
        headers = {
            "Accept": ACCEPT_PROTOJSON,
            "Authorization": f"Bearer {self.api_key}",
            "User-Agent": "xhermes-master-planner/0.1",
        }
        req = urllib.request.Request(
            self.base_url + path, headers=headers, method="GET"
        )
        return self._send_json(req)

    def _send_json(self, req: urllib.request.Request) -> dict[str, Any]:
        try:
            with urllib.request.urlopen(req, timeout=self.timeout_s) as resp:  # noqa: S310 (operator-configured gateway)
                payload = resp.read().decode("utf-8")
        except urllib.error.HTTPError as exc:
            raise self._http_error(exc) from exc
        except Exception as exc:
            raise self._wrap_transport_error(exc) from exc
        if not payload.strip():
            return {}
        try:
            parsed = json.loads(payload)
        except json.JSONDecodeError as exc:
            raise GatewayError(
                f"invalid JSON from gateway: {exc}", code="bad_response"
            ) from exc
        if not isinstance(parsed, dict):
            raise GatewayError(
                "gateway returned a non-object JSON body", code="bad_response"
            )
        return normalize_response(parsed)

    @staticmethod
    def _http_error(exc: urllib.error.HTTPError) -> GatewayError:
        message = f"gateway HTTP {exc.code}"
        code = ""
        try:
            raw = exc.read().decode("utf-8", "replace")
            parsed = json.loads(raw)
            if isinstance(parsed, dict):
                # kratos errors.Error JSON: {code, reason, message, metadata}
                message = str(parsed.get("message") or parsed.get("error") or message)
                code = str(parsed.get("reason") or parsed.get("code") or "")
        except Exception:
            pass
        return GatewayError(message, status=exc.code, code=code)

    @staticmethod
    def _wrap_transport_error(exc: Exception) -> GatewayError:
        if isinstance(exc, GatewayError):
            return exc
        if isinstance(exc, urllib.error.URLError):
            return GatewayError(
                f"gateway unreachable: {exc.reason}", code="unreachable"
            )
        if isinstance(exc, (socket.timeout, TimeoutError)):
            return GatewayError("gateway request timed out", code="timeout")
        return GatewayError(f"gateway transport error: {exc}", code="transport_error")


# join_policy tool argument → BatchPolicy.CompletionMode (proto enum name).
_JOIN_COMPLETION_MODES = {
    "all": "COMPLETION_MODE_ALL",
    "any": "COMPLETION_MODE_ANY",
    "majority": "COMPLETION_MODE_MAJORITY",
}


def _dig_socket(resp: Any) -> Any:
    """Find the raw socket under the urllib/http.client wrapper layers.

    urlopen may return an ``http.client.HTTPResponse`` (whose ``.fp`` is the
    BufferedReader) or an ``addinfourl`` (whose ``.fp`` is the HTTPResponse).
    Walk ``.fp`` until a buffered reader with ``.raw._sock`` shows up.
    """
    node = resp
    for _ in range(4):
        raw = getattr(node, "raw", None)
        if raw is not None and getattr(raw, "_sock", None) is not None:
            return raw._sock
        node = getattr(node, "fp", None)
        if node is None:
            return None
    return None


def _force_close(resp: Any) -> None:
    """Abort an in-flight SSE response, waking a reader blocked in recv.

    ``resp.close()`` from another thread blocks on the buffered reader's
    internal lock while a ``readline()`` is parked in ``recv``. Shutting down
    the raw socket first makes the pending read return EOF/error at once.
    Best-effort: any missing layer just falls through to close().
    """
    try:
        sock = _dig_socket(resp)
        if sock is not None:
            sock.shutdown(socket.SHUT_RDWR)
    except Exception:
        pass
    try:
        resp.close()
    except Exception:
        pass


def _iter_sse_frames(resp: Any):
    """Yield parsed SSE frames ``{"event": str, "id": str, "data": Any}``.

    ``data`` is JSON-decoded when possible, else kept as the raw string.
    Frames are delimited by a blank line; lines starting with ``:`` are
    comments/heartbeats and ignored. Kratos emits ``event: message|error``
    and ``data:`` lines only — ``id`` stays empty on the wire.
    """
    event = ""
    eid = ""
    data_lines: list[str] = []

    def _flush():
        nonlocal event, eid, data_lines
        if not data_lines and not event:
            return None
        raw = "\n".join(data_lines)
        try:
            data: Any = json.loads(raw) if raw else {}
        except json.JSONDecodeError:
            data = raw
        frame = {"event": event, "id": eid, "data": data}
        event, eid, data_lines = "", eid, []
        return frame

    for raw_line in resp:
        line = raw_line.decode("utf-8", "replace").rstrip("\r\n")
        if not line:
            frame = _flush()
            if frame is not None:
                yield frame
            continue
        if line.startswith(":"):
            continue
        field, _, value = line.partition(":")
        value = value.lstrip(" ")
        if field == "event":
            event = value
        elif field == "id":
            eid = value
        elif field == "data":
            data_lines.append(value)
    frame = _flush()
    if frame is not None:
        yield frame
