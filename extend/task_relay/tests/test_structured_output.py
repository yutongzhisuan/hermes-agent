"""Tests for structured output enrichment."""

from __future__ import annotations

from extend.task_relay.worker.structured_output import enrich_structured_output
from extend.task_relay.worker.task_executor import TaskCompletePayload, TaskRunPayload


def test_enrich_structured_output_extracts_json_fence():
    run = TaskRunPayload(
        task_id="t1",
        attempt=1,
        goal="g",
        params={"structured_output": {"type": "object"}},
        context=None,
        toolsets=[],
        timeout_seconds=60,
        first_progress_seconds=None,
        trace_context=None,
        resume_from_checkpoint=None,
    )
    payload = TaskCompletePayload(
        status="completed",
        summary='Done\n```json\n{"answer": 42}\n```',
    )
    enriched = enrich_structured_output(payload, run)
    assert enriched.fields is not None
    assert enriched.fields["schema_version"] == 1
    assert enriched.fields["structured"] == {"answer": 42}


def test_enrich_structured_output_noop_without_spec():
    run = TaskRunPayload(
        task_id="t1",
        attempt=1,
        goal="g",
        params=None,
        context=None,
        toolsets=[],
        timeout_seconds=60,
        first_progress_seconds=None,
        trace_context=None,
        resume_from_checkpoint=None,
    )
    payload = TaskCompletePayload(status="completed", summary="ok")
    assert enrich_structured_output(payload, run) is payload
