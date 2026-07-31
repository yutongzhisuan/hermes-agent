"""Go Hub E2E: encrypt-at-rest context and ACL audit log."""

from __future__ import annotations

import json

import pytest

pytest_plugins = ["extend.task_relay.tests.go_hub_runner"]

from extend.task_relay.gen.py import task_relay_v1_pb2 as pb
from extend.task_relay.gen.py.task_relay_v1_grpc import TaskRelayStub
from extend.task_relay.tests.go_hub_e2e import ws_connect, ws_poll, ws_recv_result
from extend.task_relay.tests.go_hub_runner import GoHubRunner, bearer_metadata
from extend.task_relay.tests.live_hub import (
    HubLaunchConfig,
    read_audit_log_count,
    read_task_row,
    start_live_hub,
    stop_live_hub,
)


@pytest.mark.asyncio
async def test_go_hub_encrypts_context_at_rest(tmp_path):
    hub = await start_live_hub(tmp_path, HubLaunchConfig(encrypt_inline_context=True))
    try:
        plain = json.dumps({"key": "secret-value"})
        stub = TaskRelayStub(hub.grpc_channel)
        spec = pb.TaskSpec(task_id="sec-1", goal="encrypt", callback_topic="sec-t")
        spec.context.inline = plain
        await stub.DispatchTask(
            pb.DispatchTaskRequest(spec=spec, master_session_id="m1"),
            metadata=bearer_metadata(hub.master_jwt),
        )
        row = read_task_row(hub, "sec-1")
        stored = row.get("context_json") or ""
        assert stored != plain
        assert "secret-value" not in stored
        ws, _ = await ws_connect(hub, "w1")
        try:
            poll = await ws_poll(ws)
            run = poll["tasks"][0]["run"]
            ctx = run.get("context")
            if isinstance(ctx, dict):
                inline = ctx.get("inline", "")
                assert json.loads(inline) == json.loads(plain)
            else:
                assert ctx == plain or json.loads(ctx) == json.loads(plain)
        finally:
            await ws.close()
    finally:
        await stop_live_hub(hub)


@pytest.mark.asyncio
async def test_go_hub_acl_dispatch_writes_audit_log(go_hub: GoHubRunner):
    stub = TaskRelayStub(go_hub.grpc_channel)
    spec = pb.TaskSpec(
        task_id="acl-1",
        goal="acl test",
        callback_topic="acl-t",
        target_worker="w-target",
    )
    spec.allowed_worker_ids.append("w-allowed")
    await stub.DispatchTask(
        pb.DispatchTaskRequest(spec=spec, master_session_id="master-acl"),
        metadata=bearer_metadata(go_hub.master_jwt),
    )
    assert read_audit_log_count(go_hub.live, "acl-1") >= 1
