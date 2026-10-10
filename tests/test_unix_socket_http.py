"""Tests for OpenAI-compatible inference over unix:// base URLs."""

from __future__ import annotations

import sys

import pytest


@pytest.mark.skipif(sys.platform == "win32", reason="UDS not available on Windows")
def test_parse_unix_socket_url_defaults_to_v1():
    from extend.unix_socket_http import parse_unix_socket_url

    socket_path, openai_base = parse_unix_socket_url("unix:///tmp/llm.sock")
    assert socket_path == "/tmp/llm.sock"
    assert openai_base == "http://localhost/v1"


@pytest.mark.skipif(sys.platform == "win32", reason="UDS not available on Windows")
def test_parse_unix_socket_url_rejects_non_unix():
    from extend.unix_socket_http import parse_unix_socket_url

    with pytest.raises(ValueError):
        parse_unix_socket_url("http://127.0.0.1:8080/v1")


@pytest.mark.skipif(sys.platform == "win32", reason="UDS not available on Windows")
def test_parse_unix_socket_url_requires_absolute_path():
    from extend.unix_socket_http import parse_unix_socket_url

    with pytest.raises(ValueError):
        parse_unix_socket_url("unix://tmp/llm.sock")


@pytest.mark.skipif(sys.platform == "win32", reason="UDS not available on Windows")
def test_resolve_rewrites_unix_base_url():
    from extend.unix_socket_http import resolve_openai_base_url

    openai_base, socket_path = resolve_openai_base_url("unix:///var/run/llama.sock")
    assert socket_path == "/var/run/llama.sock"
    assert openai_base == "http://localhost/v1"


@pytest.mark.skipif(sys.platform == "win32", reason="UDS not available on Windows")
def test_resolve_passes_through_http():
    from extend.unix_socket_http import resolve_openai_base_url

    openai_base, socket_path = resolve_openai_base_url("https://api.example.com/v1")
    assert socket_path is None
    assert openai_base == "https://api.example.com/v1"


@pytest.mark.skipif(sys.platform == "win32", reason="UDS not available on Windows")
def test_is_unix_base_url():
    from extend.unix_socket_http import is_unix_base_url

    assert is_unix_base_url("unix:///tmp/llm.sock")
    assert is_unix_base_url("UNIX:///tmp/llm.sock")
    assert not is_unix_base_url("http://localhost/v1")
    assert not is_unix_base_url("/tmp/llm.sock")


@pytest.mark.skipif(sys.platform == "win32", reason="UDS not available on Windows")
def test_build_keepalive_uses_uds_for_unix_base_url(tmp_path):
    from agent.process_bootstrap import build_keepalive_http_client

    sock = str(tmp_path / "llm.sock")
    client = build_keepalive_http_client(f"unix://{sock}")
    assert client is not None
    try:
        pool = getattr(client._transport, "_pool", None)
        assert pool is not None
        assert getattr(pool, "_uds", None) == sock
    finally:
        client.close()


@pytest.mark.skipif(sys.platform == "win32", reason="UDS not available on Windows")
def test_build_keepalive_does_not_fallback_to_tcp_when_uds_fails(monkeypatch):
    import extend.unix_socket_http as unix_mod
    from agent.process_bootstrap import build_keepalive_http_client

    def _boom(*_args, **_kwargs):
        raise OSError("uds transport failed")

    monkeypatch.setattr(unix_mod, "build_unix_httpx_client", _boom)
    with pytest.raises(RuntimeError, match="unix://"):
        build_keepalive_http_client("unix:///tmp/llm.sock")


@pytest.mark.skipif(sys.platform == "win32", reason="UDS not available on Windows")
def test_aux_openai_client_refuses_unix_without_uds_transport(monkeypatch):
    from agent import auxiliary_client as aux

    monkeypatch.setattr(aux, "_openai_http_client_kwargs", lambda *_a, **_k: {})
    with pytest.raises(RuntimeError, match="unix://"):
        aux._create_openai_client(api_key="nokey", base_url="unix:///tmp/llm.sock")


@pytest.mark.skipif(sys.platform == "win32", reason="UDS not available on Windows")
def test_validate_base_url_accepts_unix():
    from agent.auxiliary_client import _validate_base_url

    _validate_base_url("unix:///tmp/llm.sock")


@pytest.mark.skipif(sys.platform == "win32", reason="UDS not available on Windows")
def test_validate_base_url_rejects_relative_unix():
    from agent.auxiliary_client import _validate_base_url

    with pytest.raises(RuntimeError):
        _validate_base_url("unix://tmp/llm.sock")


@pytest.mark.skipif(sys.platform == "win32", reason="UDS not available on Windows")
def test_infa_config_fallback_ignores_unix_base_url(monkeypatch):
    from extend.infa_provider import session as session_mod

    monkeypatch.setattr(session_mod, "load_session", lambda: None)

    def _fake_load_config():
        return {
            "model": {
                "api_key": "nokey",
                "base_url": "unix:///tmp/llm.sock",
            }
        }

    monkeypatch.setattr("hermes_cli.config.load_config", _fake_load_config)
    assert session_mod._config_api_credentials() is None
    with pytest.raises(session_mod.InfaAuthError):
        session_mod.resolve_runtime_credentials()
