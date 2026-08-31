"""ACP execution backend for the sub-agent executor.

Owned by XHermes (migrated from swarm-network ``worker/backends/acp_backend.py``)
because it runs a sub-agent task through an in-process XHermes ACP session managed
by :mod:`acp_adapter.session`. Progress is throttled and forwarded as
``task.progress`` frames; the final agent result drives ``task.complete``.

Cancel semantics:
- A normal ``task.cancel`` calls ``agent.interrupt()`` and, once the agent
  returns, settles the task as ``cancelled`` while salvaging any partial
  ``final_response`` as the summary.
- A timeout cancel (``task.cancel`` with ``reason`` equal to
  ``CANCEL_REASON_TIMEOUT``) still interrupts the agent, but the worker settles
  ``failed`` if it still owns settlement. The Hub will mark ``failed`` otherwise.

M1 scope: no L2 resume; cancelled tasks are settled immediately.
"""

from __future__ import annotations

import asyncio
import logging
import shutil
import tempfile
import threading
import time
from typing import Any

from extend.sub_agent.constants import CANCEL_REASON_TIMEOUT
from extend.sub_agent.executor_profile import ExecutorProfile
from extend.sub_agent.progress_policy import (
    PROGRESS_MODE_OFF,
    PROGRESS_MODE_TOOLS,
    SubAgentRuntimeOptions,
    default_sidecar_options,
    parse_sub_agent_options,
    runtime_options_from_params,
)
from extend.sub_agent.responses_payload import (
    ERROR_INVALID_ENVELOPE,
    build_response_object,
    parse_responses_envelope,
    serialize_response,
)
from extend.sub_agent.sub_agent_tools import ensure_sub_agent_tools_registered
from extend.sub_agent.task_context import TaskRunContext, bind_task_context, reset_task_context
from extend.sub_agent.local_runtime import (
    ERROR_MODEL_UNAVAILABLE,
    LocalRuntimeResolver,
    ModelBinding,
    ModelUnavailableError,
)
from extend.sub_agent.task_types import (
    OnCheckpoint,
    OnProgress,
    TaskBackend,
    TaskCompletePayload,
    TaskRunPayload,
)

logger = logging.getLogger("sub_agent.backends.acp")

# Brief preamble injected before sub-agent goals so headless XHermes executors know
# they are running a platform sub-task, not a local interactive session.
SUB_AGENT_EXECUTOR_PREAMBLE = (
    "[sub-agent executor — headless platform sub-task; no master session context]\n"
    "Execute the goal below. Return concise, concrete findings only.\n"
    "Use report_progress for meaningful milestones the master planner should see "
    "(sparingly — not every step).\n"
)


class AcpTaskBackend(TaskBackend):
    """Backend that executes sub-agent tasks via a XHermes ACP session."""

    def __init__(
        self,
        session_manager: Any | None = None,
        progress_interval_seconds: float = 5.0,
        *,
        stateless: bool = False,
        stateless_toolsets: list[str] | None = None,
        state_root: str | None = None,
        workdir_root: str | None = None,
        sandbox: str | None = None,
        sandbox_image: str | None = None,
        executor_profile: ExecutorProfile | None = None,
        local_runtime: LocalRuntimeResolver | None = None,
        sub_agent_options: SubAgentRuntimeOptions | None = None,
    ):
        """Initialize the backend.

        Args:
            session_manager: Optional ``acp_adapter.session.SessionManager``
                instance. When omitted, a fresh manager is created on first
                use — a ``StatelessSessionManager`` when *stateless* is set,
                else a plain ``SessionManager``. Tests can inject a fake
                manager here.
            progress_interval_seconds: Minimum seconds between ``task.progress``
                frames forwarded from the agent.
            stateless: Run each task as a disposable session: the agent is
                built with ``skip_memory=True`` and a narrowed toolset list
                (no memory / skills / session_search), the session transcript
                goes to an ephemeral store, the task runs in a temporary
                workdir, and both are deleted when the task ends. Nothing the
                task does can read or mutate the local user's memories,
                skills, or session history.
            stateless_toolsets: Toolsets granted to stateless tasks when the
                task itself does not request any. Defaults to
                ``DEFAULT_STATELESS_TOOLSETS``.
            state_root: Directory for the ephemeral session store (default:
                a fresh temp dir per manager).
            workdir_root: Parent directory for per-task temp workdirs
                (default: system temp). Ignored when *sandbox* is set.
            sandbox: Optional execution sandbox for stateless tasks.
                ``"docker"`` runs each task in its own disposable container
                (requires ``apply_sandbox_env`` to have configured the
                process terminal environment, and implies *stateless*).
            sandbox_image: Docker image for sandboxed tasks.
            executor_profile: Toolset whitelist enforced on every stateless /
                sandboxed session (see :mod:`extend.sub_agent.executor_profile`).
                Defaults to :class:`ExecutorProfile` with
                ``DEFAULT_EXECUTOR_TOOLSETS`` — no shell/browser tools for
                remote tasks. The profile is applied here, at session
                creation, so the narrowed list reaches
                ``AIAgent(enabled_toolsets=...)`` (tool registration layer),
                not just the prompt.
            local_runtime: Resolver for per-task model bindings (spec §13.4
                S4). When omitted, one is built from the
                ``ACP_LOCAL_RUNTIME_*`` / ``ACP_ALLOWED_MODELS`` env vars on
                first use — and only then: tasks without a model binding
                never touch the local runtime config.
        """
        self._session_manager = session_manager
        self._progress_interval_seconds = progress_interval_seconds
        self._stateless = stateless or bool(sandbox)
        self._stateless_toolsets = stateless_toolsets
        self._state_root = state_root
        self._workdir_root = workdir_root
        self._sandbox = sandbox
        self._sandbox_image = sandbox_image
        self._executor_profile = executor_profile or ExecutorProfile()
        self._local_runtime = local_runtime
        self._sub_agent_options = sub_agent_options

    @property
    def executor_profile(self) -> ExecutorProfile:
        """The toolset whitelist enforced on stateless/sandboxed sessions."""
        return self._executor_profile

    async def run(
        self,
        run: TaskRunPayload,
        on_progress: OnProgress,
        on_checkpoint: OnCheckpoint,
        cancel_event: asyncio.Event,
    ) -> TaskCompletePayload:
        """Create an ACP session, run the goal, and return a terminal payload."""
        manager = self._session_manager
        if manager is None:
            manager = self._create_default_manager()

        # Per-task model binding (spec §13.4 S4): local-runtime-first. A
        # bound model the local runtime cannot serve is a fail-fast —
        # model_unavailable lets the Hub rotate candidates; we never wait
        # and never silently fall back to another provider.
        binding = await self._resolve_model_binding(run)
        if isinstance(binding, TaskCompletePayload):
            return binding
        if binding is not None and not getattr(manager, "supports_model_binding", False):
            # Honoring the binding is mandatory: running the task on the
            # default model would silently violate the dispatch contract.
            msg = (
                f"model {run.model!r} bound but the session manager cannot "
                "apply per-task model overrides"
            )
            logger.warning("task %s: %s", run.task_id, msg)
            return TaskCompletePayload(
                status="failed",
                summary=msg,
                error=msg,
                error_code=ERROR_MODEL_UNAVAILABLE,
            )
        extra: dict[str, Any] = {"binding": binding} if binding is not None else {}

        # Responses API envelope (POST /v1/responses): when present, the
        # user message comes from the OpenAI ``input`` (no planner
        # preamble) and the terminal payload becomes a Response object.
        # toolsets stay single-sourced from run.toolsets — request.tools
        # is echo only. A malformed envelope fails fast with the code the
        # Hub/gateway can surface.
        parsed = parse_responses_envelope(run.params, run.goal, run.model)
        if parsed.present and not parsed.ok:
            return TaskCompletePayload(
                status="failed",
                summary=parsed.error,
                error=parsed.error,
                error_code=ERROR_INVALID_ENVELOPE,
            )
        user_message = parsed.user_message if parsed.present else _resume_goal(run)
        workdir: str | None = None
        if self._stateless:
            # Executor profile enforcement point: the task-requested toolsets
            # are intersected with the node operator's whitelist *before* the
            # session is built. The resolved list is passed through even when
            # empty — an empty whitelist result means "no tools", never a
            # fall back to broader manager defaults.
            #
            # Responses tasks (envelope present) MUST pass the explicit list
            # (no ``or None``): ``tools: []`` means no tools, not "defaults".
            # The goal path keeps ``or None`` so legacy planner tasks with no
            # requested toolsets still get the full whitelist.
            requested = list(run.toolsets)
            if parsed.present:
                toolsets = self._executor_profile.resolve(requested)
            else:
                toolsets = self._executor_profile.resolve(requested or None)
            logger.info(
                "task %s executor toolsets: %s (requested=%s, whitelist=%s)",
                run.task_id,
                toolsets,
                list(run.toolsets),
                self._executor_profile.announce_toolsets(),
            )
        if binding is not None:
            logger.info(
                "task %s model binding: model=%s -> %s",
                run.task_id,
                binding.model,
                binding.base_url,
            )
        if self._stateless and self._sandbox:
            # Sandboxed tasks run at a fixed container path; the per-session
            # disposable container replaces the host-side temp workdir.
            from extend.sub_agent.stateless import SANDBOX_CONTAINER_CWD

            state = manager.create_session(cwd=SANDBOX_CONTAINER_CWD, toolsets=toolsets, **extra)
        elif self._stateless:
            workdir = tempfile.mkdtemp(
                prefix=f"sub-agent-{run.task_id}-", dir=self._workdir_root
            )
            state = manager.create_session(cwd=workdir, toolsets=toolsets, **extra)
        else:
            state = manager.create_session(cwd=".", **extra)
        session_id = state.session_id
        agent = state.agent

        try:
            options = self._resolve_sub_agent_options(run)
            if self._stateless and "sub_agent" in self._executor_profile.allowed:
                ensure_sub_agent_tools_registered()
            ctx_token = bind_task_context(
                TaskRunContext(
                    task_id=run.task_id,
                    on_checkpoint=on_checkpoint,
                    options=options,
                )
            )
            try:
                return await self._run_session(
                    manager,
                    state,
                    run,
                    user_message,
                    on_progress,
                    on_checkpoint,
                    cancel_event,
                    options=options,
                    parsed=parsed,
                )
            finally:
                reset_task_context(ctx_token)
        finally:
            if self._stateless:
                self._discard_session(manager, session_id, workdir)

    async def _run_session(
        self,
        manager: Any,
        state: Any,
        run: TaskRunPayload,
        user_message: str,
        on_progress: OnProgress,
        on_checkpoint: OnCheckpoint,
        cancel_event: asyncio.Event,
        *,
        options: SubAgentRuntimeOptions,
        parsed: Any = None,
    ) -> TaskCompletePayload:
        """Drive the agent to a terminal state for an already-created session."""
        session_id = state.session_id
        agent = state.agent

        loop = asyncio.get_running_loop()

        # Throttled progress forwarding from the executor thread to the event loop.
        last_progress_at = 0.0
        progress_lock = threading.Lock()
        checkpoint_seq = 0

        def _format_progress(api_call_count: int, prev_tools: Any = None) -> str:
            if options.progress_mode == PROGRESS_MODE_OFF:
                return ""
            summary = f"step {api_call_count}"
            if options.progress_mode == PROGRESS_MODE_TOOLS and prev_tools:
                names: list[str] = []
                for tool in prev_tools:
                    if isinstance(tool, dict):
                        name = tool.get("name") or tool.get("function_name")
                        if name:
                            names.append(str(name))
                    elif isinstance(tool, str):
                        names.append(tool)
                if names:
                    summary = f"completed tools: {', '.join(names)}"
            return summary

        def _maybe_emit_step_checkpoint(api_call_count: int) -> None:
            nonlocal checkpoint_seq
            every = options.checkpoint_every_steps
            if every <= 0 or api_call_count <= 0 or api_call_count % every != 0:
                return
            checkpoint_seq += 1
            cp_id = f"cp-{run.task_id}-{checkpoint_seq}"
            summary = f"step {api_call_count} milestone"
            try:
                fut = asyncio.run_coroutine_threadsafe(
                    on_checkpoint(
                        checkpoint_id=cp_id,
                        summary=summary[:500],
                        fields={"step": api_call_count},
                        resume_blob="",
                    ),
                    loop,
                )
                fut.add_done_callback(lambda f: f.exception() if f.done() else None)
            except RuntimeError:
                logger.warning(
                    "checkpoint frame dropped for session %s: event loop closed",
                    session_id,
                )

        def _step_callback(api_call_count: int, prev_tools: Any = None) -> None:
            nonlocal last_progress_at
            summary = _format_progress(api_call_count, prev_tools)
            _maybe_emit_step_checkpoint(api_call_count)
            if not summary:
                return

            with progress_lock:
                now = time.time()
                if now - last_progress_at < self._progress_interval_seconds:
                    return
                last_progress_at = now

            try:
                fut = asyncio.run_coroutine_threadsafe(on_progress(summary[:240]), loop)
            except RuntimeError:
                logger.warning(
                    "progress frame dropped for session %s: event loop closed",
                    session_id,
                )
            else:

                def _log_progress_failure(f: asyncio.Future[Any]) -> None:
                    try:
                        f.result()
                    except Exception:
                        logger.exception("progress frame failed for session %s", session_id)

                fut.add_done_callback(_log_progress_failure)

        agent.step_callback = _step_callback

        def _run_agent() -> dict[str, Any]:
            return agent.run_conversation(
                user_message=user_message,
                conversation_history=state.history,
                task_id=session_id,
                persist_user_message=user_message,
            )

        async def _watch_cancel() -> None:
            """Set the ACP session cancel_event and interrupt the agent."""
            try:
                await cancel_event.wait()
            except asyncio.CancelledError:
                return
            try:
                if state.cancel_event is not None:
                    state.cancel_event.set()
                if hasattr(agent, "interrupt"):
                    agent.interrupt()
            except Exception:
                logger.debug("ACP cancel failed for session %s", session_id, exc_info=True)

        watch_task = asyncio.create_task(_watch_cancel())
        result: dict[str, Any] | None = None

        try:
            result = await loop.run_in_executor(None, _run_agent)
        except asyncio.CancelledError:
            # Worker is shutting down or the task itself was cancelled.
            # Ensure the agent notices by re-raising after cleanup in finally.
            raise
        except Exception as exc:
            logger.exception("ACP backend run failed for task %s", run.task_id)
            return TaskCompletePayload(
                status="failed",
                summary="ACP execution error",
                error=f"{type(exc).__name__}: {exc}",
            )
        finally:
            watch_task.cancel()
            try:
                await watch_task
            except asyncio.CancelledError:
                pass
            if cancel_event.is_set():
                # Defensive cancel in case the watch task lost the race.
                try:
                    if state.cancel_event is not None:
                        state.cancel_event.set()
                    if hasattr(agent, "interrupt"):
                        agent.interrupt()
                except Exception:
                    logger.debug(
                        "Defensive ACP cancel failed for session %s",
                        session_id,
                        exc_info=True,
                    )

        # Persist any updated history so salvageable context is not lost.
        # Stateless sessions are discarded by run()'s finally instead.
        if not self._stateless and result and result.get("messages"):
            state.history = result["messages"]
            try:
                manager.save_session(session_id)
            except Exception:
                logger.debug("Failed to save ACP session %s", session_id, exc_info=True)

        cancelled = cancel_event.is_set()
        reason = getattr(cancel_event, "reason", None)

        # Timeout attribution: a Hub timeout cancel must settle as failed.
        if cancelled and reason == CANCEL_REASON_TIMEOUT:
            return self._wrap_responses(
                parsed, run,
                TaskCompletePayload(
                    status="failed",
                    summary="execution timeout",
                    error="execution timeout",
                ),
            )

        if cancelled:
            summary = result.get("final_response") if result else None
            if not summary:
                summary = reason or "cancelled"
            return self._wrap_responses(
                parsed, run,
                TaskCompletePayload(
                    status="cancelled",
                    summary=str(summary)[:500],
                    fields=self._extract_fields(result, run),
                ),
            )

        # Completed / failed path.
        if result and result.get("failed"):
            return self._wrap_responses(
                parsed, run,
                TaskCompletePayload(
                    status="failed",
                    summary=result.get("final_response") or "ACP reported failure",
                    error=result.get("error"),
                    fields=self._extract_fields(result, run),
                ),
            )

        return self._wrap_responses(
            parsed, run,
            TaskCompletePayload(
                status="completed",
                summary=result.get("final_response") if result else "",
                result_text=result.get("final_response") if result else "",
                fields=self._extract_fields(result, run),
                usage=self._extract_usage(result),
            ),
        )

    def _wrap_responses(
        self, parsed: Any, run: TaskRunPayload, payload: TaskCompletePayload
    ) -> TaskCompletePayload:
        """Turn a terminal payload into a Responses ``object: response`` JSON.

        No-op when no envelope was present (legacy goal/planner path). The
        original payload's status, usage and error are preserved; only
        ``result_text`` is replaced with the serialized Response object
        (trimmed to the envelope's byte budget).
        """
        if parsed is None or not getattr(parsed, "present", False):
            return payload
        status = payload.status
        resp_status = (
            "completed" if status == "completed"
            else "failed" if status == "failed"
            else "cancelled"
        )
        text = payload.result_text or payload.summary or ""
        tools = None
        request_echo = getattr(parsed, "request_echo", None)
        if isinstance(request_echo, dict):
            tools = request_echo.get("tools")
        obj = build_response_object(
            getattr(parsed, "response_id", "") or run.task_id,
            getattr(parsed, "model", "") or run.model or "",
            resp_status,
            text,
            task_id=run.task_id,
            instructions=getattr(parsed, "instructions", ""),
            tools=tools,
            usage=payload.usage if isinstance(payload.usage, dict) else None,
            error=payload.error if status != "completed" else None,
        )
        payload.result_text = serialize_response(obj, getattr(parsed, "max_result_bytes", 262144))
        # Keep a short plain-text summary for logs/metrics; the wire body
        # is the Response JSON in result_text.
        if not payload.summary:
            payload.summary = text[:500]
        return payload

    def _resolve_sub_agent_options(self, run: TaskRunPayload) -> SubAgentRuntimeOptions:
        """Merge sidecar defaults with per-task ``params.sub_agent_options``."""
        base = self._sub_agent_options or default_sidecar_options(stateless=self._stateless)
        params = run.params if isinstance(run.params, dict) else {}
        raw = runtime_options_from_params(params)
        if raw is None:
            return base.normalized()
        return parse_sub_agent_options(
            {
                "progress_mode": raw.get("progress_mode", base.progress_mode),
                "checkpoint_every_steps": raw.get(
                    "checkpoint_every_steps", base.checkpoint_every_steps
                ),
                "report_progress_interval_s": raw.get(
                    "report_progress_interval_s", base.report_progress_interval_s
                ),
            }
        )

    async def _resolve_model_binding(
        self, run: TaskRunPayload
    ) -> ModelBinding | TaskCompletePayload | None:
        """Resolve the task's model binding against the local runtime.

        Returns ``None`` for unbound tasks (default model path, no local
        runtime interaction), the :class:`ModelBinding` on success, or a
        fail-fast terminal payload carrying ``model_unavailable``.
        """
        model = (run.model or "").strip()
        if not model:
            return None
        if self._local_runtime is None:
            self._local_runtime = LocalRuntimeResolver.from_env()
        try:
            return await self._local_runtime.resolve(model)
        except ModelUnavailableError as exc:
            msg = str(exc)
            logger.info("task %s model binding failed fast: %s", run.task_id, msg)
            return TaskCompletePayload(
                status="failed",
                summary=msg,
                error=msg,
                error_code=ERROR_MODEL_UNAVAILABLE,
            )

    def _create_default_manager(self) -> Any:
        """Create the session manager matching the configured mode."""
        if self._stateless:
            from extend.sub_agent.model_sessions import (
                BoundModelStatelessSessionManager,
            )

            return BoundModelStatelessSessionManager(
                state_root=self._state_root,
                toolsets=self._stateless_toolsets,
                sandbox=self._sandbox,
                sandbox_image=self._sandbox_image,
            )
        from extend.sub_agent.model_sessions import BoundModelSessionManager

        return BoundModelSessionManager()

    @staticmethod
    def _discard_session(manager: Any, session_id: str, workdir: str | None) -> None:
        """Best-effort removal of a stateless session and its workdir."""
        remove = getattr(manager, "remove_session", None)
        if callable(remove):
            try:
                remove(session_id)
            except Exception:
                logger.debug("Failed to discard ACP session %s", session_id, exc_info=True)
        if workdir:
            shutil.rmtree(workdir, ignore_errors=True)

    @staticmethod
    def _extract_usage(result: dict[str, Any] | None) -> dict[str, Any] | None:
        """Build a sub-agent usage dict from token fields in the agent result."""
        if not result:
            return None
        keys = (
            "prompt_tokens",
            "completion_tokens",
            "total_tokens",
            "reasoning_tokens",
            "cache_read_tokens",
        )
        usage = {k: result[k] for k in keys if result.get(k) is not None}
        return usage if usage else None

    @staticmethod
    def _extract_fields(
        result: dict[str, Any] | None, run: TaskRunPayload
    ) -> dict[str, Any] | None:
        """Build sub-agent result fields from agent result metadata."""
        if not result:
            return None
        fields: dict[str, Any] = {}
        if "api_calls" in result:
            fields["api_calls"] = result["api_calls"]
        if "session_id" in result:
            fields["acp_session_id"] = result["session_id"]
        if "interrupted" in result:
            fields["interrupted"] = result["interrupted"]
        if run.params:
            fields["params"] = run.params
        return fields if fields else None


def _resume_goal(run: TaskRunPayload) -> str:
    """Build the user message, injecting L1 summary when resuming."""
    goal = run.goal or ""
    params = run.params if isinstance(run.params, dict) else {}
    resume_summary = str(params.get("resume_summary") or "").strip()
    if not run.resume_from_checkpoint:
        return _with_sub_agent_identity(run, goal)
    if isinstance(run.resume_blob, bytes):
        blob_text = run.resume_blob.decode("utf-8", errors="replace").strip()
    elif isinstance(run.resume_blob, str):
        blob_text = run.resume_blob.strip()
    else:
        blob_text = resume_summary
    if blob_text:
        return _with_sub_agent_identity(
            run,
            f"[Resuming from checkpoint {run.resume_from_checkpoint}]\n{blob_text}\n{goal}",
        )
    return _with_sub_agent_identity(
        run, f"[Resuming from checkpoint {run.resume_from_checkpoint}]\n{goal}"
    )


def _with_sub_agent_identity(run: TaskRunPayload, message: str) -> str:
    """Prefix the agent message with sub-agent routing identity when present."""
    parts = [f"task_id={run.task_id}"]
    if run.hub_id:
        parts.append(f"hub_id={run.hub_id}")
    if run.master_session_id:
        parts.append(f"master_session_id={run.master_session_id}")
    header = f"[sub-agent {' '.join(parts)}]"
    return f"{SUB_AGENT_EXECUTOR_PREAMBLE}{header}\n{message}"
