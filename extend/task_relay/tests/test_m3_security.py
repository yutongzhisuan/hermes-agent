"""M3 inline context encryption and ACL audit log tests."""

from __future__ import annotations

import json

import pytest

pytestmark = pytest.mark.python_hub

from extend.task_relay.hub.audit_log import record_acl_dispatch
from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.context_crypto import (
    decrypt_context_json,
    encrypt_context_json,
    should_encrypt_context,
)
from extend.task_relay.hub.models import Task
from extend.task_relay.hub.run_payload import build_run_payload
from extend.task_relay.tests.conftest import make_task_spec


def test_should_encrypt_inline_only():
    assert should_encrypt_context(json.dumps({"inline": "hello"}))
    assert not should_encrypt_context(json.dumps({"ref": {"uri": "https://x"}}))


def test_encrypt_decrypt_roundtrip():
    original = json.dumps({"inline": "secret prompt"})
    stored = encrypt_context_json(original, "secret")
    assert stored != original
    restored = decrypt_context_json(stored, "secret")
    assert restored == {"inline": "secret prompt"}


@pytest.mark.asyncio
async def test_run_payload_decrypts_encrypted_context(db):
    stored = encrypt_context_json(json.dumps({"inline": "worker-visible"}), "secret")
    task = Task(
        task_id="enc1",
        goal="g",
        callback_topic="topic-1",
        created_at=1.0,
        context_json=stored,
    )
    await db.insert_task(task)
    claimed = type("Claimed", (), {"attempt": 1, "timeout_seconds": 60, "claim_token": "tok"})()
    payload = await build_run_payload(
        db,
        "enc1",
        claimed,
        decrypt_secret="secret",
        encrypt_at_rest=True,
    )
    assert payload["context"] == {"inline": "worker-visible"}


@pytest.mark.asyncio
async def test_dispatch_with_acl_writes_audit_log(router, db):
    spec = make_task_spec(
        task_id="acl1",
        target_worker="worker-a",
        allowed_worker_ids_json=json.dumps(["worker-a"]),
    )
    await router.dispatch_task(spec, "master-session")
    cursor = await db._conn.execute(
        "SELECT action, payload_json FROM audit_log WHERE task_id = ?",
        ("acl1",),
    )
    row = await cursor.fetchone()
    assert row is not None
    assert row["action"] == "dispatch_acl"
    payload = json.loads(row["payload_json"])
    assert payload["target_worker"] == "worker-a"
    assert payload["allowed_worker_ids"] == ["worker-a"]


@pytest.mark.asyncio
async def test_record_acl_dispatch_noop_without_acl(db):
    task = Task(task_id="plain1", goal="g", callback_topic="t", created_at=1.0)
    await record_acl_dispatch(db, task, master_session_id="m1")
    cursor = await db._conn.execute("SELECT COUNT(*) AS c FROM audit_log")
    row = await cursor.fetchone()
    assert row["c"] == 0
