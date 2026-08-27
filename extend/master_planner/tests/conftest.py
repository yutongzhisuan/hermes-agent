"""Shared fixtures: a local mock AgentRelayService HTTP server.

The mock speaks the REAL gateway-api contract
(``server/api/gateway-api/v1/agent_relay.proto`` + kratos v3 HTTP):

  * unary responses are protojson with ``UseProtoNames`` (snake_case keys),
    int64 as JSON strings and enums as string names
    (``"TASK_STATUS_PENDING"``);
  * ``ListTasks`` is ``GET /v1/agent/tasks`` with query params;
  * ``GetTaskResult`` / ``CancelTask`` take ``task_id`` in the path
    (``/v1/agent/tasks/{task_id}:result`` / ``:cancel``);
  * watch SSE normal frames are ``event: message`` + one ``data:`` line
    (NO ``id:`` line — the cursor is ``data.event_id``); error frames are
    ``event: error`` with a kratos error JSON (``code``/``reason``/
    ``message``/``metadata``);
  * strict-body checks mirror the server's ``DiscardUnknown: false``
    decoder: ``wait_seconds`` in a watch body or ``master_session_id``
    inside a TaskSpec are rejected with a 400.

Behavior switches used by tests:

  * dispatch of an already-seen ``spec.task_id`` returns ``idempotent_hit: true``;
  * watch with ``since_event_id == "expired"`` emits an ``event: error`` frame
    with ``reason=CURSOR_OUT_OF_RANGE``;
  * watch with ``task_id == "block-me"`` stays silent for 25s (interrupt tests);
  * otherwise watch emits two progress frames then a terminal frame and closes.
"""

from __future__ import annotations

import json
import re
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from extend.master_planner import tools as mp_tools
from extend.master_planner.client import GatewayClient

API_KEY = "test-api-key"

_TASK_VERB_RE = re.compile(r"^/v1/agent/tasks/([^/]+):(result|cancel)$")


def _kratos_error(status: int, reason: str, message: str, metadata: dict | None = None) -> dict:
    """kratos errors.Error protojson shape."""
    return {
        "code": status,
        "reason": reason,
        "message": message,
        "metadata": metadata or {},
    }


def _task_result(tid: str, status: str, **extra) -> dict:
    """TaskResult protojson shape (snake_case, enum names, int64 strings)."""
    payload = {
        "task_id": tid,
        "status": status,
        "summary": "",
        "result_text": "",
        "error": "",
        "started_at": "0",
        "completed_at": "0",
        "worker_id": "w1",
        "schema_version": 1,
        "batch_id": "",
        "latest_checkpoint_id": "",
        "attempt": 1,
        "max_attempts": 1,
        "result_truncated": False,
    }
    payload.update(extra)
    return payload


class _MockGatewayHandler(BaseHTTPRequestHandler):
    dispatched: dict[str, dict] = {}
    lock = threading.Lock()

    # -- helpers ----------------------------------------------------------

    def _send_json(self, payload: dict, status: int = 200) -> None:
        raw = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def _sse(self, frames: list[tuple[str, dict]]) -> None:
        """Emit kratos-style SSE: ``event: <name>`` + ``data: <json>``, no id."""
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        for event, data in frames:
            chunk = f"event: {event}\ndata: {json.dumps(data)}\n\n"
            self.wfile.write(chunk.encode("utf-8"))
            self.wfile.flush()
            time.sleep(0.02)

    def _auth_ok(self) -> bool:
        return self.headers.get("Authorization") == f"Bearer {API_KEY}"

    def _read_body(self) -> dict:
        length = int(self.headers.get("Content-Length") or 0)
        return json.loads(self.rfile.read(length) or b"{}")

    # -- routing ----------------------------------------------------------

    def do_GET(self) -> None:  # noqa: N802 (http.server API)
        if not self._auth_ok():
            self._send_json(
                _kratos_error(401, "UNAUTHENTICATED", "bad key"), status=401
            )
            return
        parsed = urllib.parse.urlparse(self.path)
        if parsed.path == "/v1/agent/tasks":
            self._list_tasks(urllib.parse.parse_qs(parsed.query))
        else:
            self._send_json(
                _kratos_error(404, "NOT_FOUND", self.path), status=404
            )

    def do_POST(self) -> None:  # noqa: N802 (http.server API)
        if not self._auth_ok():
            self._send_json(
                _kratos_error(401, "UNAUTHENTICATED", "bad key"), status=401
            )
            return
        body = self._read_body()
        path = self.path.rstrip("/")

        verb = _TASK_VERB_RE.match(path)
        if verb:
            tid = urllib.parse.unquote(verb.group(1))
            if verb.group(2) == "result":
                self._task_result(tid, body)
            else:
                self._cancel(tid, body)
            return

        if path == "/v1/agent/tasks":
            self._dispatch(body)
        elif path == "/v1/agent/tasks:batch":
            self._dispatch_batch(body)
        elif path == "/v1/agent/tasks:watch":
            self._watch(body)
        elif path == "/v1/agent/workers:list":
            self._send_json({
                "workers": [
                    {"worker_id": "w1", "status": "idle", "toolsets": ["research", "code"]},
                    {"worker_id": "w2", "status": "idle", "toolsets": ["research"]},
                ]
            })
        elif path == "/v1/agent/models:list":
            self._list_models(body)
        else:
            self._send_json(
                _kratos_error(404, "NOT_FOUND", path), status=404
            )

    # -- endpoints ----------------------------------------------------------

    def _dispatch(self, body: dict) -> None:
        spec = body.get("spec") or {}
        if "master_session_id" in spec:
            # The strict server decoder rejects unknown TaskSpec fields.
            self._send_json(
                _kratos_error(
                    400, "INVALID_ARGUMENT", "unknown field master_session_id"
                ),
                status=400,
            )
            return
        tid = spec.get("task_id") or ""
        with self.lock:
            hit = tid in self.dispatched
            self.dispatched.setdefault(tid, spec)
        self._send_json({
            "task_id": tid,
            "batch_id": "",
            "callback_topic": "tenant:test:run",
            "status": "TASK_STATUS_PENDING",
            "idempotent_hit": hit,
            "attempt": 0,
        })

    def _dispatch_batch(self, body: dict) -> None:
        specs = body.get("specs") or []
        with self.lock:
            for s in specs:
                self.dispatched.setdefault(s.get("task_id") or "", s)
        first = (specs[0].get("task_id") or "x") if specs else "x"
        self._send_json({
            "batch_id": body.get("batch_id") or f"{first}-batch",
            "callback_topic": "tenant:test:run",
            "tasks": [
                {
                    "task_id": s.get("task_id") or "",
                    "batch_id": body.get("batch_id") or f"{first}-batch",
                    "callback_topic": "tenant:test:run",
                    "status": "TASK_STATUS_PENDING",
                    "idempotent_hit": False,
                    "attempt": 0,
                }
                for s in specs
            ],
            "idempotent_hit": False,
        })

    def _task_result(self, tid: str, body: dict) -> None:
        checkpoint = "cp-latest" if body.get("include_latest_checkpoint") else ""
        self._send_json(
            _task_result(
                tid,
                "TASK_STATUS_COMPLETED",
                summary=f"result for {tid}",
                result_text=f"full text for {tid}",
                latest_checkpoint_id=checkpoint,
            )
        )

    def _list_tasks(self, query: dict) -> None:
        with self.lock:
            tasks = [
                _task_result(tid, "TASK_STATUS_PENDING", summary=s.get("goal", ""))
                for tid, s in self.dispatched.items()
            ]
        statuses = set(query.get("statuses") or [])
        if statuses:
            tasks = [t for t in tasks if t["status"] in statuses]
        self._send_json({"tasks": tasks, "next_page_token": ""})

    def _list_models(self, body: dict) -> None:
        """ListAgentModels contract: pool-wide ready models, deduped server-side.

        The duplicate ``mv-qwen3-32b`` entry simulates a server regression so
        the tool's defensive dedupe is exercised; ``region`` in the request
        body filters the list.
        """
        models = [
            {
                "model_version_id": "mv-qwen3-32b",
                "display_name": "Qwen3 32B",
                "node_count": 3,
                "available_slots": 12,
                "regions": ["cn-east-1", "cn-north-1"],
            },
            {
                "model_version_id": "mv-deepseek-v3",
                "display_name": "DeepSeek V3",
                "node_count": 1,
                "available_slots": 4,
                "regions": ["cn-east-1"],
            },
            {
                "model_version_id": "mv-qwen3-32b",
                "display_name": "Qwen3 32B",
                "node_count": 3,
                "available_slots": 12,
                "regions": ["cn-east-1", "cn-north-1"],
            },
        ]
        region = str(body.get("region") or "")
        if region:
            models = [m for m in models if region in m["regions"]]
        self._send_json({"models": models})

    def _cancel(self, path_task_id: str, body: dict) -> None:
        cancelled = [path_task_id]
        batch_id = body.get("batch_id") or ""
        if batch_id:
            with self.lock:
                cancelled = [
                    tid
                    for tid, s in self.dispatched.items()
                    if s.get("batch_id") == batch_id or tid
                ]
        self._send_json({
            "cancelled_task_ids": cancelled,
            "already_terminal_task_ids": [],
        })

    def _watch(self, body: dict) -> None:
        if "wait_seconds" in body:
            # WatchTaskRequest has no wait_seconds; the strict server decoder
            # rejects it.
            self._send_json(
                _kratos_error(400, "INVALID_ARGUMENT", "unknown field wait_seconds"),
                status=400,
            )
            return
        tid = body.get("task_id") or "batch-task"
        if body.get("since_event_id") == "expired":
            self._sse([
                (
                    "error",
                    _kratos_error(
                        412,
                        "CURSOR_OUT_OF_RANGE",
                        "requested cursor is older than the oldest retained event",
                        {
                            "requested_since_event_id": "expired",
                            "oldest_available_event_id": "42",
                            "newest_event_id": "100",
                        },
                    ),
                )
            ])
            return
        if tid == "block-me":
            # Silent stream: the client must sit in its poll loop until the
            # wait deadline or an interrupt.
            try:
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                time.sleep(25)
            except (BrokenPipeError, ConnectionResetError):
                pass
            return

        def _event(eid: str, kind: str, **extra) -> dict:
            payload = {
                "event_id": eid,  # int64 → JSON string under protojson
                "event_at": "0",
                "task_id": tid,
                "batch_id": "",
                "kind": kind,
                "progress_summary": "",
            }
            payload.update(extra)
            return payload

        self._sse([
            ("message", _event("1", "TASK_EVENT_KIND_PROGRESS", progress_summary="step 1")),
            ("message", _event("2", "TASK_EVENT_KIND_PROGRESS", progress_summary="step 2")),
            (
                "message",
                _event(
                    "3",
                    "TASK_EVENT_KIND_CHECKPOINT",
                    checkpoint={
                        "task_id": tid,
                        "checkpoint_id": "cp-1",
                        "summary": "Found 3 sources",
                        "fields": {},
                    },
                ),
            ),
            (
                "message",
                _event(
                    "4",
                    "TASK_EVENT_KIND_TERMINAL",
                    result=_task_result(
                        tid, "TASK_STATUS_COMPLETED", summary="done"
                    ),
                ),
            ),
        ])

    def log_message(self, *args) -> None:  # keep test output clean
        pass


@pytest.fixture()
def mock_gateway():
    _MockGatewayHandler.dispatched = {}
    server = ThreadingHTTPServer(("127.0.0.1", 0), _MockGatewayHandler)
    server.daemon_threads = True
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_address[1]}"
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


@pytest.fixture()
def gateway_env(mock_gateway, monkeypatch, tmp_path):
    """Point the tools module at the mock gateway + a temp ledger."""
    monkeypatch.setenv("INFA_GATEWAY_API_KEY", API_KEY)
    monkeypatch.setenv("INFA_GATEWAY_BASE_URL", mock_gateway)
    monkeypatch.setenv("INFA_MASTER_PLANNER_DB", str(tmp_path / "ledger.db"))
    mp_tools.reset_state()
    yield mock_gateway
    mp_tools.reset_state()


@pytest.fixture()
def fast_client(gateway_env):
    """Inject a client with a fast interrupt-poll cadence for watch tests."""
    client = GatewayClient(gateway_env, API_KEY, timeout_s=10, poll_interval_s=0.05)
    mp_tools._client = client
    return client
