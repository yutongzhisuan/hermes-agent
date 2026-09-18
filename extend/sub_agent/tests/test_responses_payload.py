"""Tests for extend.sub_agent.responses_payload.

Behavior contract (see
docs/superpowers/specs/2026-08-31-responses-api-compat-design.md and
plans/2026-08-31-responses-api-compat.md):
- missing envelope -> present=False (caller falls back to goal)
- present but malformed -> present=True, error set
- request.model vs bound model mismatch is NOT a failure
- toolsets are NOT derived from envelope request.tools
- serialize_response always yields json.loads-able output <= max_bytes
- created_at is always present
"""

from __future__ import annotations

import json

import pytest

from extend.sub_agent import responses_payload as rp


def _envelope(
    *,
    response_id="resp_abc",
    model="qwen38-27b-fp4",
    input="hello",
    instructions="",
    tools=None,
    max_result_bytes=rp.DEFAULT_MAX_RESULT_BYTES,
):
    request = {
        "model": model,
        "input": input,
        "instructions": instructions,
        "tools": tools or [],
        "max_output_tokens": 0,
        "text": None,
        "metadata": {},
    }
    return {
        "protocol": rp.PROTOCOL,
        "response_id": response_id,
        "request": request,
        "limits": {"max_result_bytes": max_result_bytes},
    }


def _params(envelope=None, **extra):
    if envelope is None:
        return dict(extra)
    return {"responses.v1": json.dumps(envelope), **extra}


def test_missing_envelope_present_false():
    parsed = rp.parse_responses_envelope({"other": "1"}, "do goal", "bound-model")
    assert parsed.present is False
    assert parsed.ok is True


def test_envelope_round_trip_user_message_from_string_input():
    env = _envelope(input="what is 2+2", instructions="be brief")
    parsed = rp.parse_responses_envelope(_params(env), "fallback goal", "bound")
    assert parsed.present is True
    assert parsed.ok is True
    assert parsed.response_id == "resp_abc"
    assert parsed.model == "qwen38-27b-fp4"
    assert parsed.user_message == "be brief\n\nwhat is 2+2"


def test_model_mismatch_not_a_failure():
    env = _envelope(model="alias-name")
    parsed = rp.parse_responses_envelope(_params(env), "goal", "resolved-display-name")
    assert parsed.ok is True
    # echo keeps the client model
    assert parsed.model == "alias-name"


def test_empty_input_falls_back_to_goal():
    env = _envelope(input="")
    parsed = rp.parse_responses_envelope(_params(env), "the goal text", "m")
    assert parsed.user_message == "the goal text"


def test_input_items_array_flattens_single_role():
    env = _envelope(
        input=[
            {"role": "user", "content": [{"type": "input_text", "text": "first"}]},
            {"role": "user", "content": "second"},
        ]
    )
    parsed = rp.parse_responses_envelope(_params(env), "goal", "m")
    assert parsed.user_message == "first\n\nsecond"


def test_input_items_array_multi_role_transcript():
    env = _envelope(
        input=[
            {"role": "system", "content": "sys"},
            {"role": "user", "content": "q"},
        ]
    )
    parsed = rp.parse_responses_envelope(_params(env), "goal", "m")
    assert "system: sys" in parsed.user_message
    assert "user: q" in parsed.user_message


def test_malformed_envelope_json_sets_error():
    params = {"responses.v1": "{not json"}
    parsed = rp.parse_responses_envelope(params, "goal", "m")
    assert parsed.present is True
    assert parsed.error == rp.ERROR_INVALID_ENVELOPE
    assert not parsed.ok


def test_envelope_missing_request_field_is_invalid():
    params = {"responses.v1": json.dumps({"protocol": rp.PROTOCOL})}
    parsed = rp.parse_responses_envelope(params, "goal", "m")
    assert parsed.present is True
    assert parsed.error == rp.ERROR_INVALID_ENVELOPE


def test_max_result_bytes_clamped_to_outer_cap():
    env = _envelope(max_result_bytes=999999)
    parsed = rp.parse_responses_envelope(
        _params(env), "goal", "m", max_result_bytes=1024
    )
    assert parsed.max_result_bytes == 1024


def test_build_response_object_has_required_fields():
    obj = rp.build_response_object(
        "resp_abc",
        "qwen38-27b-fp4",
        "completed",
        "4",
        task_id="resp_abc",
    )
    assert obj["object"] == "response"
    assert obj["id"] == "resp_abc"
    assert obj["status"] == "completed"
    assert obj["store"] is False
    assert obj["previous_response_id"] is None
    assert isinstance(obj["created_at"], int)
    assert obj["output"][0]["type"] == "message"
    assert obj["output"][0]["content"][0]["text"] == "4"
    assert obj["metadata"]["truncated"] is False


def test_build_response_failed_uses_error_text_when_no_output():
    obj = rp.build_response_object(
        "resp_x", "m", "failed", "", error="model unavailable"
    )
    assert obj["status"] == "failed"
    assert obj["error"] == "model unavailable"
    assert obj["output"][0]["content"][0]["text"] == "model unavailable"


def test_serialize_response_fits_without_trimming():
    obj = rp.build_response_object("resp_1", "m", "completed", "short")
    s = rp.serialize_response(obj, 8192)
    assert json.loads(s)["metadata"]["truncated"] is False
    assert len(s.encode("utf-8")) <= 8192


def test_serialize_response_trims_and_stays_valid_json():
    big = "x" * 10000
    obj = rp.build_response_object("resp_1", "m", "completed", big)
    s = rp.serialize_response(obj, 2048)
    decoded = json.loads(s)  # must parse
    assert decoded["metadata"]["truncated"] is True
    assert len(s.encode("utf-8")) <= 2048
    text = decoded["output"][0]["content"][0]["text"]
    assert len(text) < len(big)


def test_serialize_response_unicode_bytes_counted_correctly():
    # 2-byte chars in UTF-8: ensure byte budget, not char budget, is enforced
    big = "é" * 4000  # 8000 bytes of text
    obj = rp.build_response_object("resp_1", "m", "completed", big)
    s = rp.serialize_response(obj, 2048)
    assert len(s.encode("utf-8")) <= 2048
    json.loads(s)


def test_usage_mapping_prompt_completion_to_input_output():
    obj = rp.build_response_object(
        "resp_1",
        "m",
        "completed",
        "ok",
        usage={"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
    )
    u = obj["usage"]
    assert u["input_tokens"] == 10
    assert u["output_tokens"] == 5
    assert u["total_tokens"] == 15


def test_envelope_tools_not_used_for_toolsets():
    # parse_responses_envelope must not surface toolsets at all — the
    # caller (AcpTaskBackend) uses run.toolsets, never envelope.tools.
    env = _envelope(tools=[{"type": "web_search"}])
    parsed = rp.parse_responses_envelope(_params(env), "goal", "m")
    assert not hasattr(parsed, "toolsets")
    # request_echo still carries tools for Response echo if needed
    assert parsed.request_echo.get("tools") == [{"type": "web_search"}]


# ---------------------------------------------------------------------------
# Structured replay (v4: input items -> messages, DeepSeek-style plaintext)
# ---------------------------------------------------------------------------


def test_messages_string_input_with_instructions():
    env = _envelope(input="what is 2+2", instructions="be brief")
    parsed = rp.parse_responses_envelope(_params(env), "goal", "m")
    assert parsed.messages == [
        {"role": "system", "content": "be brief"},
        {"role": "user", "content": "what is 2+2"},
    ]


def test_messages_string_input_without_instructions():
    env = _envelope(input="hello", instructions="")
    parsed = rp.parse_responses_envelope(_params(env), "goal", "m")
    assert parsed.messages == [{"role": "user", "content": "hello"}]


def test_messages_multi_role_preserved_not_transcript():
    env = _envelope(
        input=[
            {"role": "system", "content": "sys"},
            {"role": "user", "content": "q"},
            {"role": "assistant", "content": "a"},
        ]
    )
    parsed = rp.parse_responses_envelope(_params(env), "goal", "m")
    assert parsed.messages == [
        {"role": "system", "content": "sys"},
        {"role": "user", "content": "q"},
        {"role": "assistant", "content": "a"},
    ]


def test_messages_developer_role_maps_to_system():
    env = _envelope(input=[{"role": "developer", "content": "dev rules"}])
    parsed = rp.parse_responses_envelope(_params(env), "goal", "m")
    assert parsed.messages == [{"role": "system", "content": "dev rules"}]


def test_messages_content_parts_joined():
    env = _envelope(
        input=[
            {
                "role": "user",
                "content": [
                    {"type": "input_text", "text": "part1"},
                    {"type": "input_text", "text": "part2"},
                ],
            }
        ]
    )
    parsed = rp.parse_responses_envelope(_params(env), "goal", "m")
    assert parsed.messages == [{"role": "user", "content": "part1\npart2"}]


def test_messages_reasoning_merges_into_adjacent_assistant():
    env = _envelope(
        input=[
            {"role": "user", "content": "q"},
            {"role": "assistant", "content": "a"},
            {
                "type": "reasoning",
                "content": [{"type": "reasoning_text", "text": "thinking..."}],
            },
        ]
    )
    parsed = rp.parse_responses_envelope(_params(env), "goal", "m")
    assert parsed.messages == [
        {"role": "user", "content": "q"},
        {"role": "assistant", "content": "a\nthinking..."},
    ]


def test_messages_reasoning_without_adjacent_assistant_dropped():
    env = _envelope(
        input=[
            {"type": "reasoning", "content": [{"type": "reasoning_text", "text": "x"}]},
            {"role": "user", "content": "q"},
        ]
    )
    parsed = rp.parse_responses_envelope(_params(env), "goal", "m")
    assert parsed.messages == [{"role": "user", "content": "q"}]


def test_messages_function_call_becomes_assistant_tool_calls():
    env = _envelope(
        input=[
            {"role": "user", "content": "q"},
            {
                "type": "function_call",
                "call_id": "call_1",
                "name": "get_weather",
                "arguments": '{"city": "SH"}',
            },
        ]
    )
    parsed = rp.parse_responses_envelope(_params(env), "goal", "m")
    assert parsed.messages == [
        {"role": "user", "content": "q"},
        {
            "role": "assistant",
            "content": None,
            "tool_calls": [
                {
                    "id": "call_1",
                    "type": "function",
                    "function": {"name": "get_weather", "arguments": '{"city": "SH"}'},
                }
            ],
        },
    ]


def test_messages_function_call_output_becomes_tool_result():
    env = _envelope(
        input=[
            {"role": "user", "content": "q"},
            {
                "type": "function_call",
                "call_id": "call_1",
                "name": "get_weather",
                "arguments": "{}",
            },
            {"type": "function_call_output", "call_id": "call_1", "output": "sunny"},
        ]
    )
    parsed = rp.parse_responses_envelope(_params(env), "goal", "m")
    assert parsed.messages[-1] == {
        "role": "tool",
        "tool_call_id": "call_1",
        "content": "sunny",
    }


def test_messages_missing_envelope_empty():
    parsed = rp.parse_responses_envelope({"other": "1"}, "goal", "m")
    assert parsed.messages == []


def test_split_replay_messages_trailing_user():
    history, user = rp.split_replay_messages([
        {"role": "system", "content": "s"},
        {"role": "user", "content": "q1"},
        {"role": "assistant", "content": "a1"},
        {"role": "user", "content": "q2"},
    ])
    assert user == "q2"
    assert history == [
        {"role": "system", "content": "s"},
        {"role": "user", "content": "q1"},
        {"role": "assistant", "content": "a1"},
    ]


def test_split_replay_messages_no_trailing_user():
    messages = [
        {"role": "user", "content": "q"},
        {"role": "tool", "tool_call_id": "c", "content": "r"},
    ]
    history, user = rp.split_replay_messages(messages)
    assert user == ""
    assert history == messages


# ---------------------------------------------------------------------------
# Turn output items (v4: replayable context items in Response.output)
# ---------------------------------------------------------------------------


def test_turn_output_items_slices_after_last_user():
    messages = [
        {"role": "user", "content": "q1"},
        {"role": "assistant", "content": "a1"},
        {"role": "user", "content": "q2"},
        {"role": "assistant", "content": "a2"},
    ]
    items = rp.turn_output_items(messages)
    assert [i["type"] for i in items] == ["message"]
    assert items[0]["content"][0]["text"] == "a2"


def test_turn_output_items_tool_round_trip():
    messages = [
        {"role": "user", "content": "q"},
        {
            "role": "assistant",
            "content": None,
            "tool_calls": [
                {
                    "id": "call_1",
                    "type": "function",
                    "function": {"name": "web", "arguments": "{}"},
                }
            ],
        },
        {"role": "tool", "tool_call_id": "call_1", "content": "result"},
        {"role": "assistant", "content": "final"},
    ]
    items = rp.turn_output_items(messages)
    assert [i["type"] for i in items] == [
        "function_call",
        "function_call_output",
        "message",
    ]
    fc = items[0]
    assert fc["call_id"] == "call_1"
    assert fc["name"] == "web"
    assert fc["id"].startswith("fc_")
    out = items[1]
    assert out["call_id"] == "call_1"
    assert out["output"] == "result"
    assert items[2]["content"][0]["text"] == "final"


def test_turn_output_items_reasoning_plaintext_first():
    messages = [
        {"role": "user", "content": "q"},
        {
            "role": "assistant",
            "content": "answer",
            "reasoning_content": "thinking about it",
        },
    ]
    items = rp.turn_output_items(messages)
    assert [i["type"] for i in items] == ["reasoning", "message"]
    assert items[0]["id"].startswith("rs_")
    assert items[0]["content"][0]["text"] == "thinking about it"


def test_turn_output_items_empty():
    assert rp.turn_output_items([]) == []


def test_turn_output_items_no_user_uses_all():
    messages = [
        {"role": "assistant", "content": "a"},
    ]
    items = rp.turn_output_items(messages)
    assert [i["type"] for i in items] == ["message"]


def test_build_response_object_with_items_no_duplicate_message():
    items = [
        {
            "id": "fc_1",
            "type": "function_call",
            "call_id": "c1",
            "name": "web",
            "arguments": "{}",
        },
        {
            "id": "msg_1",
            "type": "message",
            "role": "assistant",
            "status": "completed",
            "content": [
                {
                    "type": "output_text",
                    "text": "final",
                    "annotations": [],
                    "logprobs": [],
                }
            ],
        },
    ]
    obj = rp.build_response_object("resp_1", "m", "completed", "final", items=items)
    assert obj["output"] == items
    assert len([i for i in obj["output"] if i["type"] == "message"]) == 1


def test_build_response_object_items_without_message_appends_fallback():
    items = [
        {
            "id": "fc_1",
            "type": "function_call",
            "call_id": "c1",
            "name": "web",
            "arguments": "{}",
        },
    ]
    obj = rp.build_response_object("resp_1", "m", "completed", "final", items=items)
    assert obj["output"][0]["type"] == "function_call"
    assert obj["output"][-1]["type"] == "message"
    assert obj["output"][-1]["content"][0]["text"] == "final"
