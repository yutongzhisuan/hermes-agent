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
    messages: list[dict] = field(default_factory=list)
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
    messages = _normalize_items_to_messages(request.get("input"), instructions)

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
        messages=messages,
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
    items: list | None = None,
) -> dict:
    """Build an OpenAI Responses ``object: "response"`` dict.

    ``status`` is one of ``completed`` / ``failed`` / ``cancelled`` /
    ``incomplete``. ``items`` carries the replayable context items
    (reasoning / function_call / function_call_output / message) produced
    this turn — DeepSeek-style plaintext replay, no encryption. When
    ``items`` is empty/absent, or when it contains no message item, a
    fallback assistant message carrying ``output_text`` (or ``error``)
    keeps the shape SDK-parseable across terminal states.
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
    fallback_message = {
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

    if items:
        output = list(items)
        if not any(i.get("type") == "message" for i in output if isinstance(i, dict)):
            output.append(fallback_message)
    else:
        output = [fallback_message]

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


def split_replay_messages(messages: list[dict]) -> tuple[list[dict], str]:
    """Split structured replay messages into (history, final user message).

    DeepSeek-style stateless replay: the client sends the full transcript
    as input items on every request. The trailing user message drives the
    new agent turn; everything before it becomes conversation history.
    When the transcript does not end with a user message (e.g. it ends
    with a tool result), the caller falls back to the flattened
    ``user_message`` text path and history is the full list.
    """
    if messages and messages[-1].get("role") == "user":
        content = messages[-1].get("content")
        user = content if isinstance(content, str) else _content_text(content)
        return list(messages[:-1]), user
    return list(messages), ""


def turn_output_items(messages: list[dict]) -> list[dict]:
    """Convert this turn's new chat messages to replayable output items.

    The conversation ``messages`` include the replayed history plus the
    new turn; only the messages after the last user message belong to
    this turn. Conversion (DeepSeek-style plaintext, no encryption):

    - assistant ``reasoning_content`` → a ``reasoning`` item (plaintext)
    - assistant ``tool_calls`` → ``function_call`` items
    - assistant text → a ``message`` item (``output_text``)
    - ``tool`` messages → ``function_call_output`` items

    Order follows the message sequence, i.e. chronological generation
    order, so the client can append the items to its next request's
    ``input`` verbatim.
    """
    new_messages = list(messages)
    for i in range(len(messages) - 1, -1, -1):
        if isinstance(messages[i], dict) and messages[i].get("role") == "user":
            new_messages = list(messages[i + 1 :])
            break

    items: list[dict] = []
    for msg in new_messages:
        if not isinstance(msg, dict):
            continue
        role = msg.get("role")

        if role == "assistant":
            reasoning = msg.get("reasoning_content")
            if isinstance(reasoning, str) and reasoning.strip():
                items.append({
                    "id": _new_id("rs"),
                    "type": "reasoning",
                    "content": [{"type": "reasoning_text", "text": reasoning}],
                })
            tool_calls = msg.get("tool_calls") or []
            if isinstance(tool_calls, list):
                for tc in tool_calls:
                    if not isinstance(tc, dict):
                        continue
                    fn = tc.get("function") or {}
                    items.append({
                        "id": _new_id("fc"),
                        "type": "function_call",
                        "call_id": str(tc.get("id") or ""),
                        "name": str(fn.get("name") or ""),
                        "arguments": str(fn.get("arguments") or ""),
                    })
            text = _content_text(msg.get("content"))
            if text.strip():
                items.append({
                    "id": _new_id("msg"),
                    "type": "message",
                    "role": "assistant",
                    "content": [
                        {
                            "type": "output_text",
                            "text": text,
                            "annotations": [],
                            "logprobs": [],
                        }
                    ],
                })
            continue

        if role == "tool":
            items.append({
                "id": _new_id("fc"),
                "type": "function_call_output",
                "call_id": str(msg.get("tool_call_id") or ""),
                "output": _content_text(msg.get("content")),
            })

    return items


def _normalize_items_to_messages(raw_input: Any, instructions: str) -> list[dict]:
    """Normalize OpenAI input items to a chat-style message list.

    DeepSeek-style semantics (see survey doc §6.1):
    - ``instructions`` becomes the first system message.
    - ``message`` items keep their role (``developer`` maps to system);
      content parts (``input_text``/``output_text``/plain strings) join
      with newlines.
    - ``reasoning`` items: plaintext ``content`` merges into the adjacent
      (preceding) assistant message; ``summary``/``encrypted_content``
      unsupported, and a reasoning item with no preceding assistant
      message is dropped.
    - ``function_call`` items become an assistant message with
      ``tool_calls``; ``function_call_output`` items become tool result
      messages.
    """
    messages: list[dict] = []
    if instructions:
        messages.append({"role": "system", "content": instructions})

    if isinstance(raw_input, str):
        if raw_input:
            messages.append({"role": "user", "content": raw_input})
        return messages
    if not isinstance(raw_input, list):
        return messages

    for item in raw_input:
        if not isinstance(item, dict):
            continue
        itype = item.get("type")

        if itype == "function_call":
            call_id = str(item.get("call_id") or item.get("id") or "")
            messages.append({
                "role": "assistant",
                "content": None,
                "tool_calls": [
                    {
                        "id": call_id,
                        "type": "function",
                        "function": {
                            "name": str(item.get("name") or ""),
                            "arguments": str(item.get("arguments") or ""),
                        },
                    }
                ],
            })
            continue

        if itype == "function_call_output":
            messages.append({
                "role": "tool",
                "tool_call_id": str(item.get("call_id") or ""),
                "content": _content_text(item.get("output")),
            })
            continue

        if itype == "reasoning":
            text = _reasoning_content_text(item.get("content"))
            if text and messages and messages[-1].get("role") == "assistant":
                prev = messages[-1].get("content")
                messages[-1]["content"] = (prev + "\n" + text) if prev else text
            continue

        role = str(item.get("role") or "").strip().lower()
        if role not in ("user", "assistant", "system", "developer"):
            continue
        text = _content_text(item.get("content"))
        if not text.strip():
            continue
        if role == "developer":
            role = "system"
        messages.append({"role": role, "content": text})

    return messages


def _reasoning_content_text(content: Any) -> str:
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
        if isinstance(piece, dict):
            text = piece.get("text")
            if isinstance(text, str) and text:
                parts.append(text)
    return "\n".join(parts)


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
    "split_replay_messages",
    "turn_output_items",
]
