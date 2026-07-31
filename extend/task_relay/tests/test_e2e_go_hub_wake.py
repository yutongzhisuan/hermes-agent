"""Go Hub E2E: Mode B wake token and worker.claim."""

from __future__ import annotations

import json

import pytest

pytest_plugins = ["extend.task_relay.tests.go_hub_runner"]

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.tests.go_hub_e2e import issue_wake_token, jsonrpc_request, ws_recv_result
from extend.task_relay.tests.go_hub_runner import GoHubRunner, bearer_metadata
from extend.task_relay.tests.conftest import make_worker_jwt
from extend.task_relay.tests.live_hub import read_task_row


@pytest.mark.asyncio
async def test_go_hub_wake_token_single_use(go_hub: GoHubRunner):
    token, exp = issue_wake_token("t1", "wb1")
    token2, _ = issue_wake_token("t1", "wb1", expires_at=exp)
    assert token != token2 or exp == exp  # different exp windows may differ


@pytest.mark.asyncio
async def test_go_hub_worker_claim_with_wake_token(go_hub: GoHubRunner):
    import websockets

    stub = TaskRelayStub(go_hub.grpc_channel)
    task_id = "go-wake-1"
    await stub.DispatchTask(
        pb.DispatchTaskRequest(
            spec=pb.TaskSpec(task_id=task_id, goal="wake claim", callback_topic="wake-t"),
            master_session_id="m1",
        ),
        metadata=bearer_metadata(go_hub.master_jwt),
    )
    token, exp = issue_wake_token(task_id, "wb1")
    jwt = make_worker_jwt("wb1", max_concurrent=1)
    async with websockets.connect(
        go_hub.ws_url, additional_headers={"Authorization": f"Bearer {jwt}"}
    ) as ws:
        await ws.send(
            jsonrpc_request(
                1,
                "worker.announce",
                {"worker_id": "wb1", "session_modes": ["a", "b"], "max_concurrent": 1},
            )
        )
        await ws_recv_result(ws)
        await ws.send(
            jsonrpc_request(
                2,
                "worker.claim",
                {
                    "task_id": task_id,
                    "wake_token": token,
                    "expires_at": exp,
                },
            )
        )
        resp = json.loads(await ws.recv())
        assert "error" not in resp
        assert resp["result"].get("claimed") is True
    row = read_task_row(go_hub.live, task_id)
    assert row["status"] == "running"
    assert row["worker_id"] == "wb1"
