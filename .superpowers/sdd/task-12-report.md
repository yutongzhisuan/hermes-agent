# Task 12 Report: P1 Definition-of-Done Gate

**Project:** Task Relay M1  
**Repo:** `/Users/suyanlong/github/hermes-agent` (branch `test`)  
**Base commit:** `cde1bf57c`  
**Date:** 2026-07-30  

---

## 1. Spec Coverage Self-Check

Each M1 design inventory bullet from `docs/superpowers/specs/2026-07-31-task-relay-design.md` §Capability Modules was mapped to implemented code and tests. Evidence exists and passes.

| Design inventory item | Implementation evidence | Test evidence |
|---|---|---|
| `task_relay_v1.proto` (core messages + RPCs) | `extend/task_relay/proto/task_relay_v1.proto`; generated stubs `extend/task_relay/gen/py/task_relay_v1_pb2.py` and `task_relay_v1_grpc.py` | Import + round-trip usage across all gRPC tests |
| Hub: db, auth, router, registry, event_bus, grpc, ws poll/claim | `hub/db.py`, `hub/auth.py`, `hub/task_router.py`, `hub/worker_registry.py`, `hub/event_bus.py`, `hub/grpc_server.py`, `hub/ws_server.py` | `test_db.py`, `test_auth.py`, `test_task_router.py`, `test_event_bus.py`, `test_grpc_watch.py`, `test_ws_poll.py` |
| Worker: poll loop, ACP backend, executor, CLI | `worker/task_worker.py`, `worker/task_worker_ws.py`, `worker/task_executor.py`, `worker/backends/stub_backend.py`, `worker/backends/acp_backend.py`, `worker/__main__.py` | `test_worker.py`, `test_cancel.py`, `test_e2e_mode_a.py` |
| Semantics: idempotency, timeouts, Watch replay, Cancel, ListWorkers | `hub/task_router.py` (idempotency, attempts, deadlines); `hub/event_bus.py` (replay, cursor, slow-consumer); `hub/grpc_server.py` (ListWorkers, CancelTask, WatchTask) | `test_task_router.py`, `test_event_bus.py`, `test_grpc_watch.py` |
| Cancel path: `task.cancel` → ACP interrupt, grace escalation, status attribution | `worker/backends/acp_backend.py`, `worker/task_executor.py`, `hub/task_router.py` | `test_cancel.py`, `test_e2e_mode_a.py::test_cancel_running_cancelled_with_partial_summary` |
| Worker JWT + Master JWT | `hub/auth.py` (issue/verify for both roles); `hub/ws_server.py` (upgrade auth); `hub/grpc_server.py` (interceptor) | `test_auth.py`, `test_ws_poll.py::test_rejects_upgrade_*`, `test_grpc_watch.py::test_auth_interceptor_*` |

### Design-section coverage (P1 scope)

| Design section | Covered in P1? | Notes |
|---|---|---|
| Responsibility split | Yes | Hub pass-through only; no goal/params mutation |
| Mode A poll | Yes | Atomic claim-on-poll, long poll, empty-poll backoff |
| Mode B / C | No | Explicitly deferred to P2 |
| gRPC service | Yes | All seven RPCs implemented and tested |
| Watch cursor / SlowConsumer | Yes | Global monotonic event_id, FAILED_PRECONDITION + CursorOutOfRange, RESOURCE_EXHAUSTED + SlowConsumer |
| Idempotency / attempts / queue / first-progress | Yes | task_id and batch_id idempotency; attempt bounds; queue_timeout; first_progress_seconds |
| Checkpoint L2 resume | Deferred P2 | L1 checkpoint API present and size-check enforced; resume_blob stored but not redispatched |
| Cancellation mapping | Yes | Cancel → `cancelled`; execution timeout → `failed`; grace escalation |
| Orchestration helpers | Deferred P3 | `depends_on`, BatchPolicy, AGGREGATE out of scope |
| ACP integration | Yes | In-process `AcpTaskBackend` green path; cancel maps to `agent.interrupt()` |
| Security JWT base | Yes | Worker + Master JWT with HS256; mTLS deferred to P3 |
| Go Hub / Master SDK | Deferred P4 | Not required for P1 green |
| Files under `extend/` | Yes | All new code lives under `hermes-agent/extend/task_relay/` |

---

## 2. Deferred to P2/P3/P4

These items were intentionally left out of P1. They are listed here so P2/P3 plans can own them without scope leaking back into M1.

### P2 — Connection & Continuity

- Mode C long session (`session_manager.py`, `session_client.py`)
  - Credit-based push delivery
  - Heartbeat / stale-session detection
  - `worker.drain` / `worker.close`
- Mode B HTTP wake (`wake_scheduler.py`, `task_worker_http.py`)
  - Single-use wake token generation and redemption
  - Wake failure falls back to Mode A poll (task stays `pending`)
- Checkpoint store + L1/L2 contract (`checkpoint_store.py`)
  - L1 observable checkpoints already persist; L2 resume blob redispatch on `task.run`
  - Oversize `resume_blob` rejection (> 1 MiB default)
- ContextRef fetch/decode + sha256 verification over plaintext

### P3 — Orchestration & Hardening

- `depends_on` DAG scheduling and cycle rejection
- `BatchPolicy` (completion modes, fail-fast, threshold, batch timeout)
- `AGGREGATE` events and `aggregate_key` pre-aggregation
- Resource-aware worker scheduling and `min_resources` hard-gating
- mTLS for Master→Hub and Worker→Hub
- Metrics (`relay_tasks_dispatched_total`, `relay_task_latency_seconds`, etc.)
- Postgres/HA store adapter behind `hub/db.py`

### P4 — Go Port

- Behavior-identical Go Hub
- Thin Go Master SDK (can begin once proto is frozen; not P1 blocker)

---

## 3. Test Run Output

Command:

```bash
cd /Users/suyanlong/github/hermes-agent && .venv/bin/python -m pytest extend/task_relay/tests/ -v
```

Result:

```
============================= test session starts ==============================
platform darwin -- Python 3.11.15, pytest-9.0.2, pluggy-1.6.0 -- /Users/suyanlong/github/hermes-agent/.venv/bin/python
cachedir: .pytest_cache
rootdir: /Users/suyanlong/github/hermes-agent
configfile: pyproject.toml
plugins: anyio-4.12.1, asyncio-1.3.0
asyncio: mode=Mode.STRICT, debug=False, asyncio_default_test_loop_scope=None, asyncio_default_test_loop_scope=function
collecting ... collected 163 items

extend/task_relay/tests/test_auth.py::test_worker_jwt_roundtrip PASSED   [  0%]
extend/task_relay/tests/test_auth.py::test_worker_jwt_exact_claim_keys PASSED [  1%]
extend/task_relay/tests/test_auth.py::test_reject_missing_audience PASSED [  1%]
extend/task_relay/tests/test_auth.py::test_reject_wrong_audience PASSED  [  2%]
extend/task_relay/tests/test_auth.py::test_reject_wrong_issuer PASSED    [  3%]
extend/task_relay/tests/test_auth.py::test_reject_bad_signature PASSED   [  3%]
extend/task_relay/tests/test_auth.py::test_reject_expired PASSED         [  4%]
extend/task_relay/tests/test_auth.py::test_reject_worker_token_missing_scope_claims PASSED [  4%]
extend/task_relay/tests/test_auth.py::test_master_jwt_roundtrip PASSED   [  5%]
extend/task_relay/tests/test_auth.py::test_master_jwt_rejected_as_worker_and_vice_versa PASSED [  6%]
extend/task_relay/tests/test_auth.py::test_exchange_bootstrap_issues_scoped_worker_jwt PASSED [  6%]
extend/task_relay/tests/test_auth.py::test_exchange_bootstrap_rejects_unknown_token PASSED [  7%]
extend/task_relay/tests/test_auth.py::test_exchange_bootstrap_rejects_worker_id_mismatch PASSED [  7%]
extend/task_relay/tests/test_auth.py::test_reject_empty_secret_direct_construction PASSED [  8%]
extend/task_relay/tests/test_auth.py::test_reject_empty_secret_from_config PASSED [  9%]
extend/task_relay/tests/test_auth.py::test_auth_from_hub_config PASSED   [  9%]
extend/task_relay/tests/test_auth.py::test_hub_config_jwt_defaults PASSED [ 10%]
extend/task_relay/tests/test_cancel.py::test_cancel_during_tool_settles_cancelled PASSED [ 11%]
extend/task_relay/tests/test_cancel.py::test_execution_timeout_attribution_is_failed PASSED [ 11%]
extend/task_relay/tests/test_cancel.py::test_acp_backend_completion_green_path PASSED [ 12%]
extend/task_relay/tests/test_cancel.py::test_worker_cancel_reason_reaches_backend PASSED [ 12%]
extend/task_relay/tests/test_cancel.py::test_executor_drops_complete_when_hub_already_terminal PASSED [ 13%]
extend/task_relay/tests/test_cancel.py::test_progress_throttling_drops_rapid_callbacks PASSED [ 14%]
extend/task_relay/tests/test_db.py::test_hub_config_defaults PASSED      [ 14%]
extend/task_relay/tests/test_db.py::test_append_event_is_globally_monotonic PASSED [ 15%]
extend/task_relay/tests/test_db.py::test_schema_tables_and_indexes PASSED [ 15%]
extend/task_relay/tests/test_db.py::test_append_event_check_constraint_rejects_missing_task_id PASSED [ 16%]
extend/task_relay/tests/test_db.py::test_task_round_trip_and_status_update PASSED [ 17%]
extend/task_relay/tests/test_db.py::test_upsert_and_get_worker PASSED    [ 17%]
extend/task_relay/tests/test_db.py::test_list_events_after_and_oldest_event_id_for_filter PASSED [ 18%]
extend/task_relay/tests/test_e2e_mode_a.py::test_dispatch_poll_stub_complete_watch_terminal PASSED [ 19%]
extend/task_relay/tests/test_e2e_mode_a.py::test_idempotent_dispatch_hit PASSED [ 19%]
extend/task_relay/tests/test_e2e_mode_a.py::test_cancel_pending_cancelled_without_worker PASSED [ 20%]
extend/task_relay/tests/test_e2e_mode_a.py::test_cancel_running_cancelled_with_partial_summary PASSED [ 20%]
extend/task_relay/tests/test_e2e_mode_a.py::test_first_progress_miss_marks_lost PASSED [ 21%]
extend/task_relay/tests/test_e2e_mode_a.py::test_watch_reconnect_since_event_id PASSED [ 22%]
extend/task_relay/tests/test_e2e_mode_a.py::test_watch_cursor_out_of_range_since_too_old PASSED [ 22%]
extend/task_relay/tests/test_e2e_mode_a.py::test_unauthorized_ws_rejected PASSED [ 23%]
extend/task_relay/tests/test_event_bus.py::test_publish_persists_first_and_returns_event PASSED [ 23%]
extend/task_relay/tests/test_event_bus.py::test_event_ids_are_globally_monotonic_across_topics PASSED [ 24%]
extend/task_relay/tests/test_event_bus.py::test_subscribe_replays_after_cursor PASSED [ 25%]
extend/task_relay/tests/test_event_bus.py::test_subscribe_since_zero_replays_from_oldest_retained PASSED [ 25%]
extend/task_relay/tests/test_event_bus.py::test_subscribe_filters_by_batch_and_task PASSED [ 26%]
extend/task_relay/tests/test_event_bus.py::test_subscribe_receives_live_events_after_replay PASSED [ 26%]
extend/task_relay/tests/test_event_bus.py::test_live_event_arriving_during_replay_is_not_duplicated PASSED [ 27%]
extend/task_relay/tests/test_event_bus.py::test_cursor_out_of_range PASSED [ 28%]
extend/task_relay/tests/test_event_bus.py::test_cursor_at_oldest_retained_is_in_range PASSED [ 28%]
extend/task_relay/tests/test_event_bus.py::test_cursor_out_of_range_not_raised_for_empty_filter PASSED [ 30%]
extend/task_relay/tests/test_event_bus.py::test_cursor_out_of_range_when_all_matching_events_pruned PASSED [ 30%]
extend/task_relay/tests/test_event_bus.py::test_subscribe_with_nonzero_cursor_on_empty_db_is_allowed PASSED [ 30%]
extend/task_relay/tests/test_event_bus.py::test_slow_consumer_closes PASSED [ 31%]
extend/task_relay/tests/test_event_bus.py::test_slow_consumer_reports_last_delivered_cursor PASSED [ 31%]
extend/task_relay/tests/test_event_bus.py::test_non_matching_events_do_not_fill_buffer PASSED [ 32%]
extend/task_relay/tests/test_event_bus.py::test_aclose_wakes_consumer_blocked_in_anext PASSED [ 33%]
extend/task_relay/tests/test_event_bus.py::test_aclose_delivers_buffered_live_events_before_ending PASSED [ 33%]
extend/task_relay/tests/test_event_bus.py::test_event_filter_requires_a_selector PASSED [ 34%]
extend/task_relay/tests/test_grpc_watch.py::test_dispatch_requires_authorization PASSED [ 34%]
extend/task_relay/tests/test_grpc_watch.py::test_dispatch_rejects_worker_jwt PASSED [ 35%]
extend/task_relay/tests/test_grpc_watch.py::test_dispatch_rejects_invalid_token PASSED [ 36%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[missing-DispatchTask] PASSED [ 36%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[missing-DispatchTaskBatch] PASSED [ 37%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[missing-GetTaskResult] PASSED [ 38%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[missing-WatchTask] PASSED [ 38%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[missing-ListWorkers] PASSED [ 39%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[missing-ListTasks] PASSED [ 39%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[missing-CancelTask] PASSED [ 40%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[invalid_bearer-DispatchTask] PASSED [ 41%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[invalid_bearer-DispatchTaskBatch] PASSED [ 41%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[invalid_bearer-GetTaskResult] PASSED [ 42%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[invalid_bearer-WatchTask] PASSED [ 42%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[invalid_bearer-ListWorkers] PASSED [ 43%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[invalid_bearer-ListTasks] PASSED [ 44%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[invalid_bearer-CancelTask] PASSED [ 44%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[bare_token-DispatchTask] PASSED [ 45%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[bare_token-DispatchTaskBatch] PASSED [ 46%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[bare_token-GetTaskResult] PASSED [ 46%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[bare_token-WatchTask] PASSED [ 47%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[bare_token-ListWorkers] PASSED [ 47%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[bare_token-ListTasks] PASSED [ 48%]
extend/task_relay/tests/test_grpc_watch.py::test_auth_interceptor_rejects_all_rpcs[bare_token-CancelTask] PASSED [ 49%]
extend/task_relay/tests/test_grpc_watch.py::test_watch_receives_terminal PASSED [ 49%]
extend/task_relay/tests/test_grpc_watch.py::test_dispatch_task_batch PASSED [ 50%]
extend/task_relay/tests/test_grpc_watch.py::test_get_task_result_returns_terminal PASSED [ 50%]
extend/task_relay/tests/test_grpc_watch.py::test_list_tasks_clamps_limit PASSED [ 51%]
extend/task_relay/tests/test_grpc_watch.py::test_cancel_pending_task PASSED [ 52%]
extend/task_relay/tests/test_grpc_watch.py::test_cancel_unknown_task_returns_not_found PASSED [ 52%]
extend/task_relay/tests/test_grpc_watch.py::test_list_workers_filters_toolsets PASSED [ 53%]
extend/task_relay/tests/test_grpc_watch.py::test_watch_cursor_out_of_range PASSED [ 53%]
extend/task_relay/tests/test_task_router.py::test_idempotent_dispatch PASSED [ 54%]
extend/task_relay/tests/test_task_router.py::test_dispatch_returns_actual_status PASSED [ 55%]
extend/task_relay/tests/test_task_router.py::test_dispatch_rejects_invalid_spec PASSED [ 55%]
extend/task_relay/tests/test_task_router.py::test_dispatch_uses_spec_timeouts PASSED [ 56%]
extend/task_relay/tests/test_task_router.py::test_atomic_claim_moves_pending_to_running PASSED [ 57%]
extend/task_relay/tests/test_task_router.py::test_claim_emits_running_status_and_progress PASSED [ 57%]
extend/task_relay/tests/test_task_router.py::test_claim_increments_attempt_and_sets_deadlines PASSED [ 58%]
extend/task_relay/tests/test_task_router.py::test_claim_respects_max_tasks PASSED [ 58%]
extend/task_relay/tests/test_task_router.py::test_claim_skips_missing_worker PASSED [ 59%]
extend/task_relay/tests/test_task_router.py::test_claim_skips_offline_or_draining_worker PASSED [ 60%]
extend/task_relay/tests/test_task_router.py::test_claim_respects_allowed_worker_ids PASSED [ 60%]
extend/task_relay/tests/test_task_router.py::test_claim_respects_deny_worker_ids PASSED [ 61%]
extend/task_relay/tests/test_task_router.py::test_claim_respects_toolsets PASSED [ 61%]
extend/task_relay/tests/test_task_router.py::test_claim_jwt_toolset_scope_allows_claim PASSED [ 62%]
extend/task_relay/tests/test_task_router.py::test_claim_jwt_toolset_scope_denies_claim PASSED [ 63%]
extend/task_relay/tests/test_task_router.py::test_claim_jwt_toolset_scope_intersects_with_advertised PASSED [ 63%]
extend/task_relay/tests/test_task_router.py::test_on_progress_extends_lease_and_clears_first_progress_deadline PASSED [ 64%]
extend/task_relay/tests/test_task_router.py::test_complete_is_monotonic PASSED [ 65%]
extend/task_relay/tests/test_task_router.py::test_complete_emits_terminal_event PASSED [ 65%]
extend/task_relay/tests/test_task_router.py::test_on_complete_for_missing_task_raises PASSED [ 66%]
extend/task_relay/tests/test_task_router.py::test_queue_timeout_marks_lost PASSED [ 66%]
extend/task_relay/tests/test_task_router.py::test_first_progress_deadline_marks_lost PASSED [ 67%]
extend/task_relay/tests/test_task_router.py::test_execution_lease_timeout_marks_failed PASSED [ 68%]
extend/task_relay/tests/test_task_router.py::test_redispatch_lost_when_allow_redispatch_and_attempts_remain PASSED [ 68%]
extend/task_relay/tests/test_task_router.py::test_completed_not_redispatched PASSED [ 69%]
extend/task_relay/tests/test_task_router.py::test_redispatch_exhausted_attempts_stays_terminal PASSED [ 69%]
extend/task_relay/tests/test_task_router.py::test_cancel_pending_immediately PASSED [ 70%]
extend/task_relay/tests/test_task_router.py::test_cancel_running_hits_grace_then_cancelled PASSED [ 71%]
extend/task_relay/tests/test_task_router.py::test_cancel_running_worker_settles_first PASSED [ 71%]
extend/task_relay/tests/test_task_router.py::test_dispatch_task_batch_treats_tasks_independent PASSED [ 72%]
extend/task_relay/tests/test_task_router.py::test_batch_idempotent_on_tasks PASSED [ 73%]
extend/task_relay/tests/test_task_router.py::test_batch_idempotent_exact_redispatch_returns_existing PASSED [ 73%]
extend/task_relay/tests/test_task_router.py::test_batch_redispatch_with_different_spec_not_idempotent PASSED [ 74%]
extend/task_relay/tests/test_task_router.py::test_batch_stores_policy_json PASSED [ 74%]
extend/task_relay/tests/test_task_router.py::test_batch_rejects_dependency_cycle PASSED [ 75%]
extend/task_relay/tests/test_task_router.py::test_status_vocabulary_rejects_invalid_transition PASSED [ 76%]
extend/task_relay/tests/test_worker.py::test_worker_stub_backend_executes_task_to_completion PASSED [ 76%]
extend/task_relay/tests/test_worker.py::test_worker_caps_max_concurrent_to_jwt_claim PASSED [ 77%]
extend/task_relay/tests/test_worker.py::test_worker_keeps_cli_max_concurrent_when_below_jwt_claim PASSED [ 77%]
extend/task_relay/tests/test_worker.py::test_worker_defaults_max_concurrent_when_jwt_claim_missing PASSED [ 78%]
extend/task_relay/tests/test_worker.py::test_worker_logs_warning_when_cli_exceeds_jwt_claim PASSED [ 79%]
extend/task_relay/tests/test_worker.py::test_worker_cancel_sets_backend_cancel_event PASSED [ 79%]
extend/task_relay/tests/test_worker.py::test_worker_backoff_doubles_on_empty_poll PASSED [ 80%]
extend/task_relay/tests/test_worker.py::test_worker_backoff_resets_after_offered_task PASSED [ 80%]
extend/task_relay/tests/test_worker.py::test_worker_concurrency_limit_is_respected PASSED [ 81%]
extend/task_relay/tests/test_worker.py::test_worker_sends_single_failed_complete_when_backend_raises PASSED [ 82%]
extend/task_relay/tests/test_worker.py::test_executor_does_not_send_duplicate_complete_on_send_failure PASSED [ 82%]
extend/task_relay/tests/test_worker.py::test_worker_does_not_send_second_complete_when_first_raises PASSED [ 83%]
extend/task_relay/tests/test_worker.py::test_load_jwt_reads_and_strips_file PASSED [ 84%]
extend/task_relay/tests/test_worker.py::test_worker_announce_uses_session_modes_from_cli PASSED [ 84%]
extend/task_relay/tests/test_worker.py::test_cli_session_modes_default_is_a PASSED [ 85%]
extend/task_relay/tests/test_worker.py::test_cli_session_modes_can_be_comma_separated PASSED [ 85%]
extend/task_relay/tests/test_worker.py::test_cli_rejects_session_modes_without_a PASSED [ 86%]
extend/task_relay/tests/test_ws_poll.py::test_rejects_upgrade_without_authorization PASSED [ 87%]
extend/task_relay/tests/test_ws_poll.py::test_rejects_upgrade_with_bad_token PASSED [ 87%]
extend/task_relay/tests/test_ws_poll.py::test_accepts_valid_token PASSED [ 88%]
extend/task_relay/tests/test_ws_poll.py::test_methods_before_announce_return_error PASSED [ 88%]
extend/task_relay/tests/test_ws_poll.py::test_announce_rejects_worker_id_mismatch PASSED [ 89%]
extend/task_relay/tests/test_ws_poll.py::test_announce_rejects_without_mode_a PASSED [ 90%]
extend/task_relay/tests/test_ws_poll.py::test_mode_a_poll_claims_task PASSED [ 90%]
extend/task_relay/tests/test_ws_poll.py::test_poll_returns_empty_when_no_work PASSED [ 91%]
extend/task_relay/tests/test_ws_poll.py::test_task_progress_extends_lease PASSED [ 92%]
extend/task_relay/tests/test_ws_poll.py::test_task_complete_marks_terminal PASSED [ 92%]
extend/task_relay/tests/test_ws_poll.py::test_task_checkpoint_persists_l1_and_rejects_oversized_blob PASSED [ 93%]
extend/task_relay/tests/test_ws_poll.py::test_worker_heartbeat PASSED    [ 93%]
extend/task_relay/tests/test_ws_poll.py::test_worker_drain PASSED        [ 94%]
extend/task_relay/tests/test_ws_poll.py::test_worker_close_marks_offline PASSED [ 95%]
extend/task_relay/tests/test_ws_poll.py::test_worker_nack_releases_task PASSED [ 95%]
extend/task_relay/tests/test_ws_poll.py::test_malformed_json_returns_parse_error PASSED [ 96%]
extend/task_relay/tests/test_ws_poll.py::test_unknown_method_returns_method_not_found PASSED [ 96%]
extend/task_relay/tests/test_ws_poll.py::test_missing_method_field_returns_invalid_request PASSED [ 97%]
extend/task_relay/tests/test_ws_poll.py::test_non_string_method_returns_invalid_request PASSED [ 98%]
extend/task_relay/tests/test_ws_poll.py::test_announce_stores_session_modes_uppercase PASSED [ 98%]
extend/task_relay/tests/test_ws_poll.py::test_disconnect_does_not_overwrite_newer_session PASSED [ 99%]
extend/task_relay/tests/test_ws_poll.py::test_task_cancel_pushed_to_worker PASSED [100%]

============================= 163 passed in 12.33s ==============================
```

---

## 4. Files Changed

Only documentation/plan files were modified for this gate task.

| File | Change |
|---|---|
| `/Users/suyanlong/github/infa/docs/superpowers/plans/2026-07-30-task-relay-m1-delivery-core.md` | Marked Task 12 steps complete; expanded coverage table with exact module/test paths; added explicit P2/P3/P4 deferred list |
| `/Users/suyanlong/github/infa/docs/superpowers/plans/2026-07-30-task-relay-roadmap.md` | Added status column; marked P1 done; updated next action to author/execute P2 |
| `/Users/suyanlong/github/hermes-agent/.superpowers/sdd/task-12-report.md` | Created this report |

No implementation code was added or changed.

---

## 5. Self-Review

- [x] Spec coverage table maps every M1 design inventory bullet to code and tests.
- [x] Deferred items are explicitly listed and assigned to P2/P3/P4.
- [x] Roadmap marks P1 done and points to P2.
- [x] Full test suite passes: 163/163.
- [x] No implementation code was written for this gate task.
- [x] No placeholder TBD items remain inside P1 scope.

### Known residual edge cases (documented in prior task reports, not blockers for P1)

- `AcpTaskBackend` cancellation relies on the agent loop checking `interrupt()`; an in-flight tool call is not killed. This is the designed cooperative behavior.
- L2 resume is not implemented; the checkpoint size check is present.
- `depends_on` cycle rejection exists for batch validation but dependency scheduling itself is a P3 concern.

### Verdict

P1 M1 Delivery Core meets the definition of done.

---

## 6. Final Whole-Branch Review Fixes

Commit: `6b26aad4f` — `fix(task-relay): final review Important fixes — timeout cancel, guard bypass, CLI backend, proto script`

### What changed

1. **`extend/task_relay/worker/task_worker.py`**
   - The outer `except Exception` fallback in `_execute_one` now routes through `executor._complete_once` (which calls `_guard_settlement`) instead of sending `task.complete` directly. Added `TaskCompletePayload` import.

2. **`extend/task_relay/hub/task_router.py`**
   - Running execution/lease timeout now transitions to `cancelling` with reason `"timeout"` (new `_enter_cancelling` helper) instead of immediately settling `failed`.
   - The cancel-grace expiry path now settles `failed` when the cancelling reason is `"timeout"`, otherwise `cancelled`.
   - `on_progress` no longer extends `claim_expires_at` when `task.status == "cancelling"`.
   - `_existing_result` now includes the full task row fields needed for a complete `TaskResult` proto.

3. **`extend/task_relay/hub/ws_server.py`**
   - `_cancel_monitor_loop` tracks already-notified cancelling task IDs in a per-session `set[str]` and sends only once per task/session.

4. **`extend/task_relay/hub/grpc_server.py`**
   - `TaskRelayService` now receives `db`, `bus`, and `registry` as explicit constructor arguments instead of reaching into `router._db` etc.
   - `serve_grpc` signature updated to accept `db`, `bus`, `registry`.
   - `_existing_result_to_proto` now populates the full `TaskResult`: `task_id`, `result_text`, `fields`, `usage`, `started_at`, `batch_id`, `latest_checkpoint_id`, `schema_version`.

5. **`extend/task_relay/worker/__main__.py`**
   - Added `"acp"` to the `--backend` choices.
   - Wired `_create_backend` to instantiate `AcpTaskBackend` with `--acp-progress-interval-seconds` (default 5.0).

6. **`extend/task_relay/scripts/gen_proto.sh`**
   - The import rewrite is now idempotent (succeeds if already relative) and exits non-zero if the expected top-level import is not found. Uses exact-line matching and a replacement count check.

### Test changes

- Added failing tests first for findings 2, 3, 4 (TDD):
  - `test_execution_lease_timeout_enters_cancelling_with_timeout_reason`
  - `test_execution_lease_timeout_grace_expires_to_failed`
  - `test_on_progress_during_cancelling_does_not_extend_grace_window`
  - `test_task_cancel_pushed_only_once_per_session`
  - `test_execution_timeout_pushes_cancel_to_worker`
- Added `test_worker_fallback_complete_uses_settlement_guard` for finding 1.
- Updated fixtures in `conftest.py`, `test_grpc_watch.py`, and `test_e2e_mode_a.py` for the new `serve_grpc` signature.
- Updated `FakeWorkerWs` in `test_worker.py` to support `task.status` responses.

### Verification

```bash
cd /Users/suyanlong/github/hermes-agent
.venv/bin/python -m pytest extend/task_relay/tests/ -v
```

Result: **168 passed in 20.17s**

---

## 7. Final Re-Review Important Fixes

Commit: `fix(task-relay): cancel reason column, base64 inline_gzip round-trip, retention pruning`

### What changed

1. **`extend/task_relay/hub/models.py`**
   - Added `cancel_reason: str | None = None` to the `Task` dataclass.

2. **`extend/task_relay/hub/db.py`**
   - Added `cancel_reason TEXT` column to the `tasks` table schema.
   - Enabled `PRAGMA foreign_keys = ON` on new connections so cascading deletes work if foreign keys are later added by migration.

3. **`extend/task_relay/hub/task_router.py`**
   - Added dedicated `cancel_reason` tracking. `_enter_cancelling` and `on_cancel` now write the reason into `task.cancel_reason` (and keep `task.summary` in sync).
   - Cancel-grace expiry now uses `task.cancel_reason == "timeout"` to decide between `failed` and `cancelled`. A Master cancel that arrives while a task is already timeout-cancelling correctly flips attribution to `cancelled`.
   - Redispatch now clears `cancel_reason` when reopening a terminal task.
   - Added `prune_old_data()` and wired it to `tick_timeouts()` (hourly, throttled by `PRUNE_INTERVAL_SECONDS`).
   - Retention policy: delete `checkpoints`, then `task_events`, then terminal `tasks` older than `config.retention_days`. In-flight (non-terminal) tasks are never deleted.

4. **`extend/task_relay/hub/ws_server.py`**
   - `_cancel_monitor_loop` now reads `cancel_reason` instead of `summary` when pushing `task.cancel` reason to the worker.

5. **`extend/task_relay/hub/grpc_server.py`**
   - `_context_payload_to_dict` now stores `inline_gzip.gzip_data` as standard base64 (instead of hex) so binary payloads survive JSON round-trips.

6. **`extend/task_relay/worker/task_worker.py`**
   - `_run_payload_from_dict` decodes base64 `inline_gzip.gzip_data` back to `bytes` before passing the context to the backend, restoring the original compressed bytes end-to-end.

### Test changes

- Added `extend/task_relay/tests/test_rereview_fixes.py` with failing tests first (TDD):
  - `test_execution_timeout_sets_cancel_reason_timeout`
  - `test_master_cancel_during_timeout_cancelling_settles_cancelled`
  - `test_context_payload_to_dict_base64_encodes_gzip_data`
  - `test_run_payload_decodes_inline_gzip_base64_to_bytes`
  - `test_prune_old_data_removes_old_events_and_checkpoints_and_tasks`
- Updated `extend/task_relay/tests/test_task_router.py::test_execution_lease_timeout_enters_cancelling_with_timeout_reason` to assert `cancel_reason == "timeout"`.

### Verification

```bash
cd /Users/suyanlong/github/hermes-agent
.venv/bin/python -m pytest extend/task_relay/tests/ -v
```

Result: **173 passed in 25.49s**


---

## 8. Final Merge Blocker Fix: SQLite Migration for `cancel_reason`

Commit: `4868cd290694172054cc463879498d0295523e2d fix(task-relay): add SQLite migration for cancel_reason column`

### Finding

Final whole-branch re-review found that `open_db()` only ran `CREATE TABLE IF NOT EXISTS`, so databases created before the `cancel_reason` column was added lacked it. This caused `_persist_task` and `_cancel_monitor_loop` to fail at runtime when they read or wrote `task.cancel_reason`.

### What changed

1. **`extend/task_relay/hub/db.py`**
   - Added `_migrate()` helper that runs after `_SCHEMA` creation.
   - Attempts `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS cancel_reason TEXT`.
   - Falls back to `PRAGMA table_info(tasks)` + conditional `ALTER TABLE tasks ADD COLUMN cancel_reason TEXT` when the running SQLite rejects the `IF NOT EXISTS` clause (syntax error).
   - Ignores `duplicate column name` errors and re-raises any other unexpected `OperationalError`.

2. **`extend/task_relay/tests/test_db.py`**
   - Added `test_open_db_migrates_cancel_reason_column`.
   - Creates a legacy database with the pre-`cancel_reason` schema using the standard `sqlite3` module, then opens it with `open_db()` and verifies the column exists and round-trips a value.

### Verification

```bash
cd /Users/suyanlong/github/hermes-agent
.venv/bin/python -m pytest extend/task_relay/tests/ -v
```

Result: **174 passed in 25.54s**


---

## 9. Final Re-review Important Findings (Task Relay P1)

Commit: `6145a3dda fix(task-relay): checkpoint event mapping, batch idempotent_hit semantics, cancel notification reset`

### Findings

1. **`WatchTask` CHECKPOINT events did not carry checkpoint data.**
   `extend/task_relay/hub/grpc_server.py::_event_to_proto()` handled `TERMINAL`, `PROGRESS`, and `STATUS` but left `TaskEvent.checkpoint` unset for `CHECKPOINT` events.

2. **Batch `idempotent_hit` semantics were wrong for newly created batches.**
   `extend/task_relay/hub/task_router.py::dispatch_task_batch()` returned `idempotent_hit=all(r.idempotent_hit for r in responses)` on the create path. Per the proto comment, batch-level `idempotent_hit` must be true only when `batch_id` already existed, not when the tasks inside it already existed.

3. **`_cancel_monitor_loop` never cleared `_notified_cancelling`.**
   `extend/task_relay/hub/ws_server.py::_cancel_monitor_loop()` added task ids to the per-session set but never removed them. If a task id was redispatched and cancelled again in the same worker session, the second `task.cancel` frame was not pushed.

### What changed

1. **`extend/task_relay/hub/grpc_server.py`**
   - Added `CHECKPOINT` handling in `_event_to_proto()`.
   - Maps `checkpoint_id`, `summary`, and `fields_json` from the event payload into `pb.TaskCheckpoint`.
   - Sets `task_id`, `event_id`, and `checkpoint_at` from the event row.

2. **`extend/task_relay/hub/task_router.py`**
   - On the create path, hard-coded `idempotent_hit=False` for the batch response.
   - Existing-batch path continues to return `idempotent_hit=True`.

3. **`extend/task_relay/hub/ws_server.py`**
   - `_cancel_monitor_loop()` now tracks the set of currently cancelling tasks for this worker.
   - After notifying new cancellations, it updates `_notified_cancelling` to the intersection of the notified set and the currently cancelling set, so ids that have left `cancelling` can be notified again after redispatch.

### Tests

- `extend/task_relay/tests/test_grpc_watch.py::test_event_to_proto_maps_checkpoint_payload`
- `extend/task_relay/tests/test_task_router.py::test_batch_create_idempotent_hit_false_even_when_tasks_preexist`
- `extend/task_relay/tests/test_ws_poll.py::test_task_cancel_notification_resets_after_task_leaves_cancelling`

### Verification

```bash
cd /Users/suyanlong/github/hermes-agent
.venv/bin/python -m pytest extend/task_relay/tests/ -v
```

Result: **177 passed in 28.65s**


---

## 10. Final Re-review Important Finding: Binary `resume_blob` in `task.run` Payload

Commit: `671c0559b fix(task-relay): base64-encode resume_blob in task.run payload`

### Finding

`extend/task_relay/hub/ws_server.py::_build_run_payload()` UTF-8 decoded `latest.resume_blob` (`latest.resume_blob.decode("utf-8")`). The schema stores it as `BLOB` and the proto defines it as `bytes`, so opaque binary blobs would crash redispatch.

### What changed

1. **`extend/task_relay/hub/ws_server.py`**
   - `_build_run_payload()` now base64-encodes `resume_blob` with `base64.b64encode(...).decode("ascii")` before placing it in the JSON `task.run` payload.
   - Updated the method docstring to document that `resume_blob` is base64-encoded for JSON transport.

2. **`extend/task_relay/worker/task_worker.py`**
   - Added `_decode_resume_blob()` helper that decodes a base64 `resume_blob` string back to `bytes` when receiving `task.run`.
   - Falls back to leaving the value as a string if it is not valid base64 (e.g. an older plain-string blob).
   - `_run_payload_from_dict()` now uses `_decode_resume_blob()` for the `resume_blob` field.

3. **`extend/task_relay/worker/task_executor.py`**
   - Updated `TaskRunPayload.resume_blob` type from `str | None` to `str | bytes | None` to reflect the decoded bytes path.

### Test changes

- Added `extend/task_relay/tests/test_ws_poll.py::test_poll_run_payload_base64_encodes_binary_resume_blob`.
- Creates a checkpoint with a binary `resume_blob` (including non-UTF-8 bytes), persists it, then polls and verifies the `task.run` payload contains a base64 string that round-trips back to the original bytes without crashing.

### Verification

```bash
cd /Users/suyanlong/github/hermes-agent
.venv/bin/python -m pytest extend/task_relay/tests/ -v
```

Result: **178 passed in 28.67s**


---

## 11. Final Re-review Critical + Important Findings (Task Relay P1)

Commit: `fix(task-relay): correct serve_grpc call, enforce JWT max_concurrent, full terminal results, checkpoint blob symmetry, auth error hygiene`

### Findings

1. **`serve_grpc` was called with the wrong signature in `hub/main.py`.**
   `extend/task_relay/hub/main.py::run()` invoked `serve_grpc(router, auth, hub_config, host=..., port=...)` but `extend/task_relay/hub/grpc_server.py::serve_grpc()` requires `db`, `bus`, and `registry` after `config`. This prevented the Hub process from starting.

2. **Worker-announced `max_concurrent` was not capped to the JWT claim on the Hub side.**
   `extend/task_relay/hub/ws_server.py::_handle_worker_announce()` accepted the worker's announced `max_concurrent` without clamping it to `claims.max_concurrent`, allowing a worker to exceed its JWT-authorized concurrency.

3. **`WatchTask` TERMINAL events did not carry the full `TaskResult`.**
   `extend/task_relay/hub/grpc_server.py::_event_to_proto()` only populated `status`, `summary`, `error`, and `attempt` for TERMINAL events, omitting `result_text`, `fields`, `usage`, `worker_id`, `batch_id`, `latest_checkpoint_id`, and timestamps.

4. **Worker→Hub checkpoint `resume_blob` was not base64-decoded.**
   `extend/task_relay/hub/ws_server.py::_handle_task_checkpoint()` stored a base64 string as UTF-8 bytes instead of decoding it back to the original bytes, breaking symmetry with the Hub→Worker direction.

5. **Auth failure responses could leak token-validation details.**
   `extend/task_relay/hub/ws_server.py::_process_request()` included `str(exc)` in the 401 response body, exposing information such as "Not enough segments" or expiration details.

### What changed

1. **`extend/task_relay/hub/main.py`**
   - `run()` now passes `db`, `bus`, and `registry` explicitly to `serve_grpc()`.

2. **`extend/task_relay/hub/ws_server.py`**
   - `_handle_worker_announce()` clamps `max_concurrent` to `min(announced, claims.max_concurrent)` before persisting the worker.
   - `_handle_task_checkpoint()` attempts `base64.b64decode(resume_blob, validate=True)` when the incoming value is a string, falling back to UTF-8 encoding for plain-string backwards compatibility.
   - `_process_request()` returns a generic `"Invalid or missing token"` 401 body and logs the verification failure detail at debug level.
   - Added module-level `logger`.

3. **`extend/task_relay/hub/grpc_server.py`**
   - Imported `Database`.
   - `_event_to_proto()` is now async and accepts an optional `db` parameter.
   - For TERMINAL events, when `db` is provided and the task row exists, it reuses `_task_to_result_proto()` to populate the full `TaskResult`.
   - `WatchTask` awaits `_event_to_proto()` and passes `self._db`.

4. **`extend/task_relay/tests/test_grpc_watch.py`**
   - Updated `test_event_to_proto_maps_checkpoint_payload` to be async and await `_event_to_proto()`.

### Tests

- Added `extend/task_relay/tests/test_final_rereview_fixes.py`:
  - `test_announce_clamps_max_concurrent_to_jwt_claim`
  - `test_event_to_proto_terminal_includes_full_task_result`
  - `test_task_checkpoint_decodes_base64_resume_blob`
  - `test_checkpoint_resume_blob_round_trips_through_poll`
  - `test_rejects_upgrade_with_generic_error_body`

### Verification

Hub smoke test:

```bash
cd /Users/suyanlong/github/hermes-agent
timeout 5 .venv/bin/python -m extend.task_relay.hub --grpc-port 9090 --ws-port 9000 --db /tmp/relay-test.db --jwt-secret test
```

Result: Hub logs gRPC/WebSocket listening and stops cleanly on SIGTERM (exit code 124 from `timeout`).

Full test suite:

```bash
cd /Users/suyanlong/github/hermes-agent
.venv/bin/python -m pytest extend/task_relay/tests/ -v
```

Result: **183 passed in 28.58s**


---

## 12. Final Re-review Remaining Important Findings

Commit: `387d8ffc9 fix(task-relay): gRPC auth error hygiene and Hub worker capacity enforcement`

### Findings

1. **gRPC auth failures still leaked token-validation details.**
   `extend/task_relay/hub/grpc_server.py::MasterAuthInterceptor.authenticate()` raised `GRPCError(Status.UNAUTHENTICATED, str(e))` from `AuthError`, exposing strings like `"Not enough segments"` to the Master client.

2. **Hub poll did not enforce worker capacity, and `running_tasks` was stale.**
   `extend/task_relay/hub/task_router.py::atomic_claim_for_poll()` ignored `worker.running_tasks` and never updated it, so a worker could claim an unbounded number of tasks and `ListWorkers` reported an always-zero `running_tasks`.

3. **`--max-concurrent` help text was misleading.**
   `extend/task_relay/worker/__main__.py` described `--max-concurrent` as overriding the JWT limit, but the Hub caps it to the JWT claim.

### What changed

1. **`extend/task_relay/hub/grpc_server.py`**
   - Added module-level `logger = logging.getLogger("task_relay.hub.grpc")`.
   - In `MasterAuthInterceptor.authenticate()`, `AuthError` now logs the verification failure detail at debug level and raises `GRPCError(Status.UNAUTHENTICATED, "Invalid or missing token")`, mirroring the WebSocket auth error hygiene.

2. **`extend/task_relay/hub/task_router.py`**
   - `atomic_claim_for_poll()` now fetches the worker inside the router lock, clamps `max_tasks` to `max(0, worker.max_concurrent - worker.running_tasks)`, and returns early when capacity is zero.
   - Each successful claim increments `worker.running_tasks`.
   - Poll refreshes `worker.last_heartbeat_at` and persists the worker row.
   - Added `_release_task_slot()` helper that decrements `worker.running_tasks` (clamped at zero).
   - `on_complete()` releases the slot when the previous status was `running` or `cancelling`.
   - `_settle_lost()`, `_settle_failed()`, and `_settle_cancelled()` release the slot when a running/cancelling task reaches a terminal state.

3. **`extend/task_relay/worker/__main__.py`**
   - Changed `--max-concurrent` help text to `"capped by the JWT's limit"`.

### Tests

- Added to `extend/task_relay/tests/test_final_rereview_fixes.py`:
  - `test_grpc_auth_error_is_generic_for_malformed_token`
  - `test_grpc_auth_error_is_generic_for_worker_token`
  - `test_poll_capacity_clamped_by_remaining_slots`
  - `test_poll_updates_worker_last_heartbeat_at`

### Verification

```bash
cd /Users/suyanlong/github/hermes-agent
.venv/bin/python -m pytest extend/task_relay/tests/ -v
```

Result: **187 passed in 28.69s**


---

## 13. Final Re-review Remaining Important Findings (Task Relay P1)

Commit: `fix(task-relay): preserve running_tasks on re-announce, worker staleness, atomic claim queue`

### Findings

1. **Worker re-announce reset `running_tasks` to 0, allowing Hub-side overcommit across reconnects.**
   `extend/task_relay/hub/worker_registry.py::announce()` created a fresh `Worker(..., running_tasks=0)` and `db.upsert_worker()` updated all columns, so a reconnecting worker would report zero active tasks and could be over-allocated.

2. **No heartbeat staleness detector.**
   `extend/task_relay/hub/task_router.py::tick_timeouts()` did not check how long a worker had been silent, so crashed or partitioned workers stayed `idle` indefinitely and kept tasks assigned to them.

3. **`atomic_claim_for_poll` read the pending queue outside the router lock.**
   `extend/task_relay/hub/task_router.py::atomic_claim_for_poll()` selected pending rows before acquiring `self._lock`, so capacity and queue were not checked atomically. This caused wasted scans and potential starvation.

4. **Duplicate `_json_list` helper.**
   Both `extend/task_relay/hub/task_router.py` and `extend/task_relay/hub/worker_registry.py` defined an identical `_json_list()` helper.

### What changed

1. **`extend/task_relay/hub/worker_registry.py`**
   - `announce()` now queries `SELECT COUNT(*) FROM tasks WHERE worker_id = ? AND status IN ('running', 'cancelling')` and uses that count for `running_tasks` instead of 0.
   - `announce()` sets `last_seen_at = now` (in addition to `last_announce_at` and `last_heartbeat_at`).
   - Removed the local `_json_list()` helper; now imports it from `extend.task_relay.hub.models`.

2. **`extend/task_relay/hub/task_router.py`**
   - Moved the pending-task `SELECT` inside `async with self._lock` in `atomic_claim_for_poll()` so capacity and queue are evaluated atomically.
   - Poll now updates both `worker.last_heartbeat_at` and `worker.last_seen_at`.
   - `tick_timeouts()` now marks workers stale when `last_seen_at` is older than `config.worker_stale_seconds`.
   - Imported `Worker` and `_json_list` from `models.py`; removed the local `_json_list()` duplicate.

3. **`extend/task_relay/hub/ws_server.py`**
   - `worker.heartbeat` handler now updates `worker.last_seen_at` in addition to `worker.last_heartbeat_at`.

4. **`extend/task_relay/hub/models.py`**
   - Added `last_seen_at: float | None = None` to the `Worker` dataclass.
   - Added a shared `_json_list()` helper at module level.

5. **`extend/task_relay/hub/db.py`**
   - Added `last_seen_at REAL` to the `workers` table schema.
   - Added migration in `_migrate()` to add `last_seen_at` to existing databases (with fallback for older SQLite builds).

6. **`extend/task_relay/hub/config.py`**
   - Added `worker_stale_seconds: int = 300` to `HubConfig`.

### Tests

- Added to `extend/task_relay/tests/test_final_rereview_fixes.py`:
  - `test_re_announce_preserves_running_task_count`
  - `test_re_announce_preserves_cancelling_task_count`
  - `test_stale_worker_marked_after_missing_heartbeat`
  - `test_stale_worker_not_eligible_for_poll`
  - `test_concurrent_polls_do_not_overclaim_single_task`

### Verification

```bash
cd /Users/suyanlong/github/hermes-agent
.venv/bin/python -m pytest extend/task_relay/tests/ -v
```

Result: **192 passed in 29.97s**


---

## Final Re-Review Fix Report

**Commit:** `35220b6c1fbba37cd27ecdd3c06e3c1cb0f7b75d`  
**Subject:** `fix(task-relay): heartbeat recovers stale workers, explicit zero grace, remove dead claim path`  
**Date:** 2026-07-31  

### Findings addressed

| Finding | Severity | File(s) | Fix |
|---|---|---|---|
| Stale workers do not recover when heartbeats resume | Important | `extend/task_relay/hub/ws_server.py` | `_handle_worker_heartbeat()` now transitions `status == "stale"` back to `"idle"` and persists the worker. |
| `CancelTask.grace_seconds = 0` treated as unset | Minor | `extend/task_relay/proto/task_relay_v1.proto`, `extend/task_relay/hub/grpc_server.py`, generated stubs | Marked `grace_seconds` as `optional int32`; regenerated stubs; `CancelTask` now uses `request.HasField("grace_seconds")` so explicit `0` means zero grace while unset still falls back to the Hub default. |
| `_handle_worker_claim()` dead two-step code path | Minor | `extend/task_relay/hub/ws_server.py` | Removed `_handle_worker_claim()` and the unused `DEFAULT_TWO_STEP_CLAIM_TIMEOUT_S` constant. M1 uses atomic claim-on-poll exclusively; any `worker.claim` request now returns the standard "unknown method" JSON-RPC error. |

### Tests added

- `test_final_rereview_fixes.py::test_heartbeat_recover_stale_worker` — verifies a heartbeat returns a stale worker to `idle`.
- `test_final_rereview_fixes.py::test_cancel_task_explicit_zero_grace` — verifies `grace_seconds=0` produces a cancel-grace deadline at the current time.
- `test_final_rereview_fixes.py::test_cancel_task_unset_grace_uses_default` — verifies an unset `grace_seconds` still uses the Hub default (60s).

### Verification

```bash
cd /Users/suyanlong/github/hermes-agent && .venv/bin/python -m pytest extend/task_relay/tests/ -v
```

Result: **195 passed in 30.10s** (pristine output, no warnings/failures).


---

## 14. Final Re-Review Remaining Important Findings (Task Relay P1)

**Commit:** `c221047f2`  
**Subject:** `fix(task-relay): guard heartbeat by session ownership and preserve drain on stale recovery`  
**Date:** 2026-07-31

### Findings

1. **Heartbeat bypassed session ownership check.**
   `extend/task_relay/hub/ws_server.py::_handle_worker_heartbeat()` updated `last_seen_at` and transitioned `stale → idle` without verifying the heartbeat came from the worker's current active session. A heartbeat from a superseded (e.g., stale WebSocket) session could resurrect a stale worker or refresh timestamps even though a newer session had taken over.

2. **Stale recovery clobbered `draining`.**
   When `_handle_worker_heartbeat()` recovered a worker from `stale`, it always restored status to `idle`, discarding a prior `draining` state.

### What changed

1. **`extend/task_relay/hub/ws_server.py`**
   - `_handle_worker_heartbeat()` now calls `_is_current_session_for_worker()` before writing any worker row updates.
   - If the session is no longer active for the worker, the heartbeat is ignored (returns an empty `worker.heartbeat_ok` result) and nothing is persisted.
   - Stale recovery now restores `draining` when `worker.drain_requested` is set, otherwise `idle`.
   - `_handle_worker_drain()` sets `worker.drain_requested = True` so the drain intent survives a stale period.
   - `_handle_worker_announce()` passes `drain_requested=False` so a fresh announce clears any stale drain intent from a previous session.

2. **`extend/task_relay/hub/models.py`**
   - Added `drain_requested: bool = False` to the `Worker` dataclass.

3. **`extend/task_relay/hub/db.py`**
   - Added `drain_requested INTEGER DEFAULT 0` to the `workers` table schema.
   - Added migration in `_migrate()` to add `drain_requested` to existing databases (with fallback for older SQLite builds).

4. **`extend/task_relay/hub/worker_registry.py`**
   - `announce()` accepts a new `drain_requested: bool = False` keyword argument and persists it on the worker row.

### Tests added

- `extend/task_relay/tests/test_final_rereview_fixes.py::test_heartbeat_from_superseded_session_ignored` — a second `worker.announce` replaces the active session; a heartbeat on the old connection does not update `last_seen_at` or status, while a heartbeat on the new session recovers normally.
- `extend/task_relay/tests/test_final_rereview_fixes.py::test_heartbeat_recover_stale_draining_worker` — a worker that was `draining`, then marked `stale`, recovers to `draining` when heartbeats resume.

### Verification

```bash
cd /Users/suyanlong/github/hermes-agent
.venv/bin/python -m pytest extend/task_relay/tests/ -v
```

Result: **197 passed in 30.17s** (pristine output, no warnings/failures).


## Fix Report: Final Whole-Branch Review Important Findings

**Date:** 2026-07-30  
**Commit:** `27b47b7a2` — `fix(task-relay): target cancel to current session and use deterministic timeout marker`

### Finding 1: Cancel push bypasses session ownership

- **Problem:** `WsHubServer.push_cancel()` and the per-session `_cancel_monitor_loop()` selected the target socket by `worker_id` only. If a stale connection was still in `_sessions`, the `task.cancel` frame could be delivered to the wrong socket.
- **Fix:**
  - `push_cancel()` now looks up the worker's `online_session_id` and sends only to the session whose `session_id` matches.
  - `_cancel_monitor_loop()` exits early (and stops pushing) when the session is no longer the active one for the worker, using the existing `_is_current_session_for_worker()` helper.
- **Files touched:** `extend/task_relay/hub/ws_server.py`
- **New tests:** `extend/task_relay/tests/test_cancel_session.py::test_push_cancel_targets_current_session`, `test_cancel_monitor_loop_targets_current_session`

### Finding 2: Timeout attribution is fragile between Hub and worker

- **Problem:** The Hub used `task.cancel_reason == "timeout"` while `AcpTaskBackend` checked `"timeout" in reason.lower()`. A master cancel with a reason containing "timeout" would be settled as `failed` by the worker even though the Hub intended `cancelled`.
- **Fix:**
  - Introduced a dedicated marker constant `CANCEL_REASON_TIMEOUT = "__timeout__"` in `extend/task_relay/constants.py`.
  - Hub stores and delivers the marker for execution/lease timeout cancels (`task_router.py`, `ws_server.py`).
  - Worker (`AcpTaskBackend`) now checks exact equality (`reason == CANCEL_REASON_TIMEOUT`) instead of substring matching.
- **Files touched:**
  - `extend/task_relay/constants.py` (new)
  - `extend/task_relay/hub/task_router.py`
  - `extend/task_relay/hub/ws_server.py`
  - `extend/task_relay/worker/backends/acp_backend.py`
- **Tests updated/added:**
  - `extend/task_relay/tests/test_cancel.py::test_execution_timeout_marker_settles_failed`
  - `extend/task_relay/tests/test_cancel.py::test_cancel_reason_containing_timeout_is_not_failed`
  - `extend/task_relay/tests/test_rereview_fixes.py::test_execution_timeout_sets_cancel_reason_timeout`
  - `extend/task_relay/tests/test_rereview_fixes.py::test_master_cancel_during_timeout_cancelling_settles_cancelled`
  - `extend/task_relay/tests/test_task_router.py::test_execution_lease_timeout_enters_cancelling_with_timeout_reason`

### Verification

```bash
cd /Users/suyanlong/github/hermes-agent
.venv/bin/python -m pytest extend/task_relay/tests/ -v
```

Result: **200 passed in 31.88s**.


## Fix Report: Final Whole-Branch Review — Remaining Important Findings

**Date:** 2026-07-31  
**Commit:** See git log for SHA (`git log --oneline -1`).

### Finding 1: Redispatch gated on stored `task.allow_redispatch` flag

- **Problem:** `_handle_existing()` required both the current request's `allow_redispatch` *and* the stored `task.allow_redispatch` to reopen a terminal task. The stored flag was also never updated, so a later dispatch could not change redispatchability.
- **Fix:**
  - Redispatch decision now uses only the current request's `allow_redispatch`.
  - On every terminal-task redispatch, the stored `task.allow_redispatch` is updated to the requested value and persisted.
- **Files touched:** `extend/task_relay/hub/task_router.py`
- **New tests:**
  - `extend/task_relay/tests/test_whole_branch_fixes.py::test_redispatch_uses_current_request_allow_redispatch`
  - `extend/task_relay/tests/test_whole_branch_fixes.py::test_redispatch_updates_stored_allow_redispatch_flag`

### Finding 2: `uv.lock` lost platform wheels for `greenlet`

- **Problem:** `s390x` and `riscv64` Linux wheels for `greenlet==3.5.3` were missing from the lockfile.
- **Fix:**
  - Ran `uv lock` in `/Users/suyanlong/github/hermes-agent`; it did not regenerate the missing wheels (host platform does not produce them).
  - Manually added the six missing wheels (cp311/cp312/cp313 × s390x/riscv64) with verified hashes, sizes, and upload times from PyPI.
- **Files touched:** `uv.lock`
- **Verification:** `uv lock` accepts the additions (no further diff).

### Finding 3: `TaskExecutor` drops fallback completions after send failure

- **Problem:** `_complete_once()` set `_completion_state = "pending"` before the network send; if `task.complete` raised, `completion_attempted` was true, so `TaskWorker._execute_one()` dropped its fallback failure completion.
- **Fix:**
  - Separated "attempted/sent/dropped/failed" states.
  - `completion_attempted` is now true only for `sent` or `dropped`.
  - A send failure sets state to `failed`, allowing one retry/fallback completion.
- **Files touched:** `extend/task_relay/worker/task_executor.py`
- **Tests updated/added:**
  - `extend/task_relay/tests/test_worker.py::test_executor_allows_retry_after_send_failure` (updated)
  - `extend/task_relay/tests/test_worker.py::test_worker_sends_fallback_complete_after_send_failure` (updated)
  - `extend/task_relay/tests/test_whole_branch_fixes.py::test_executor_allows_retry_after_send_failure`
  - `extend/task_relay/tests/test_whole_branch_fixes.py::test_worker_sends_fallback_complete_after_send_failure`

### Finding 4: Lease-expiry requeue emits only PROGRESS, not STATUS pending

- **Problem:** When `_settle_lost()` or `_settle_failed()` requeued a task because attempts remained, only a PROGRESS frame was emitted. Masters watching the task stream therefore did not see the state transition back to `pending`.
- **Fix:** Both requeue paths now emit a `STATUS pending` event after the PROGRESS frame.
- **Files touched:** `extend/task_relay/hub/task_router.py`
- **New tests:**
  - `extend/task_relay/tests/test_whole_branch_fixes.py::test_lost_requeue_emits_status_pending_event`
  - `extend/task_relay/tests/test_whole_branch_fixes.py::test_failed_requeue_emits_status_pending_event`

### Verification

```bash
cd /Users/suyanlong/github/hermes-agent
.venv/bin/python -m pytest extend/task_relay/tests/ -v
```

Result: **206 passed in 36.16s** (pristine output, no warnings/failures).
