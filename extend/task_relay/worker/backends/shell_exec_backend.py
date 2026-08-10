"""Shell-exec task backend: runs a shell command from task params.

Policy and audit live on the master side; this backend enforces hard
limits only: a timeout ceiling, per-stream output caps, and
process-group kill on timeout or cancellation.
"""

from __future__ import annotations

import asyncio
import json
import os
import signal

from extend.task_relay.worker.task_executor import (
    OnCheckpoint,
    OnProgress,
    TaskCompletePayload,
    TaskRunPayload,
)


def _kill_group(proc: asyncio.subprocess.Process) -> None:
    try:
        os.killpg(proc.pid, signal.SIGKILL)
    except (ProcessLookupError, PermissionError):
        pass
    except AttributeError:
        pass
    if proc.returncode is None:
        try:
            proc.kill()
        except ProcessLookupError:
            pass


class ShellExecBackend:
    MAX_TIMEOUT = 600
    MAX_OUTPUT = 1 << 20

    def __init__(self, max_timeout: int = MAX_TIMEOUT) -> None:
        self._max_timeout = min(max_timeout, self.MAX_TIMEOUT)

    async def run(
        self,
        run: TaskRunPayload,
        on_progress: OnProgress,
        on_checkpoint: OnCheckpoint,
        cancel_event: asyncio.Event,
    ) -> TaskCompletePayload:
        params = run.params or {}
        cmd = params.get("cmd", "")
        if not cmd:
            return TaskCompletePayload(
                status="failed",
                summary="shell exec failed: no command",
                error="params['cmd'] is required for the shell-exec backend",
            )
        workdir = params.get("workdir") or None
        try:
            requested = int(params.get("timeout_seconds") or run.timeout_seconds or 60)
        except (TypeError, ValueError):
            requested = 60
        timeout = max(1, min(requested, self._max_timeout))

        await on_progress(f"shell exec started: {cmd[:80]}")

        proc = await asyncio.create_subprocess_shell(
            cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            cwd=workdir,
            start_new_session=True,
        )

        async def _watch_cancel() -> None:
            await cancel_event.wait()
            _kill_group(proc)

        watcher = asyncio.create_task(_watch_cancel())
        timed_out = False
        try:
            stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout)
        except asyncio.TimeoutError:
            timed_out = True
            _kill_group(proc)
            stdout, stderr = await proc.communicate()
        finally:
            watcher.cancel()
            try:
                await watcher
            except asyncio.CancelledError:
                pass

        canceled = cancel_event.is_set() and not timed_out

        stdout = (stdout or b"")[-self.MAX_OUTPUT :]
        stderr = (stderr or b"")[-self.MAX_OUTPUT :]
        stdout_text = stdout.decode("utf-8", errors="replace")
        stderr_text = stderr.decode("utf-8", errors="replace")
        exit_code = proc.returncode if proc.returncode is not None else -1

        payload = json.dumps({
            "exit_code": exit_code,
            "stdout": stdout_text,
            "stderr": stderr_text,
            "timed_out": timed_out,
            "canceled": canceled,
        })

        if timed_out:
            status = "failed"
            summary = f"shell exec timed out after {timeout}s"
            error = f"command exceeded timeout of {timeout}s"
        elif canceled:
            status = "cancelled"
            summary = "shell exec cancelled by worker"
            error = None
        elif exit_code == 0:
            status = "completed"
            summary = "shell exec exit=0"
            error = None
        else:
            status = "failed"
            summary = f"shell exec exit={exit_code}"
            error = (
                stderr_text.strip().splitlines()[-1][:200]
                if stderr_text.strip()
                else f"exit code {exit_code}"
            )

        return TaskCompletePayload(
            status=status,
            summary=summary,
            result_text=stdout_text[-4096:],
            fields={"extensions": {"exec": payload}},
            error=error,
        )
