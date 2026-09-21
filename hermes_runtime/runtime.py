"""Subprocess-first SDK for headless ``xhermes serve``."""

from __future__ import annotations

import os
import secrets
import signal
import subprocess
import sys
import threading
import time
from dataclasses import dataclass
from typing import Any
from urllib.parse import quote

from hermes_constants import get_hermes_home

from ._ready import parse_ready_port
from ._spawn import find_xhermes_executable
from ._stdout import read_stdout_chunk
from .exceptions import RuntimeBinaryNotFound, RuntimeStartError


@dataclass(frozen=True)
class RuntimeInfo:
    ws_url: str
    base_url: str
    token: str
    port: int
    pid: int


class HermesRuntime:
    """Spawn and manage a headless ``xhermes serve`` backend process."""

    def __init__(
        self,
        *,
        hermes_home: str | os.PathLike[str] | None = None,
        profile: str | None = None,
        host: str = "127.0.0.1",
        port: int = 0,
        extra_env: dict[str, str] | None = None,
        xhermes_executable: str | None = None,
    ) -> None:
        self._hermes_home = str(hermes_home) if hermes_home is not None else str(get_hermes_home())
        self._profile = profile
        self._host = host
        self._port = port
        self._extra_env = dict(extra_env or {})
        self._xhermes_executable = xhermes_executable
        self._proc: subprocess.Popen[bytes] | None = None
        self._token: str | None = None
        self._info: RuntimeInfo | None = None
        self._log_tail: list[str] = []
        self._log_lock = threading.Lock()

    @property
    def info(self) -> RuntimeInfo | None:
        return self._info

    def is_running(self) -> bool:
        return self._proc is not None and self._proc.poll() is None

    def start(self, *, timeout_s: float = 90.0) -> RuntimeInfo:
        if self.is_running():
            raise RuntimeStartError("HermesRuntime.start() called while backend is already running")

        executable = find_xhermes_executable(self._xhermes_executable)
        if not executable:
            raise RuntimeBinaryNotFound(
                "Could not find the xhermes executable. Install xhermes-agent or pass xhermes_executable=."
            )

        token = secrets.token_urlsafe(32)
        argv = []
        if self._profile:
            argv.extend(["--profile", self._profile])
        argv.extend(["serve", "--host", self._host, "--port", str(self._port)])

        env = os.environ.copy()
        env.update(self._extra_env)
        env["XHERMES_HOME"] = self._hermes_home
        env["XHERMES_SERVE_HEADLESS"] = "1"
        env["XHERMES_DASHBOARD_SESSION_TOKEN"] = token
        env["PYTHONUNBUFFERED"] = "1"

        popen_kwargs: dict[str, Any] = {
            "env": env,
            "stdout": subprocess.PIPE,
            "stderr": subprocess.STDOUT,
        }
        if sys.platform == "win32":
            popen_kwargs["creationflags"] = subprocess.CREATE_NEW_PROCESS_GROUP  # type: ignore[attr-defined]
        else:
            popen_kwargs["start_new_session"] = True

        proc = subprocess.Popen([executable, *argv], **popen_kwargs)
        self._proc = proc
        self._token = token
        self._log_tail.clear()

        buffer = bytearray()
        deadline = time.monotonic() + timeout_s
        port: int | None = None

        assert proc.stdout is not None
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                raise RuntimeStartError(self._format_start_failure("backend exited before ready"))

            wait_s = min(0.05, max(0.0, deadline - time.monotonic()))
            chunk = read_stdout_chunk(proc.stdout, wait_s)
            if chunk is None:
                continue

            self._record_log(chunk)
            port = parse_ready_port(chunk, buffer) or port
            if port is not None:
                break

        if port is None:
            self.stop(grace_s=0)
            raise RuntimeStartError(self._format_start_failure(f"timed out after {timeout_s}s waiting for ready"))

        scheme = "ws" if self._host not in ("0.0.0.0", "::") else "ws"
        http_scheme = "http"
        ws_url = f"{scheme}://{self._host}:{port}/api/ws?token={quote(token)}"
        base_url = f"{http_scheme}://{self._host}:{port}"
        self._info = RuntimeInfo(
            ws_url=ws_url,
            base_url=base_url,
            token=token,
            port=port,
            pid=proc.pid,
        )
        return self._info

    def stop(self, *, grace_s: float = 10.0) -> None:
        proc = self._proc
        if proc is None:
            return
        if proc.poll() is not None:
            self._proc = None
            return

        try:
            if sys.platform == "win32":
                proc.send_signal(signal.CTRL_BREAK_EVENT)  # type: ignore[attr-defined]
            else:
                os.killpg(proc.pid, signal.SIGTERM)
        except (ProcessLookupError, OSError):
            pass

        try:
            proc.wait(timeout=grace_s)
        except subprocess.TimeoutExpired:
            try:
                if sys.platform == "win32":
                    proc.kill()
                else:
                    os.killpg(proc.pid, signal.SIGKILL)
            except (ProcessLookupError, OSError):
                pass
            proc.wait(timeout=5)
        finally:
            self._proc = None

    def __enter__(self) -> HermesRuntime:
        return self

    def __exit__(self, *exc: object) -> None:
        self.stop()

    def _record_log(self, chunk: bytes) -> None:
        text = chunk.decode("utf-8", errors="replace")
        with self._log_lock:
            self._log_tail.extend(text.splitlines())
            if len(self._log_tail) > 80:
                self._log_tail = self._log_tail[-80:]

    def _format_start_failure(self, reason: str) -> str:
        with self._log_lock:
            tail = "\n".join(self._log_tail[-20:])
        if tail:
            return f"{reason}\n--- backend log tail ---\n{tail}"
        return reason
