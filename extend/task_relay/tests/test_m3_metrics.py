"""M3 Prometheus metrics endpoint tests."""

from __future__ import annotations

import pytest

pytestmark = pytest.mark.python_hub

from extend.task_relay.hub.bootstrap import wire_orchestration
from extend.task_relay.hub.metrics import inc, observe, render_prometheus, reset, set_gauge, snapshot
from extend.task_relay.hub.metrics_server import METRICS_PATH, create_metrics_app
from extend.task_relay.tests.conftest import make_task_spec


@pytest.fixture(autouse=True)
def _clean_metrics():
    reset()
    yield
    reset()


def test_render_prometheus_empty():
    assert render_prometheus() == ""


def test_render_prometheus_counters():
    inc("relay_tasks_dispatched_total", status="pending", batch="false")
    inc("relay_tasks_terminal_total", status="completed")
    body = render_prometheus()
    assert "# TYPE relay_tasks_dispatched_total counter" in body
    assert 'relay_tasks_dispatched_total{batch="false",status="pending"} 1.0' in body
    assert 'relay_tasks_terminal_total{status="completed"} 1.0' in body


def test_render_prometheus_gauge_and_summary():
    set_gauge(
        "relay_worker_sessions_active",
        1.0,
        worker_id="w1",
        session_modes="a,c",
    )
    observe("relay_batch_completion_seconds", 1.5, completion_mode="ANY")
    body = render_prometheus()
    assert "# TYPE relay_worker_sessions_active gauge" in body
    assert 'relay_worker_sessions_active{session_modes="a,c",worker_id="w1"} 1.0' in body
    assert "# TYPE relay_batch_completion_seconds summary" in body
    assert 'relay_batch_completion_seconds{completion_mode="ANY"}_sum 1.5' in body


def test_snapshot_label_format():
    inc("relay_tasks_claimed_total", worker_id="w1")
    snap = snapshot()
    assert 'relay_tasks_claimed_total{worker_id="w1"}' in snap


@pytest.mark.asyncio
async def test_metrics_http_endpoint():
    from aiohttp.test_utils import TestClient, TestServer

    app = create_metrics_app()
    inc("relay_tasks_dispatched_total", status="pending", batch="false")
    async with TestClient(TestServer(app)) as client:
        resp = await client.get(METRICS_PATH)
        assert resp.status == 200
        body = await resp.text()
        assert 'relay_tasks_dispatched_total{batch="false",status="pending"} 1.0' in body


@pytest.mark.asyncio
async def test_dispatch_increments_metrics(router):
    await router.dispatch_task(make_task_spec(task_id="m1"), master_session_id="sess")
    snap = snapshot()
    assert snap.get('relay_tasks_dispatched_total{batch="false",status="pending"}') == 1.0


@pytest.mark.asyncio
async def test_complete_observes_latency(router, registry):
    await registry.announce("w1", session_modes="A")
    await router.dispatch_task(make_task_spec(task_id="lat1"), master_session_id="sess")
    await router.atomic_claim_for_poll("w1", 1)
    await router.on_complete("lat1", status="completed", summary="done")
    snap = snapshot()
    assert 'relay_task_latency_seconds{status="completed",worker_id="w1"}_count' in snap
    assert snap['relay_task_latency_seconds{status="completed",worker_id="w1"}_count'] == 1.0


@pytest.mark.asyncio
async def test_worker_announce_updates_session_gauge(router, registry, db, bus):
    wire_orchestration(router, db, bus)
    await registry.announce(
        worker_id="w1",
        session_modes="A",
        toolsets=[],
        max_concurrent=1,
        online_session_id="sess-1",
    )
    snap = snapshot()
    assert (
        snap.get('relay_worker_sessions_active{session_modes="a",worker_id="w1"}') == 1.0
    )
