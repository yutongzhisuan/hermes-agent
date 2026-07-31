"""M3 signed ContextRef validation tests."""

from __future__ import annotations

import hashlib
import json

import pytest
from grpclib.const import Status
from grpclib.exceptions import GRPCError

from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.context_ref import ContextRefSignError, sign_context_ref, verify_context_ref
from extend.task_relay.hub.grpc_server import _validate_context_json


def test_sign_and_verify_context_ref():
    ref = {
        "uri": "https://example.com/context.txt",
        "sha256": hashlib.sha256(b"payload").hexdigest(),
        "content_encoding": "gzip",
    }
    ref["signature"] = sign_context_ref(ref, "secret")
    verify_context_ref(ref, "secret")


def test_verify_rejects_tampered_ref():
    ref = {
        "uri": "https://example.com/context.txt",
        "sha256": "abc",
        "content_encoding": "",
        "signature": sign_context_ref(
            {
                "uri": "https://example.com/context.txt",
                "sha256": "abc",
                "content_encoding": "",
            },
            "secret",
        ),
    }
    ref["uri"] = "https://evil.example/steal"
    with pytest.raises(ContextRefSignError, match="invalid"):
        verify_context_ref(ref, "secret")


def test_validate_context_json_requires_signature_when_configured():
    ref = {"uri": "https://example.com/x", "sha256": "abc"}
    context_json = json.dumps({"ref": ref})
    cfg = HubConfig(jwt_secret="secret", require_signed_context_ref=True)
    with pytest.raises(GRPCError) as exc:
        _validate_context_json(context_json, cfg)
    assert exc.value.status == Status.INVALID_ARGUMENT


def test_validate_context_json_accepts_signed_ref():
    ref = {
        "uri": "https://example.com/x",
        "sha256": "abc",
        "content_encoding": "",
    }
    ref["signature"] = sign_context_ref(ref, "secret")
    context_json = json.dumps({"ref": ref})
    cfg = HubConfig(jwt_secret="secret", require_signed_context_ref=True)
    _validate_context_json(context_json, cfg)
