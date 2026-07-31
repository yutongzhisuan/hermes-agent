"""M2 ContextRef and inline_gzip resolution tests."""

from __future__ import annotations

import gzip
import hashlib
from unittest.mock import patch

import pytest

from extend.task_relay.worker.context_loader import ContextLoadError, resolve_context_payload


@pytest.mark.asyncio
async def test_inline_gzip_sha256_verifies():
    plaintext = "hello context"
    raw = gzip.compress(plaintext.encode("utf-8"))
    digest = hashlib.sha256(plaintext.encode("utf-8")).hexdigest()
    result = await resolve_context_payload(
        {"inline_gzip": {"gzip_data": raw, "sha256": digest}}
    )
    assert result == plaintext


@pytest.mark.asyncio
async def test_inline_gzip_sha256_mismatch_raises():
    raw = gzip.compress(b"data")
    with pytest.raises(ContextLoadError, match="sha256 mismatch"):
        await resolve_context_payload(
            {"inline_gzip": {"gzip_data": raw, "sha256": "0" * 64}}
        )


@pytest.mark.asyncio
async def test_context_ref_fetch_and_verify():
    plaintext = "remote context payload"
    digest = hashlib.sha256(plaintext.encode("utf-8")).hexdigest()

    class FakeResponse:
        status = 200

        async def read(self) -> bytes:
            return plaintext.encode("utf-8")

        async def __aenter__(self):
            return self

        async def __aexit__(self, *args):
            return False

    class FakeSession:
        def get(self, uri, timeout=120):
            return FakeResponse()

        async def __aenter__(self):
            return self

        async def __aexit__(self, *args):
            return False

    with patch("aiohttp.ClientSession", return_value=FakeSession()):
        result = await resolve_context_payload(
            {"ref": {"uri": "https://example.com/context.txt", "sha256": digest}}
        )
    assert result == plaintext
