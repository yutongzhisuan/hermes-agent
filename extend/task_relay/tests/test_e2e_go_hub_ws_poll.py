"""Go Hub E2E: two-step poll, nack, checkpoint via WebSocket."""

from __future__ import annotations

import base64
import json

import pytest

pytest_plugins = ["extend.task_relay.tests.go_hub_runner"]

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.tests.go_hub_e2e import get_task_status, jsonrpc_request, ws_connect, ws_nack, ws_poll, ws_recv_result
from extend.task_relay.tests.go_hub_runner import GoHubRunner, bearer_metadata


@pytest.mark.asyncio
async def test_go_hub_two_step_offer_preview_without_context(go_hub: GoHubRunner):
    stub = TaskRelayStub(go_hub.grpc_channel)
    spec = pb.TaskSpec(
        task_id="ts-go-1",
        goal="preview goal",
        callback_topic="ts-topic",
    )
    spec.context.inline = json.dumps({"secret": "hidden"})
    spec.toolsets.append("terminal")
    await stub.DispatchTask(
        pb.DispatchTaskRequest(spec=spec, master_session_id="m1"),
        metadata=bearer_metadata(go_hub.master_jwt),
    )
    ws, _ = await ws_connect(go_hub.live, "w1", toolsets=["terminal"])
    try:
        poll = await ws_poll(ws, atomic=False)
        assert poll["offered"] is True
        task_info = poll["tasks"][0]
        assert task_info["claimed"] is False
        assert "preview" in task_info
        assert "run" not in task_info
        assert "hidden" not in json.dumps(task_info)
        await ws.send(
            jsonrpc_request(
                3,
                "worker.claim",
                {"task_id": "ts-go-1", "claim_token": task_info["claim_token"]},
            )
        )
        claim = await ws_recv_result(ws)
        assert claim["claimed"] is True
        assert claim["run"]["goal"] == "preview goal"
    finally:
        await ws.close()


@pytest.mark.asyncio
async def test_go_hub_two_step_nack_releases_offer(go_hub: GoHubRunner):
    stub = TaskRelayStub(go_hub.grpc_channel)
    await stub.DispatchTask(
        pb.DispatchTaskRequest(
            spec=pb.TaskSpec(task_id="ts-go-2", goal="nack", callback_topic="ts-topic"),
            master_session_id="m1",
        ),
        metadata=bearer_metadata(go_hub.master_jwt),
    )
    ws, _ = await ws_connect(go_hub.live, "w1")
    try:
        poll = await ws_poll(ws, atomic=False)
        token = poll["tasks"][0]["claim_token"]
        await ws_nack(ws, "ts-go-2", token)
        poll2 = await ws_poll(ws, msg_id=5, atomic=False)
        assert poll2.get("offered") is True
    finally:
        await ws.close()


@pytest.mark.asyncio
async def test_go_hub_checkpoint_persists_with_fields(go_hub: GoHubRunner):
    stub = TaskRelayStub(go_hub.grpc_channel)
    task_id = "cp-go-1"
    await stub.DispatchTask(
        pb.DispatchTaskRequest(
            spec=pb.TaskSpec(task_id=task_id, goal="checkpoint", callback_topic="cp-t"),
            master_session_id="m1",
        ),
        metadata=bearer_metadata(go_hub.master_jwt),
    )
    ws, _ = await ws_connect(go_hub.live, "w1")
    try:
        poll = await ws_poll(ws)
        assert poll["tasks"][0]["task_id"] == task_id
        blob = base64.b64encode(b"resume-state").decode()
        await ws.send(
            jsonrpc_request(
                3,
                "task.checkpoint",
                {
                    "task_id": task_id,
                    "checkpoint_id": "cp-1",
                    "summary": "half done",
                    "resume_blob": blob,
                    "fields_json": json.dumps({"step": 1}),
                },
            )
        )
        result = await ws_recv_result(ws)
        assert result["checkpoint_id"] == "cp-1"
        status = await get_task_status(go_hub.live, task_id)
        assert status == "running"
    finally:
        await ws.close()
