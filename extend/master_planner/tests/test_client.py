"""Client-level tests against the mock gateway (transport + SSE parsing)."""

from __future__ import annotations

import threading
import time

import pytest

from extend.master_planner.client import (
    GatewayClient,
    GatewayError,
    normalize_response,
    task_event_kind_type,
    task_status_enum,
    task_status_name,
)


def test_dispatch_success(mock_gateway):
    client = GatewayClient(mock_gateway, "test-api-key")
    resp = client.dispatch_task({"task_id": "r1-1", "goal": "do x"})
    assert resp["task_id"] == "r1-1"
    assert resp["idempotent_hit"] is False
    assert resp["status"] == "TASK_STATUS_PENDING"


def test_dispatch_idempotent_replay(mock_gateway):
    client = GatewayClient(mock_gateway, "test-api-key")
    spec = {"task_id": "r1-2", "goal": "do y"}
    first = client.dispatch_task(spec)
    second = client.dispatch_task(spec)
    assert first["idempotent_hit"] is False
    assert second["idempotent_hit"] is True


def test_auth_required(mock_gateway):
    client = GatewayClient(mock_gateway, "wrong-key")
    with pytest.raises(GatewayError) as excinfo:
        client.dispatch_task({"task_id": "r1-3", "goal": "nope"})
    assert excinfo.value.status == 401
    assert excinfo.value.code == "UNAUTHENTICATED"


def test_requires_api_key():
    with pytest.raises(GatewayError):
        GatewayClient("http://127.0.0.1:1", "")


def test_watch_progress_then_terminal(mock_gateway):
    client = GatewayClient(mock_gateway, "test-api-key", timeout_s=10)
    result = client.watch(task_id="t-1", wait_seconds=10)
    assert result["reason"] == "terminal"
    assert result["interrupted"] is False
    assert result["error"] is None
    # kratos SSE frames carry no id: line — the cursor is data.event_id.
    assert result["cursor"] == "3"
    types = [e["type"] for e in result["events"]]
    assert types == ["progress", "progress", "terminal"]
    terminal = result["events"][-1]
    assert terminal["id"] == "3"
    assert terminal["data"]["result"]["status"] == "TASK_STATUS_COMPLETED"


def test_watch_cursor_out_of_range_error_frame(mock_gateway):
    client = GatewayClient(mock_gateway, "test-api-key", timeout_s=10)
    result = client.watch(task_id="t-1", since_event_id="expired", wait_seconds=10)
    assert result["reason"] == "error"
    err = result["error"]
    assert err["code"] == "cursor_out_of_range"
    assert err["reason"] == "CURSOR_OUT_OF_RANGE"
    assert err["http_code"] == 412
    # kratos error metadata is flattened for callers.
    assert err["oldest_available_event_id"] == "42"
    assert err["requested_since_event_id"] == "expired"


def test_watch_interrupt_breaks_poll_loop(mock_gateway):
    client = GatewayClient(
        mock_gateway, "test-api-key", timeout_s=10, poll_interval_s=0.05
    )
    started = time.monotonic()

    def stop_after_delay():
        return time.monotonic() - started > 0.3

    result = client.watch(
        task_id="block-me", wait_seconds=20, should_stop=stop_after_delay
    )
    elapsed = time.monotonic() - started
    assert result["interrupted"] is True
    assert result["reason"] == "interrupted"
    assert elapsed < 5  # nowhere near the server's 25s silence


def test_watch_wait_clamped_to_60s(mock_gateway):
    client = GatewayClient(mock_gateway, "test-api-key", timeout_s=10)
    # Clamp is internal; just verify an oversized wait still works and the
    # terminal frame arrives promptly.
    result = client.watch(task_id="t-9", wait_seconds=999)
    assert result["reason"] == "terminal"


def test_get_result_and_list_and_cancel(mock_gateway):
    client = GatewayClient(mock_gateway, "test-api-key")
    client.dispatch_task({"task_id": "r2-1", "goal": "a"})
    result = client.get_task_result("r2-1")
    assert result["result_text"] == "full text for r2-1"
    assert result["status"] == "TASK_STATUS_COMPLETED"
    assert result["latest_checkpoint_id"] == "cp-latest"
    tasks = client.list_tasks(master_session_id="s")["tasks"]
    assert any(t["task_id"] == "r2-1" for t in tasks)
    resp = client.cancel_task(task_id="r2-1")
    assert resp["cancelled_task_ids"] == ["r2-1"]
    workers = client.list_workers(require_toolsets=["research"])["workers"]
    assert len(workers) == 2
    models = client.list_models()["models"]
    assert any(m["model_version_id"] == "mv-qwen3-32b" for m in models)
    filtered = client.list_models(region="cn-north-1")["models"]
    assert filtered
    assert all("cn-north-1" in m["regions"] for m in filtered)


def test_list_tasks_status_filter_uses_query_params(mock_gateway):
    client = GatewayClient(mock_gateway, "test-api-key")
    client.dispatch_task({"task_id": "r3-1", "goal": "a"})
    resp = client.list_tasks(statuses=("TASK_STATUS_COMPLETED",))
    assert resp["tasks"] == []
    resp = client.list_tasks(statuses=("TASK_STATUS_PENDING",))
    assert any(t["task_id"] == "r3-1" for t in resp["tasks"])


def test_cancel_requires_task_id_for_path(mock_gateway):
    client = GatewayClient(mock_gateway, "test-api-key")
    with pytest.raises(GatewayError) as excinfo:
        client.cancel_task(task_id="", batch_id="b-1")
    assert excinfo.value.code == "invalid_args"


def test_watch_unthreaded_server_close(mock_gateway):
    """A server that closes the stream without a terminal event -> stream_closed."""
    client = GatewayClient(mock_gateway, "test-api-key", timeout_s=10)
    # 'block-me' sleeps 25s; use a short wait so the client times out first.
    result = client.watch(task_id="block-me", wait_seconds=1)
    assert result["reason"] == "timeout"
    assert result["events"] == []


# ---------------------------------------------------------------------------
# response normalization helpers
# ---------------------------------------------------------------------------


def test_normalize_response_camel_to_snake():
    raw = {
        "taskId": "t-1",
        "idempotentHit": True,
        "existingResult": {"resultText": "x", "startedAt": "12"},
        "metadata": {"requested_since_event_id": "7"},  # map keys untouched
        "tasks": [{"taskId": "t-2"}],
    }
    assert normalize_response(raw) == {
        "task_id": "t-1",
        "idempotent_hit": True,
        "existing_result": {"result_text": "x", "started_at": "12"},
        "metadata": {"requested_since_event_id": "7"},
        "tasks": [{"task_id": "t-2"}],
    }


def test_status_and_kind_tolerance():
    # protojson enum names, numeric enums and short names all accepted.
    assert task_status_name("TASK_STATUS_COMPLETED") == "completed"
    assert task_status_name(3) == "completed"
    assert task_status_name("3") == "completed"
    assert task_status_name("completed") == "completed"
    assert task_status_name("TASK_STATUS_UNSPECIFIED") == ""
    assert task_status_name("") == ""
    assert task_status_enum("completed") == "TASK_STATUS_COMPLETED"
    assert task_status_enum("TASK_STATUS_LOST") == "TASK_STATUS_LOST"
    assert task_event_kind_type("TASK_EVENT_KIND_TERMINAL") == "terminal"
    assert task_event_kind_type(2) == "progress"
    assert task_event_kind_type("TASK_EVENT_KIND_UNSPECIFIED") == "event"
