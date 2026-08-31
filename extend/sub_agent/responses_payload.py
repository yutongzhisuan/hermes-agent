"""OpenAI Responses API payload handling for the stateless sub-agent.

Parses the ``params["responses.v1"]`` envelope (an OpenAI Responses
request subset — see
``docs/superpowers/specs/2026-08-31-responses-api-compat-design.md``)
and builds the terminal ``object: "response"`` JSON that becomes
``TaskResult.result_text``.

Trust model (matches the implementation plan):
- toolsets are NOT taken from ``request.tools``; the caller supplies
  ``run.toolsets`` as the single source of truth (the gateway already
  mapped them). ``request.tools`` is echo-only.
- ``request.model`` is echo-only; the execution model binding comes from
  ``run.model`` (``params["model"]``). No equality check is performed.
- ``store`` is always ``false`` in the response. ``previous_response_id``
  is rejected at the HTTP layer and never reaches here.
- ``max_output_tokens`` is not enforced in P0 (the agent loop has no
  token cap hook); it rides in the envelope for P2.
"""

from __future__ import annotations

import copy
import json
import time
import uuid
from dataclasses import dataclass, field
from typing import Any

ENVELOPE_KEY = "responses.v1"
PROTOCOL = "responses/v1"
DEFAULT_MAX_RESULT_BYTES = 262144  # 256 KiB

#: Error code surfaced to the Hub when a present envelope is malformed.
ERROR_INVALID_ENVELOPE = "invalid_responses_payload"


@dataclass
class ParsedResponsesRequest:
    """Result of envelope parsing.

    ``present=False`` means there was no ``responses.v1`` key: the caller
    must fall back to the legacy ``goal`` text path. ``present=True`` with
    ``error`` set means the envelope was present but malformed: the caller
    fails the task with that code.
    """

    present: bool
    response_id: str = ""
    model: str = ""
    instructions: str = ""
    user_message: str = ""
    max_result_bytes: int = DEFAULT_MAX_RESULT_BYTES
    request_echo: dict = field(default_factory=dict)
    error: str | None = None

    @property
    def ok(self) -> bool:
        return self.error is None


def parse_responses_envelope(
    params: Any,
    goal: str,
    bound_model: str,
    max_result_bytes: int | None = None,
) -> ParsedResponsesRequest:
    """Parse the ``responses.v1`` envelope from task params.

    Args:
        params: the task params dict (``run.params``).
        goal: the task goal text, used as the user message when the
            envelope's ``input`` is empty.
        bound_model: the execution model (``run.model``); used only as a
            fallback for the echoed ``model`` when the envelope omits it.
        max_result_bytes: an optional outer cap on the result budget,
            clamped against the envelope's ``limits.max_result_bytes``.

    The ``request.model`` vs ``bound_model`` comparison is intentionally
    NOT performed: the client may send an alias while the binding is the
    gateway-resolved display name.
    """
    if not isinstance(params, dict) or ENVELOPE_KEY not in params:
        return ParsedResponsesRequest(present=False)

    raw = params.get(ENVELOPE_KEY)
    if not isinstance(raw, str):
        return ParsedResponsesRequest(present=True, error=ERROR_INVALID_ENVELOPE)
    try:
        env = json.loads(raw)
    except (json.JSONDecodeError, TypeError):
        return ParsedResponsesRequest(present=True, error=ERROR_INVALID_ENVELOPE)
    if not isinstance(env, dict):
        return ParsedResponsesRequest(present=True, error=ERROR_INVALID_ENVELOPE)

    request = env.get("request")
    if not isinstance(request, dict):
        return ParsedResponsesRequest(present=True, error=ERROR_INVALID_ENVELOPE)

    response_id = str(env.get("response_id") or "").strip()
    req_model = str(request.get("model") or "").strip()
    instructions = _normalize_instructions(request.get("instructions"))
    user_message = _normalize_user_message(request.get("input"), instructions, goal)

    limits = env.get("limits") or {}
    if not isinstance(limits, dict):
        limits = {}
    cap = _coerce_int(limits.get("max_result_bytes"), DEFAULT_MAX_RESULT_BYTES)
    if max_result_bytes and max_result_bytes > 0:
        cap = min(cap, max_result_bytes)

    echo_model = req_model or str(bound_model or "").strip()

    return ParsedResponsesRequest(
        present=True,
        response_id=response_id,
        model=echo_model,
        instructions=instructions,
        user_message=user_message,
        max_result_bytes=max(1, cap),
        request_echo=request,
    )


def build_response_object(
    response_id: str,
    model: str,
    status: str,
    output_text: str,
    *,
    task_id: str = "",
    instructions: str = "",
    tools: list | None = None,
    usage: dict | None = None,
    error: str | None = None,
    created_at: int | None = None,
) -> dict:
    """Build an OpenAI Responses ``object: "response"`` dict.

    ``status`` is one of ``completed`` / ``failed`` / ``cancelled`` /
    ``incomplete``. The output always carries at least one assistant
    message item whose ``output_text`` is ``output_text`` (or ``error``
    when the task failed without producing text), so the shape stays
    SDK-parseable across terminal states.
    """
    if created_at is None:
        created_at = int(time.time())

    rid = response_id.strip() or _new_id("resp")
    text = output_text
    if not text and error:
        text = error
    if not text and status != "completed":
        text = status

    message_id = _new_id("msg")
    output = [
        {
            "id": message_id,
            "type": "message",
            "role": "assistant",
            "status": status,
            "content": [
                {
                    "type": "output_text",
                    "text": text,
                    "annotations": [],
                    "logprobs": [],
                }
            ],
        }
    ]

    return {
        "id": rid,
        "object": "response",
        "created_at": created_at,
        "status": status,
        "model": model,
        "output": output,
        "store": False,
        "previous_response_id": None,
        "parallel_tool_calls": False,
        "instructions": instructions,
        "tools": list(tools) if tools is not None else [],
        "tool_choice": "auto",
        "truncation": "disabled",
        "reasoning": {"effort": None, "summary": None},
        "usage": _map_usage(usage),
        "incomplete_details": None,
        "error": error,
        "metadata": {"task_id": task_id, "truncated": False},
    }


def serialize_response(obj: dict, max_bytes: int) -> str:
    """Serialize a Response object, trimming ``output_text`` to fit.

    Returns valid JSON no larger than ``max_bytes`` (UTF-8 byte length)
    whenever feasible. When the object already fits, it is returned as-is
    with ``metadata.truncated=False``. When trimming is required, the
    first message item's ``output_text`` is shortened and
    ``metadata.truncated`` is set to ``True``. The result is always
    ``json.loads``-parseable.
    """
    encoded = _dumps(obj)
    if len(encoded.encode("utf-8")) <= max_bytes:
        return encoded

    trimmed = copy.deepcopy(obj)
    text = _first_output_text(trimmed)
    if text is None:
        # Nothing trimmable to shrink; return the original (still valid
        # JSON, just over the soft budget — caller's clamp is skipped for
        # resp_ tasks, so this stays intact end-to-end).
        return encoded

    lo, hi = 0, len(text)
    best = ""
    while lo <= hi:
        mid = (lo + hi) // 2
        _set_first_output_text(trimmed, text[:mid])
        _set_metadata(trimmed, truncated=True)
        candidate = _dumps(trimmed)
        if len(candidate.encode("utf-8")) <= max_bytes:
            best = candidate
            lo = mid + 1
        else:
            hi = mid - 1

    if best:
        return best

    # Even an empty text overruns (huge envelope echo); emit a minimal
    # valid object so the wire is never broken.
    _set_first_output_text(trimmed, "")
    _set_metadata(trimmed, truncated=True)
    return _dumps(trimmed)


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------


def _new_id(prefix: str) -> str:
    return f"{prefix}_{uuid.uuid4().hex}"


def _dumps(obj: Any) -> str:
    return json.dumps(obj, ensure_ascii=False, separators=(",", ":"))


def _coerce_int(value: Any, default: int) -> int:
    if isinstance(value, bool):
        return default
    if isinstance(value, int):
        return value
    if isinstance(value, float):
        return int(value)
    if isinstance(value, str):
        try:
            return int(value)
        except ValueError:
            return default
    return default


def _normalize_instructions(raw: Any) -> str:
    if raw is None:
        return ""
    if isinstance(raw, str):
        return raw.strip()
    return ""


def _normalize_user_message(raw_input: Any, instructions: str, goal: str) -> str:
    """Normalize OpenAI ``input`` (string or items array) to one user msg.

    A single user string is returned verbatim. A multi-item array is
    flattened to a transcript (``<role>: <text>`` lines) — P0 keeps the
    agent's single ``user_message`` entry; multi-turn history support is
    a later refinement. Empty input falls back to ``goal`` so a misbuilt
    envelope never produces an empty prompt.
    """
    text = _extract_input_text(raw_input)
    if not text.strip():
        text = goal or ""
    prefix = instructions + "\n\n" if instructions else ""
    return prefix + text


def _extract_input_text(raw_input: Any) -> str:
    if raw_input is None:
        return ""
    if isinstance(raw_input, str):
        return raw_input
    if not isinstance(raw_input, list):
        return ""

    lines: list[str] = []
    multi_role = False
    seen_roles: set[str] = set()
    for item in raw_input:
        if not isinstance(item, dict):
            continue
        role = str(item.get("role") or "").strip().lower() or "user"
        seen_roles.add(role)
        content = item.get("content")
        body = _content_text(content)
        if not body.strip():
            continue
        lines.append(body)

    if len(seen_roles) > 1:
        multi_role = True

    if multi_role:
        rendered: list[str] = []
        for item in raw_input:
            if not isinstance(item, dict):
                continue
            role = str(item.get("role") or "").strip().lower() or "user"
            body = _content_text(item.get("content"))
            if not body.strip():
                continue
            rendered.append(f"{role}: {body}")
        return "\n\n".join(rendered)
    return "\n\n".join(lines)


def _content_text(content: Any) -> str:
    if content is None:
        return ""
    if isinstance(content, str):
        return content
    if not isinstance(content, list):
        return ""
    parts: list[str] = []
    for piece in content:
        if isinstance(piece, str):
            parts.append(piece)
            continue
        if not isinstance(piece, dict):
            continue
        ptype = piece.get("type")
        if ptype in ("input_text", "text", "output_text", None):
            text = piece.get("text")
            if isinstance(text, str):
                parts.append(text)
    return "\n".join(p for p in parts if p)


def _map_usage(usage: dict | None) -> dict:
    if not usage:
        return {
            "input_tokens": 0,
            "input_tokens_details": {"cached_tokens": 0},
            "output_tokens": 0,
            "output_tokens_details": {"reasoning_tokens": 0},
            "total_tokens": 0,
        }
    prompt = _coerce_int(usage.get("prompt_tokens"), 0)
    completion = _coerce_int(usage.get("completion_tokens"), 0)
    total = _coerce_int(usage.get("total_tokens"), prompt + completion)
    return {
        "input_tokens": prompt,
        "input_tokens_details": {
            "cached_tokens": _coerce_int(usage.get("cache_read_tokens"), 0),
        },
        "output_tokens": completion,
        "output_tokens_details": {
            "reasoning_tokens": _coerce_int(usage.get("reasoning_tokens"), 0),
        },
        "total_tokens": total,
    }


def _first_output_text(obj: dict) -> str | None:
    for item in obj.get("output") or []:
        if not isinstance(item, dict) or item.get("type") != "message":
            continue
        for part in item.get("content") or []:
            if isinstance(part, dict) and part.get("type") == "output_text":
                text = part.get("text")
                if isinstance(text, str):
                    return text
    return None


def _set_first_output_text(obj: dict, text: str) -> None:
    for item in obj.get("output") or []:
        if not isinstance(item, dict) or item.get("type") != "message":
            continue
        for part in item.get("content") or []:
            if isinstance(part, dict) and part.get("type") == "output_text":
                part["text"] = text
                return


def _set_metadata(obj: dict, *, truncated: bool) -> None:
    meta = obj.get("metadata")
    if not isinstance(meta, dict):
        meta = {}
        obj["metadata"] = meta
    meta["truncated"] = truncated


__all__ = [
    "ENVELOPE_KEY",
    "PROTOCOL",
    "DEFAULT_MAX_RESULT_BYTES",
    "ERROR_INVALID_ENVELOPE",
    "ParsedResponsesRequest",
    "parse_responses_envelope",
    "build_response_object",
    "serialize_response",
]
