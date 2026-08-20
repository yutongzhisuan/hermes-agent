"""Tool-handler tests: dispatch/watch/result/list/cancel + hard constraints.

Covers the spec §12.1 must-haves:
  * delegation isolation (delegate_task children are refused);
  * blocking watch polls ``tools.interrupt.is_interrupted()`` and exits clean;
  * ledger mirroring (status/cursor) and thread-safe concurrent writes;
  * PROGRESS throttling (only the latest progress summary per task survives);
  * SSE cursor_out_of_range guidance toward gateway_get_task_result.
"""

from __future__ import annotations

import base64
import gzip
import json
import threading
import time

from agent.delegation_context import delegated_child_context

from extend.master_planner import tools as mp_tools
from extend.master_planner.ledger import Ledger
from extend.master_planner.tools import (
    CONTEXT_INLINE_MAX_BYTES,
    _encode_context,
    gateway_cancel_task,
    gateway_dispatch_batch,
    gateway_dispatch_task,
    gateway_get_task_result,
    gateway_list_tasks,
    gateway_list_workers,
    gateway_watch_task,
)

ALL_HANDLERS = [
    (gateway_dispatch_task, {"goal": "x"}),
    (gateway_dispatch_batch, {"specs": [{"goal": "x"}]}),
    (gateway_watch_task, {"task_id": "t", "wait_seconds": 1}),
    (gateway_get_task_result, {"task_id": "t"}),
    (gateway_list_tasks, {}),
    (gateway_list_workers, {}),
    (gateway_cancel_task, {"task_id": "t"}),
]


def _parse(raw: str) -> dict:
    return json.loads(raw)


# ---------------------------------------------------------------------------
# dispatch
# ---------------------------------------------------------------------------


def test_dispatch_task_records_ledger(gateway_env):
    out = _parse(gateway_dispatch_task({"goal": "research A"}))
    assert out["task_id"].startswith(out["run_id"] + "-")
    # Server status is a TaskStatus enum name; the tool surfaces the short name.
    assert out["status"] == "pending"
    row = mp_tools._get_ledger().get(out["task_id"])
    assert row is not None
    assert row["goal"] == "research A"
    assert row["status"] == "submitted"


def test_dispatch_task_requires_goal(gateway_env):
    out = _parse(gateway_dispatch_task({}))
    assert out["error"] == "invalid_args"


def test_dispatch_task_seq_increments(gateway_env):
    first = _parse(gateway_dispatch_task({"goal": "a"}))
    second = _parse(gateway_dispatch_task({"goal": "b"}))
    assert first["task_id"] != second["task_id"]
    seq1 = int(first["task_id"].rsplit("-", 1)[1])
    seq2 = int(second["task_id"].rsplit("-", 1)[1])
    assert seq2 == seq1 + 1


def test_dispatch_batch_records_all(gateway_env):
    out = _parse(
        gateway_dispatch_batch({
            "specs": [{"goal": "A"}, {"goal": "B"}, {"goal": "C"}],
            "join_policy": "all",
        })
    )
    assert out["count"] == 3
    assert len(out["task_ids"]) == 3
    ledger = mp_tools._get_ledger()
    for tid in out["task_ids"]:
        row = ledger.get(tid)
        assert row is not None
        assert row["batch_id"] == out["batch_id"]


def test_dispatch_batch_rejects_bad_specs(gateway_env):
    assert _parse(gateway_dispatch_batch({"specs": []}))["error"] == "invalid_args"
    out = _parse(gateway_dispatch_batch({"specs": [{"goal": "ok"}, {"no_goal": 1}]}))
    assert out["error"] == "invalid_args"


# ---------------------------------------------------------------------------
# context encoding (48 KiB gzip threshold)
# ---------------------------------------------------------------------------


def test_encode_context_inline_below_threshold():
    ctx = _encode_context({"notes": "small"})
    assert set(ctx) == {"inline"}
    assert json.loads(ctx["inline"]) == {"notes": "small"}


def test_encode_context_gzip_above_threshold():
    big = "x" * (CONTEXT_INLINE_MAX_BYTES + 100)
    ctx = _encode_context(big)
    assert set(ctx) == {"inline_gzip"}
    assert gzip.decompress(base64.b64decode(ctx["inline_gzip"])).decode("utf-8") == big


def test_encode_context_boundary_exact():
    ctx = _encode_context("y" * CONTEXT_INLINE_MAX_BYTES)
    assert set(ctx) == {"inline"}


# ---------------------------------------------------------------------------
# watch
# ---------------------------------------------------------------------------


def test_watch_terminal_throttles_progress(gateway_env):
    dispatched = _parse(gateway_dispatch_task({"goal": "job"}))
    tid = dispatched["task_id"]
    out = _parse(gateway_watch_task({"task_id": tid, "wait_seconds": 10}))
    assert out["reason"] == "terminal"
    # Two progress frames arrived; only the latest summary survives.
    assert out["progress"] == {tid: "step 2"}
    assert out["terminal"] == [
        {"task_id": tid, "status": "completed", "summary": "done"}
    ]
    assert out["cursor"] == "3"
    # Ledger mirrored the terminal status and the resume cursor.
    row = mp_tools._get_ledger().get(tid)
    assert row["status"] == "completed"
    assert row["cursor_event_id"] == "3"


def test_watch_cursor_out_of_range_guides_to_get_result(gateway_env):
    out = _parse(
        gateway_watch_task({
            "task_id": "t-x",
            "wait_seconds": 5,
            "since_event_id": "expired",
        })
    )
    assert out["error"]["code"] == "cursor_out_of_range"
    assert "gateway_get_task_result" in out["message"]


def test_watch_batch_persists_cursor_on_watched_tasks(gateway_env):
    batch = _parse(gateway_dispatch_batch({"specs": [{"goal": "A"}, {"goal": "B"}]}))
    out = _parse(
        gateway_watch_task({"batch_id": batch["batch_id"], "wait_seconds": 10})
    )
    assert out["reason"] == "terminal"
    assert out["cursor"] == "3"
    ledger = mp_tools._get_ledger()
    for tid in batch["task_ids"]:
        assert ledger.get(tid)["cursor_event_id"] == "3"


def test_watch_interrupts_cleanly(gateway_env, fast_client):
    main_ident = threading.current_thread().ident
    from tools.interrupt import set_interrupt

    def interrupter():
        time.sleep(0.3)
        set_interrupt(True, thread_id=main_ident)

    t = threading.Thread(target=interrupter, daemon=True)
    started = time.monotonic()
    try:
        t.start()
        out = _parse(gateway_watch_task({"task_id": "block-me", "wait_seconds": 20}))
    finally:
        set_interrupt(False, thread_id=main_ident)
        t.join(timeout=5)
    elapsed = time.monotonic() - started
    assert out["reason"] == "interrupted"
    assert out["interrupted"] is True
    assert "中断" in out["message"]
    assert elapsed < 10  # clean exit, nowhere near wait_seconds=20


def test_watch_quiet_window_reports_no_events(gateway_env, fast_client):
    out = _parse(gateway_watch_task({"task_id": "block-me", "wait_seconds": 1}))
    assert out["reason"] == "timeout"
    assert "无新事件" in out["message"]


# ---------------------------------------------------------------------------
# result / list / cancel
# ---------------------------------------------------------------------------


def test_get_task_result_updates_ledger(gateway_env):
    tid = _parse(gateway_dispatch_task({"goal": "job"}))["task_id"]
    out = _parse(gateway_get_task_result({"task_id": tid}))
    assert out["status"] == "completed"
    assert out["result_text"] == f"full text for {tid}"
    assert "不可信数据" in out["note"]
    assert mp_tools._get_ledger().get(tid)["status"] == "completed"


def test_list_tasks_reconciles_ledger(gateway_env):
    tid = _parse(gateway_dispatch_task({"goal": "job"}))["task_id"]
    out = _parse(gateway_list_tasks({}))
    assert any(t["task_id"] == tid for t in out["tasks"])
    assert tid in out["locally_open"]


def test_list_workers_aggregates_toolsets(gateway_env):
    out = _parse(gateway_list_workers({}))
    assert out["count"] == 2
    assert out["available_toolsets"] == ["code", "research"]


def test_cancel_task_marks_ledger(gateway_env):
    tid = _parse(gateway_dispatch_task({"goal": "job"}))["task_id"]
    out = _parse(gateway_cancel_task({"task_id": tid, "reason": "user asked"}))
    assert out["cancelled"] is True
    assert out["response"]["cancelled_task_ids"] == [tid]
    assert mp_tools._get_ledger().get(tid)["status"] == "cancelled"


def test_cancel_batch_without_task_id_uses_ledger_path_task(gateway_env):
    batch = _parse(gateway_dispatch_batch({"specs": [{"goal": "A"}, {"goal": "B"}]}))
    out = _parse(gateway_cancel_task({"batch_id": batch["batch_id"]}))
    assert out["cancelled"] is True
    # The path task_id was borrowed from the ledger; batch_id rode in the body.
    assert out["task_id"] in batch["task_ids"]
    assert out["response"]["cancelled_task_ids"]
    ledger = mp_tools._get_ledger()
    for tid in batch["task_ids"]:
        assert ledger.get(tid)["status"] == "cancelled"


def test_cancel_batch_unknown_batch_errors(gateway_env):
    out = _parse(gateway_cancel_task({"batch_id": "no-such-batch"}))
    assert out["error"] == "unknown_batch"


# ---------------------------------------------------------------------------
# delegation isolation (spec §12.1 #4)
# ---------------------------------------------------------------------------


def test_all_handlers_refuse_delegated_children(gateway_env):
    with delegated_child_context("child-session"):
        for handler, args in ALL_HANDLERS:
            out = _parse(handler(dict(args)))
            assert out["error"] == "delegated_child_refused", handler.__name__


# ---------------------------------------------------------------------------
# ledger concurrency (spec §12.4 #21: writes must be lock-safe)
# ---------------------------------------------------------------------------


def test_ledger_concurrent_writes(tmp_path):
    ledger = Ledger(str(tmp_path / "conc.db"))
    n_threads, n_per = 8, 25

    def worker(worker_idx: int):
        for i in range(n_per):
            ledger.record(
                run_id="conc-run",
                task_id=f"conc-run-w{worker_idx}-{i}",
                goal=f"g{worker_idx}-{i}",
            )
            ledger.update_status(f"conc-run-w{worker_idx}-{i}", "running")

    threads = [
        threading.Thread(target=worker, args=(w,), daemon=True)
        for w in range(n_threads)
    ]
    for t in threads:
        t.start()
    for t in threads:
        t.join(timeout=30)
    assert ledger.next_seq("conc-run") == n_threads * n_per + 1
    assert len(ledger.open_tasks()) == n_threads * n_per
    ledger.close()
