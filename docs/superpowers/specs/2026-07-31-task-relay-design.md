# Task Relay — Distributed Multi-Agent Task Dispatch System

**Date:** 2026-07-31
**Revised:** 2026-07-30
**Status:** Draft (rev 8) — complete design for remote/heterogeneous multi-agent dispatch
**Author:** Hermes Agent Team

## Overview

Task Relay connects a **Master Agent** (Go/Rust) with multiple **Sub-Agents**
(Python Hermes) through a **Relay Hub**, so a master can decompose work,
dispatch sub-tasks across heterogeneous networks (NAT, firewalls, different
clouds), collect results asynchronously, and continue its own workflow.

```
Master Agent (Go/Rust)  ←→  Relay Hub  ←→  Sub-Agent (Python Hermes)
  Orchestrator              Task bus          Executor
  (decide / join)           (route / store)   (run / report)
```

### Design Goals

1. **Cross-network delivery** — Sub-Agents need no public IP; Hub is the only
   inbound-facing service Master and Workers must reach.
2. **Asynchronous join** — Master dispatches and continues later via streamed
   events or poll; process restarts must not lose terminal results.
3. **Heterogeneous executors** — Workers differ in OS, tools, resources, and
   online pattern (always-on vs intermittent).
4. **Reuse Hermes execution** — Sub-Agent runs through ACP / AIAgent isolation,
   not a third agent runtime.
5. **At-least-once with idempotency** — Networks drop; duplicates are expected
   and safe.

### Non-Goals (of Task Relay itself)

- Replacing same-process `delegate_task` (in-process fork-join stays local).
- Replacing Kanban for long-lived human-in-the-loop boards on one install.
- Being a general workflow engine for non-agent jobs (CI, ETL, etc.).
- Guaranteeing perfect mid-turn resume of arbitrary tool side-effects
  (browser cookies, half-written files) without worker-defined resume logic.

### Relationship to Existing Hermes Primitives

| Primitive | Scope | Relationship |
|---|---|---|
| `delegate_task` | Same-process fork-join | **Semantic reference** — `TaskSpec` borrows `goal` / `context` from `delegate_task`'s signature (`tools/delegate_tool.py`). `toolsets` and `timeout_seconds` are *deliberately promoted to explicit fields* here: in-process `delegate_task` derives toolsets from the parent agent and reads timeout from `delegation.child_timeout_seconds` config, but Task Relay crosses trust boundaries so a remote worker must not inherit the parent's toolset and each task must carry its own deadline. Summary extraction reuses the delegation pipeline |
| Kanban | Durable board in one `HERMES_HOME` cluster | **Sibling** — Kanban coordinates named profiles locally; Task Relay crosses networks. A future bridge may let Kanban claim remote workers via Hub |
| ACP (`acp_adapter/`) | External turn/session API | **Execution backend** — Worker calls ACP session create + prompt submit for each task |
| Gateway relay wake | Gateway sleep/wake | **Connectivity pattern inspired** — reuses the outbound-only design from `docs/relay-connector-contract.md`. The wake *token* is independently designed: gateway relay's wake poke is an unsigned payload-free GET, and its HMAC-SHA256 lives on the WS upgrade handshake as a per-gateway secret (not single-use); Task Relay needs a per-task, short-lived, single-use credential, so it defines its own |

---

## Responsibility Split

This split is load-bearing. Features that blur it must state which side owns
the decision.

| Concern | Owner | Hub role |
|---|---|---|
| Task decomposition / next-step planning | **Master** | Pass-through of `goal` / `context` / `params` |
| Which workers may run a task (ACL) | **Master** (declares) | Enforces `allowed_worker_ids` / `deny_worker_ids` |
| Routing / claim / lease / delivery | **Hub** | Core |
| Result persistence + event replay | **Hub** | Core |
| Join policy (wait all / any / threshold) | **Master** (primary) | Optional Hub `BatchPolicy` helper (see Orchestration) |
| Dependency DAG | **Master** (primary) | Optional Hub `depends_on` scheduler (see Orchestration) |
| Final aggregation into Master context | **Master** | Optional Hub pre-aggregate event (convenience only) |
| Tool execution / model calls | **Sub-Agent** | Opaque |
| Checkpoint emission / resume interpretation | **Sub-Agent** | Stores opaque resume blob; does not interpret it |

**Rule:** Hub never invents goals, never mutates `params`, and never decides
“what the Master should do next.” Hub may enforce declared constraints and
emit structured events that make Master join easier.

---

## Architecture

### Topology

```
┌──────────────────────┐     gRPC (protobuf)       ┌─────────────────────┐
│    Master Agent      │◄─────────────────────────►│     Relay Hub       │
│    (Go/Rust)         │  Dispatch / Watch / List  │  gRPC + WS + store  │
│                      │  Cancel / GetResult       │  router + registry  │
│  decompose → dispatch│                           │  events + auth      │
│  watch → aggregate   │                           └──────────┬──────────┘
│  → continue          │                                      │
└──────────────────────┘                         WS JSON-RPC (worker-outbound)
                                                              │
                                           ┌──────────────────┼──────────────┐
                                           │                  │              │
                                      ┌────▼────┐        ┌────▼────┐        ...
                                      │Agent-01 │        │Agent-02 │
                                      │ Hermes  │        │ Hermes  │
                                      │ NAT OK  │        │ NAT OK  │
                                      └─────────┘        └─────────┘
```

### Components

| Component | Language | Role |
|---|---|---|
| **Master Agent** | Go / Rust | Decompose, dispatch (single/batch), subscribe to events, aggregate, continue |
| **Relay Hub** | Python first; Go port for scale | Route, claim, lease, wake, persist, replay, optional orchestration helpers |
| **Sub-Agent Worker** | Python (Hermes) | Connect outbound, claim work, execute via ACP, report progress/checkpoints/results |

### Capability Modules (design layers, not a build order)

The system is one protocol with three capability modules. All are part of the
complete design. Implementations may ship them in any order later; the design
requires them to compose cleanly.

| Module | Contents |
|---|---|
| **M1 — Delivery Core** | TaskSpec/Result/Event, Dispatch, Watch replay, Cancel, ListWorkers, poll/claim, idempotency, timeouts, ACP backend, **worker JWT + Master→Hub JWT auth (base security)** |
| **M2 — Connection & Continuity** | Mode A/B/C sessions, heartbeat, checkpoint store, resume contract, ContextRef |
| **M3 — Orchestration & Hardening** | BatchPolicy, `depends_on`, Hub pre-aggregate, resource-aware schedule, mTLS, TraceContext, metrics |

---

## Connection Modes

Every worker advertises `session_modes` (a capability set) and optional
`wake_url`. Hub picks the best delivery path per worker. Modes are
**capabilities**, not mutually exclusive deployments: a worker may support A+C,
or A+B, etc. **Mode A is mandatory** for all workers (fallback when long session
or wake is unavailable), so `session_modes` always contains `A`.

### Mode Matrix

| Mode | Worker requirement | Hub behavior | Typical use |
|---|---|---|---|
| **A — Poll** | Outbound WS only | Long-poll returns up to `max_tasks` claimed tasks | Intermittent, battery, metered, NAT |
| **B — HTTP wake** | Reachable `wake_url` + Mode A | POST wake → worker opens WS → claim | Worker has inbound HTTP (VPN/public) |
| **C — Long session** | Sustained outbound WS | Push `task.run` within worker-granted credit | Always-on remote agents behind NAT |

**Selection order when dispatching to a specific worker:**

1. If worker has a healthy Mode C session with credit available → push on session.
2. Else if worker has `wake_url` and Mode B enabled → HTTP wake, then claim.
3. Else → leave `pending` for Mode A poll.

**NAT rule:** Prefer Mode C over Mode B. Mode B must never be required for
NATted agents. Because Mode A is mandatory for every worker, a wake failure
always has a poll fallback: the task stays `pending` and is **not** marked
`lost` on wake failure alone (it can still expire via
`queue_timeout_seconds`). There is no separate `can_poll` capability flag —
poll support is universal by definition.

### Mode A — Poll (mandatory)

```
loop:
  WS connect (outbound)
  worker.announce {session_modes:["a"], max_concurrent, …}
  worker.poll {max_wait_ms, max_tasks}     // Hub may hold ≤ 60s
  ← empty → close → backoff → retry
  ← 1..max_tasks claimed tasks (atomic by default; see Poll Protocol)
  → execute concurrently up to max_concurrent
    task.progress* / task.checkpoint* / task.complete   (keyed by task_id)
  WS close (or poll again on the same socket)
```

### Mode B — HTTP Wake (optional)

```
Hub POST wake_url {task_id, relay_url, token, expires_at}
Worker 202 Accepted
Worker WS connect → worker.claim {wake_token}
← task.run → … → task.complete → WS close
```

The single-use wake token is independently designed for Task Relay. It is
*not* a reuse of `docs/relay-connector-contract.md`'s wake poke (which is an
unsigned, payload-free GET). Gateway relay's HMAC-SHA256 exists only on the WS
upgrade handshake and is a per-gateway secret, not single-use; Task Relay needs
a per-task, short-lived, single-use credential bound to the wake, so it defines
its own token. Only the outbound-only connectivity model is shared.

### Mode C — Long Session (recommended for always-on remote)

```
worker.announce {session_modes:["a","c"], capabilities, resources,
                 max_concurrent, credit: N}
← worker.announce_ok {session_id, heartbeat_interval_ms}
loop:
  ← task.run (only while credit > 0) | task.cancel
  → task.progress | task.checkpoint | task.complete
  → worker.credit {available}        // refresh capacity, or 0 to pause intake
  → worker.heartbeat
on transport error:
  reconnect; Hub reclaims stale running tasks per lease rules;
  worker may resume via resume_from_checkpoint if present
on shutdown:
  worker.drain → finish running tasks → worker.close
```

Delivery is **push-only within credit**; there is no Hub→worker "poll now"
frame (see Mode C Delivery Is Push-Only). Missing two expected heartbeats marks
the session `stale`.

---

## Protocol

### gRPC Service (Master ↔ Hub)

```protobuf
service TaskRelay {
  rpc DispatchTask(DispatchTaskRequest) returns (DispatchTaskResponse);
  rpc DispatchTaskBatch(DispatchTaskBatchRequest) returns (DispatchTaskBatchResponse);
  rpc GetTaskResult(TaskResultRequest) returns (TaskResult);
  rpc WatchTask(WatchTaskRequest) returns (stream TaskEvent);
  rpc ListWorkers(ListWorkersRequest) returns (ListWorkersResponse);
  rpc ListTasks(ListTasksRequest) returns (ListTasksResponse);
  rpc CancelTask(CancelTaskRequest) returns (CancelTaskResponse);
}
```

### Dispatch Messages

```protobuf
message DispatchTaskRequest {
  TaskSpec spec = 1;
  string master_session_id = 2;
  bool allow_redispatch = 3;   // reopen lost/failed for retry
}

message DispatchTaskResponse {
  string task_id = 1;
  string batch_id = 2;            // empty for single dispatch
  string callback_topic = 3;
  TaskStatus status = 4;          // the task's ACTUAL current status
  bool idempotent_hit = 5;        // true when this task_id already existed
  TaskResult existing_result = 6; // set when idempotent_hit and status is terminal
  int32 attempt = 7;              // attempts consumed so far
}

message DispatchTaskBatchRequest {
  string batch_id = 1;         // Master-generated, globally unique
  repeated TaskSpec specs = 2;
  string master_session_id = 3;
  string callback_topic = 4;   // shared topic for the batch
  bool allow_redispatch = 5;
  BatchPolicy policy = 6;      // optional Hub-side join helper
}

message DispatchTaskBatchResponse {
  string batch_id = 1;
  string callback_topic = 2;
  repeated DispatchTaskResponse tasks = 3;
  bool idempotent_hit = 4;     // true when this batch_id already existed
}
```

`status` always reports the task's real state (`pending`, `running`, or a
terminal value). There is no synthetic `already_terminal` state — "was this a
duplicate submit?" is answered by `idempotent_hit`, which is orthogonal to
status.

### TaskSpec

Aligned with `delegate_task` (`goal`, `context`, `toolsets`, `timeout_seconds`).

```protobuf
message TaskSpec {
  string task_id = 1;                 // Master-generated, globally unique
  string goal = 2;
  map<string, string> params = 3;     // e.g. params["model"], params["structured_output"]
  ContextPayload context = 4;
  repeated string toolsets = 5;
  string target_worker = 6;           // optional hard pin
  int32 timeout_seconds = 7;          // execution timeout
  string callback_topic = 8;          // optional; batch may share one
  int32 priority = 9;                 // higher = sooner
  repeated string depends_on = 10;    // optional Hub DAG edges
  string aggregate_key = 11;          // optional Hub pre-aggregate group
  ResourceRequirements min_resources = 12;
  TraceContext trace_context = 13;
  repeated string allowed_worker_ids = 14;
  repeated string deny_worker_ids = 15;
  reserved 16;                        // was resume_from_checkpoint (moved to task.run)
  int32 queue_timeout_seconds = 17;   // max time in pending before lost (default 900)
  int32 max_attempts = 18;            // total execution attempts allowed (default 1)
  int32 first_progress_seconds = 19;  // deadline for first progress/checkpoint (default 120; worker emits an immediate claim-ack progress frame, see Lease)
}

message ResourceRequirements {
  // Hard gate, not a preference. A worker missing any specified field is
  // treated as unsatisfied (excluded), never best-effort. Unsatisfiable
  // min_resources → permanent pending until queue_timeout_seconds → lost.
  int32 min_cpu_cores = 1;
  int32 min_memory_gb = 2;
  bool requires_gpu = 3;
  repeated string required_network_profiles = 4; // e.g. "unmetered"
}

message TraceContext {
  string trace_id = 1;
  string span_id = 2;
  string parent_span_id = 3;
  bool sampled = 4;
}

message ContextPayload {
  oneof payload {
    string inline = 1;           // plaintext; recommend < 64 KiB
    InlineGzip inline_gzip = 2;  // compressed, still delivered in-band
    ContextRef ref = 3;          // out-of-band; worker fetches
  }
}

message InlineGzip {
  bytes gzip_data = 1;
  string sha256 = 2;             // over the DECOMPRESSED plaintext
}

message ContextRef {
  string uri = 1;                // https:// presigned, s3://, …
  string sha256 = 2;             // over the DECOMPRESSED plaintext
  string content_encoding = 3;   // "" | "gzip" — encoding of the fetched bytes
}
```

**Field placement rules.**

- `resume_from_checkpoint` is deliberately **not** in `TaskSpec`. It is
  Hub-computed, worker-facing state carried only in the `task.run` payload, so a
  Master's dispatch input never competes with Hub-managed resume bookkeeping.
- Inline compression is its own oneof arm (`inline_gzip`), not a field of
  `ContextRef`. A "ref" always means *fetch it yourself*; anything delivered
  in-band stays out of `ContextRef`.
- `sha256` is always computed over the **final plaintext** context the worker
  feeds into the prompt — never over compressed or transport-encoded bytes.
  Worker order is decode → verify → execute.

### TaskEvent & TaskResult

`WatchTask` streams **events**. Terminal payload lives in `TaskResult`.

```protobuf
message TaskEvent {
  int64 event_id = 1;             // globally monotonic in the Hub (see Cursor semantics)
  int64 event_at = 2;             // unix ms
  string task_id = 3;
  string batch_id = 4;
  TaskEventKind kind = 5;
  TaskResult result = 6;
  string progress_summary = 7;
  TaskCheckpoint checkpoint = 8;
  AggregateResult aggregate = 9;  // kind=AGGREGATE only
  TraceContext trace_context = 10;
}

// AGGREGATE events describe a GROUP, not a task. task_id is empty on them.
message AggregateResult {
  string batch_id = 1;
  string aggregate_key = 2;
  repeated string task_ids = 3;          // members, dispatch order
  map<string, int32> status_counts = 4;  // TaskStatus name → count
  string summary = 5;                    // member summaries joined in task_ids order
  repeated Metric metrics = 6;           // concatenation of member fields.metrics, each stamped with origin_task_id
  int32 schema_version = 7;
}

enum TaskEventKind {
  TASK_EVENT_KIND_UNSPECIFIED = 0;
  TASK_EVENT_KIND_STATUS = 1;
  TASK_EVENT_KIND_PROGRESS = 2;
  TASK_EVENT_KIND_TERMINAL = 3;
  TASK_EVENT_KIND_CHECKPOINT = 4;
  TASK_EVENT_KIND_AGGREGATE = 5;
}

message TaskCheckpoint {
  string task_id = 1;
  string checkpoint_id = 2;       // worker-generated dedupe key
  int64 event_id = 3;             // Hub-assigned after persist
  int64 checkpoint_at = 4;
  string summary = 5;
  TaskFields fields = 6;          // structured partial output for Master
  bytes resume_blob = 7;          // opaque; worker-defined only; capped at resume_blob_max_bytes (default 1 MiB)
  int64 lease_until = 8;
}

message BatchPolicy {
  enum CompletionMode {
    COMPLETION_MODE_UNSPECIFIED = 0;
    COMPLETION_MODE_ALL = 1;
    COMPLETION_MODE_ANY = 2;
    COMPLETION_MODE_MAJORITY = 3;
    COMPLETION_MODE_THRESHOLD = 4;
  }
  CompletionMode completion_mode = 1;
  int32 success_threshold = 2;
  int64 batch_timeout_ms = 3;
  bool fail_fast = 4;
}

enum TaskStatus {
  TASK_STATUS_UNSPECIFIED = 0;
  TASK_STATUS_PENDING = 1;
  TASK_STATUS_RUNNING = 2;
  TASK_STATUS_COMPLETED = 3;
  TASK_STATUS_FAILED = 4;
  TASK_STATUS_LOST = 5;
  TASK_STATUS_CANCELLED = 6;
}

message TaskResult {
  string task_id = 1;
  TaskStatus status = 2;
  string summary = 3;
  string result_text = 4;
  TaskFields fields = 5;
  string error = 6;
  TaskUsage usage = 7;
  int64 started_at = 8;
  int64 completed_at = 9;
  string worker_id = 10;
  int32 schema_version = 11;
  string batch_id = 12;
  string latest_checkpoint_id = 13;
  int32 attempt = 14;              // 1-based attempt that produced this result
  int32 max_attempts = 15;         // effective limit for this task
}

message TaskUsage {
  int32 prompt_tokens = 1;
  int32 completion_tokens = 2;
  int32 total_tokens = 3;
  int32 api_calls = 4;             // model round-trips inside the run
  int32 tool_calls = 5;
  double wall_seconds = 6;
  double cost_usd = 7;             // 0 when the worker cannot attribute cost
  string model = 8;                // model actually used by the worker
}

message TaskFields {
  int32 version = 1;
  repeated Metric metrics = 2;
  repeated KeyValue tags = 3;
  string report = 4;
  map<string, bytes> extensions = 15;
}

message Metric {
  string name = 1;
  double value = 2;
  string unit = 3;
  string description = 4;
  string origin_task_id = 5;   // populated by Hub when aggregating into AggregateResult.metrics; empty on per-task metrics
}

message KeyValue {
  string key = 1;
  string value = 2;
}
```

### WatchTask Replay

```protobuf
message WatchTaskRequest {
  oneof filter {
    string topic = 1;
    string batch_id = 2;
    string task_id = 3;
  }
  int64 since_event_id = 4;   // 0 = from oldest retained; reconnect uses last seen
}
```

**Cursor semantics.** `event_id` is assigned from a single **globally
monotonic** sequence in the Hub, not a per-topic counter. Any filtered view is
therefore an increasing subsequence, and a Master compares cursors only within
the stream it subscribed to. Implementations (Python, Go, Postgres) MUST keep
one global sequence; per-topic numbering is non-conformant because it would
make cursors ambiguous across filters.

**HA scope.** A single Hub instance (M1) owns the global sequence — typically
`AUTOINCREMENT`/`SEQUENCE` on the `task_events` table, which is correct and
uncontended at single-instance scale. A multi-Hub HA deployment turns this
sequence into a write-contention point: the M1 design does **not** specify a
partitioned or hybrid sequence (e.g. shard-local counters reconciled by a
global coordinator, or a snowflake-style hybrid logical clock). HA sequence
strategy is deferred to M3 and MUST preserve the contract above (globally
monotonic, comparable across filters) — a naive per-shard counter that breaks
cross-filter cursor comparison is non-conformant.

Hub replays `event_id > since_event_id`, then live-streams. Events are retained
for `retention_days` (default 7).

**Cursor expiry is an explicit error, never silent loss.** If
`since_event_id` is older than the oldest retained event for the requested
filter, Hub fails the stream immediately with gRPC `FAILED_PRECONDITION` and a
`CursorOutOfRange` detail:

```protobuf
message CursorOutOfRange {
  int64 requested_since_event_id = 1;
  int64 oldest_available_event_id = 2;
  int64 newest_event_id = 3;
}
```

The Master's recovery path is to reconcile terminal state via `GetTaskResult` /
`ListTasks` for the tasks it cares about, then resubscribe with
`since_event_id = newest_event_id`. Hub MUST NOT silently start from the oldest
retained event, because that looks identical to "nothing was missed."

**Slow-consumer policy.** A `WatchTask` stream has a bounded per-stream buffer
(`watch_stream_buffer_events`, default 1024). If the Master reads slower than
the Hub produces (gRPC flow control saturates and the buffer fills), the Hub
closes the stream with gRPC `RESOURCE_EXHAUSTED` and a `SlowConsumer` detail
carrying the last `delivered_event_id`. The Master reconciles via
`GetTaskResult` / `ListTasks` for terminal tasks it may have missed, then
resubscribes with `since_event_id = delivered_event_id`. The Hub MUST NOT block
event production on a slow consumer, and MUST NOT drop events silently —
closing the stream with an explicit cursor is the only allowed backpressure
action.

```protobuf
message SlowConsumer {
  int64 delivered_event_id = 1;   // last event successfully sent on this stream
  int64 newest_event_id = 2;      // Hub's current tail; gap = missed range
}
```

### Query & Control Messages

```protobuf
message TaskResultRequest {
  string task_id = 1;
  bool include_latest_checkpoint = 2;
}

message ListTasksRequest {
  string batch_id = 1;                  // optional filter
  string callback_topic = 2;            // optional filter
  string master_session_id = 3;         // optional filter
  repeated TaskStatus statuses = 4;     // optional filter; empty = any
  string worker_id = 5;                 // optional filter
  int32 limit = 6;                      // default 100, max 500; Hub clamps values above list_tasks_max_limit (default 500)
  string page_token = 7;
}

message ListTasksResponse {
  repeated TaskResult tasks = 1;
  string next_page_token = 2;
}

message ListWorkersRequest {
  repeated string require_toolsets = 1;         // worker must have all of these
  ResourceRequirements require_resources = 2;   // optional minimum profile
  bool only_schedulable = 3;                    // exclude offline/stale/draining
}

message ListWorkersResponse {
  repeated WorkerInfo workers = 1;
}

message WorkerInfo {
  string worker_id = 1;
  string status = 2;                 // offline | idle | busy | stale | draining
  repeated SessionMode session_modes = 3;  // capability set; A always present (mandatory)
  repeated string toolsets = 4;
  string os = 5;
  string arch = 6;
  string region = 7;
  repeated string resume_formats = 8;
  WorkerResources resources = 9;
  WorkerLoad load = 10;
  int32 max_concurrent = 11;
  int32 running_tasks = 12;
  int64 last_announce_at = 13;
  int64 last_heartbeat_at = 14;
  bool wake_url_present = 15;        // Mode B availability, URL itself not exposed
}

enum SessionMode {
  SESSION_MODE_UNSPECIFIED = 0;
  SESSION_MODE_A = 1;                // poll (mandatory for every worker)
  SESSION_MODE_B = 2;                // HTTP wake (requires wake_url + A)
  SESSION_MODE_C = 3;                // long session push (requires sustained WS)
}

message WorkerResources {
  int32 cpu_cores = 1;
  int32 memory_gb = 2;
  int32 gpu_count = 3;
  string gpu_model = 4;
  int32 disk_gb = 5;
  string network_profile = 6;        // e.g. "unmetered"
}

message WorkerLoad {
  int32 running_tasks = 1;
  double cpu_percent = 2;
  double memory_percent = 3;
}

message CancelTaskRequest {
  string task_id = 1;
  string batch_id = 2;               // optional: cancel every non-terminal task in batch
  string reason = 3;
  int32 grace_seconds = 4;           // overrides Hub cancel_grace_seconds (default 60)
}

message CancelTaskResponse {
  repeated string cancelled_task_ids = 1;
  repeated string already_terminal_task_ids = 2;
}
```

`WorkerInfo` is the same projection the Hub scheduler uses, so a Master doing
manual `target_worker` selection sees exactly what auto-select would score.

### Schema Compatibility

- New fields are optional with defaults.
- Never change existing field numbers, types, or semantics.
- Experimental data goes in `TaskFields.extensions`.
- Masters branch on `schema_version`; unknown versions degrade to `summary`.

---

## Worker ↔ Hub (WebSocket JSON-RPC)

### Methods

Connection setup is one method for both modes; `session_modes` advertises the
delivery model.

```
→ worker.announce          {worker_id, session_modes:["a","c"],
                            wake_url?, capabilities, resources?, load?,
                            max_concurrent, credit?}
                            // auth: Hub-issued worker JWT as bearer on the
                            // WS upgrade (NOT in the announce body)
← worker.announce_ok       {session_id, heartbeat_interval_ms, server_time}
```

Task acquisition (Mode A / short sessions — worker pulls):

```
→ worker.poll              {max_wait_ms, max_tasks, prefer_atomic_claim?}
← worker.poll_result       {offered:false} | {offered:true, tasks:[…]}
→ worker.claim             {task_id, claim_token|wake_token}   // two-step mode only
```

Task delivery (Mode C / long sessions — Hub pushes within credit):

```
← task.run                 (pushed; consumes one credit)
→ worker.credit            {available}                          // grant/refresh credit
```

Execution and lifecycle (both modes):

```
← task.run                 {task_id, attempt, goal, params, context, toolsets,
                            timeout_seconds, first_progress_seconds,
                            trace_context, resume_from_checkpoint?, resume_blob?}
→ task.progress            {task_id, summary}
→ task.checkpoint          {task_id, checkpoint_id, summary, fields, resume_blob, lease_until}
← checkpoint.ack           {event_id, checkpoint_id, lease_until}
→ task.complete            {task_id, status, summary, fields, usage, result_text}
← task.cancel              {task_id, reason, hard_deadline_at}
→ cancel.ack               {task_id, accepted, in_flight_tool?, will_settle_by}
← worker.nack              {task_id, reason}
→ worker.heartbeat         {}
← worker.heartbeat_ok      {}
→ worker.drain             {reason, finish_running:true|false}
← worker.drain_ok          {running_task_ids}
→ worker.close             {}
```

### Concurrency Model (`max_concurrent`)

`max_concurrent` is honored in **both** modes; the mechanism differs.

- **Mode A:** `worker.poll` carries `max_tasks`. Hub returns up to
  `min(max_tasks, max_concurrent - running_tasks)` offers in one
  `worker.poll_result`. A worker that wants strictly sequential behavior sends
  `max_tasks: 1` — that is a configuration choice, not a protocol limit.
  Multiple tasks run over the same socket, interleaving `task.progress` /
  `task.checkpoint` / `task.complete` keyed by `task_id`.
- **Mode C:** credit-based. The worker grants credit via `worker.credit`
  (initially in `worker.announce`); Hub pushes at most that many concurrent
  `task.run` frames. Each push consumes one credit; each terminal
  `task.complete` returns one. A worker sends `worker.credit {available: 0}` to
  stop receiving work without dropping the session.

**Credit return on cancel.** A `task.cancel` that the worker settles as
`cancelled` is terminal, so it returns one credit exactly like a
`task.complete` — the credit was consumed on push and refunded on settlement,
regardless of whether settlement was completion or cancellation. The only path
that does **not** refund credit is a `task.run` push that the worker never
acknowledged (lost on disconnect): that credit is recovered when the Hub marks
the task `lost` and re-enqueues or fails it. This keeps the credit invariant —
`granted = in_flight + available` — preserved across completion, cancellation,
and loss.

Hub never dispatches beyond a worker's advertised capacity in either mode.

### Mode C Delivery Is Push-Only

Mode C is a **push model with worker-granted credit**. There is no
Hub→worker "go poll now" signal, because the Hub can simply push. Earlier
drafts described both a push and a `worker.session.wake` frame; that was
ambiguous and the wake frame is removed.

Consequences:

- Hub holds queued work until credit is available; it does not nudge the worker.
- A worker that prefers pull semantics advertises `session_modes: ["a"]` and
  uses Mode A, even over a long-lived TCP connection.
- Mode B (HTTP wake) remains the only wake mechanism, and it exists solely for
  workers that are reachable inbound.

### Graceful Drain

A worker being redeployed must not manufacture `lost` tasks.

```
→ worker.drain {reason: "deploy", finish_running: true}
← worker.drain_ok {running_task_ids: ["t_abc"]}
```

On `worker.drain`, Hub:

1. Sets worker status to `draining` — excluded from poll offers, credit pushes,
   Mode B wakes, and from `ListWorkers(only_schedulable=true)`.
2. Keeps existing leases valid so running tasks can finish normally.
3. Keeps accepting `task.progress` / `task.checkpoint` / `task.complete`.

With `finish_running: false`, Hub instead pushes `task.cancel` for the worker's
running tasks and marks them `cancelled` on acknowledgement. After the last
task settles the worker sends `worker.close` and disconnects; Hub moves it to
`offline`. A worker that disappears **without** draining is handled by the
normal lease/heartbeat path (`lost`).

### Poll Protocol (Mode A)

**Default: atomic claim-on-poll** (one RTT; preferred on high-latency links).
`tasks` holds up to `min(max_tasks, max_concurrent - running_tasks)` entries.

```json
{
  "result": {
    "offered": true,
    "tasks": [
      {
        "claimed": true,
        "task_id": "t_abc",
        "attempt": 1,
        "claim_token": "ctok_...",
        "claim_expires_at": 1710000030,
        "run": {
          "goal": "...",
          "params": {},
          "context": {},
          "toolsets": [],
          "timeout_seconds": 600,
          "first_progress_seconds": 120,
          "resume_from_checkpoint": null
        }
      }
    ]
  }
}
```

Hub atomically moves each returned task `pending → running` before responding.
The worker starts execution immediately; no separate `worker.claim`.

**Optional two-step offer** (`prefer_atomic_claim: false`): entries carry
`claimed: false`, a `claim_token`, and a `preview` instead of `run`. The worker
must `worker.claim` before `claim_expires_at` (default 30s). Concurrent
claimants: first wins, others get `worker.nack`.

```json
{
  "claimed": false,
  "task_id": "t_abc",
  "claim_token": "ctok_...",
  "claim_expires_at": 1710000030,
  "preview": {
    "goal_excerpt": "analyze nginx 5xx on web-1",
    "toolsets": ["terminal", "file"],
    "priority": 0,
    "attempt": 1,
    "timeout_seconds": 600,
    "context_bytes": 8192,
    "min_resources": {"min_memory_gb": 2},
    "has_resume_checkpoint": false
  }
}
```

`preview` is metadata only — never the full `context` or `params` — so a worker
can decline on cost/capability grounds without receiving payload it may not be
authorized to read.

**Empty poll:**

```json
{"result": {"offered": false}}
```

Worker reconnects; exponential backoff 1s → 30s on repeated empties / errors.

**Lease:** After claim (atomic or two-step) the worker must send
`task.progress`, `task.checkpoint`, or `task.complete` within
`first_progress_seconds` (default 120), and thereafter keep the lease alive with
progress or checkpoints until `timeout_seconds`. Missing the first-progress
deadline marks the task `lost` quickly instead of holding it for the full
execution timeout. On expiry → `lost` (or a new attempt if attempts remain).

**Immediate claim-ack progress frame (required):** Upon successful claim the
worker MUST emit a `task.progress` frame immediately and before the first model
round-trip — e.g. `{"task_id": "...", "summary": "claimed, starting ACP session"}`.
This clears the first-progress deadline independently of ACP cold-start latency,
which can exceed 60s on slow models or cold caches. The frame carries no payload
beyond an acknowledging summary; subsequent progress frames carry real partial
output. Without this rule a tight `first_progress_seconds` would mark a
legitimately-running cold-start task `lost` prematurely. The 120s default is a
backstop for workers that fail to emit the immediate frame; correct workers
satisfy the deadline in well under 1s.

**Cancel while polling:** Hub may push `task.cancel` on the open poll WS for a
task the worker was about to receive / just claimed; worker aborts.

### Wake (Mode B)

```
POST {wake_url}
Content-Type: application/json

{
  "task_id": "t_abc",
  "relay_url": "wss://relay-hub:9000/ws/worker",
  "token": "single-use-hmac-token",
  "expires_at": 1710000060
}
```

Worker responds `202`, opens WS, `worker.claim` with `wake_token`.

---

## Idempotency & Delivery Semantics

End-to-end delivery is **at-least-once**. All parties dedupe.

### Dispatch

| Existing state | `allow_redispatch=false` | `allow_redispatch=true` |
|---|---|---|
| not found | create `pending` | create `pending` |
| `pending` / `running` | ACK same (no double enqueue) | ACK same |
| `completed` | return `existing_result` | return `existing_result` |
| `failed` / `lost` / `cancelled` | return terminal result | reset `pending` **if attempts remain**, re-enqueue; attach `resume_from_checkpoint` if stored |

- Idempotency keys: `task_id` (single), `batch_id` (batch).
- Every response sets `idempotent_hit` so a Master can distinguish "created" from
  "already existed" without inspecting status.
- Hub never runs two concurrent executions for the same `task_id`.
- To force-retry a task stuck in `running` behind a dead worker, the Master
  calls `CancelTask` first, then dispatches with `allow_redispatch=true`.

### Attempts

- `TaskSpec.max_attempts` (default 1) bounds total executions of a `task_id`.
- Hub increments `attempt` on each transition into `running`.
- Redispatch (`allow_redispatch=true`) is refused once
  `attempt >= max_attempts`: the task stays terminal and the response returns
  the last `existing_result` with `attempt` / `max_attempts` populated.
- Lease expiry and requeue consume attempts exactly like an explicit
  redispatch, so a flapping task cannot loop forever.
- `TaskResult.attempt` tells the Master which attempt produced the payload it
  is reading.

### Batch idempotency conflicts

`batch_id` is the batch idempotency key, so Hub stores a canonical hash of the
submitted `specs` (`batch_spec_hash`: sorted `task_id`s plus each spec's
serialized bytes).

| Resubmit shape | Behavior |
|---|---|
| Same `batch_id`, identical specs | Idempotent replay: return the original per-task ACKs with `idempotent_hit=true` |
| Same `batch_id`, **different** specs | Reject the whole request with `ALREADY_EXISTS` and a `BatchSpecMismatch` detail (`existing_hash`, `submitted_hash`); **nothing** is enqueued or mutated |
| Same specs, new `batch_id` | Treated as a new batch; per-task `task_id` idempotency still applies individually |

Rejecting the mismatch is deliberate: silently honoring the first submission
would make a Master believe its edited batch was accepted.

### Completion & Checkpoint

- `task.complete`: idempotent; terminal status is **monotonic**
  (`completed` never reverts to `running`). First terminal payload wins;
  conflicting duplicates are logged and ignored.
- `task.checkpoint`: idempotent on `(task_id, checkpoint_id)`; first wins;
  Hub assigns `event_id` and ACKs.

### Watch consumption

- Master stores `last_event_id` per topic.
- Reconnect with `since_event_id = last_event_id`.
- Terminal and checkpoint events remain within retention.

### Hub crash recovery

| Pre-crash state | After restart |
|---|---|
| `pending` | still dispatchable |
| `running` past lease | `lost`, or requeue if `allow_redispatch` |
| terminal | unchanged |

---

## Checkpoint & Resume Contract

Checkpoints have **two layers**. They must not be conflated.

### Layer L1 — Observable partial results (always)

- `summary` + `TaskFields` for Master via `TASK_EVENT_KIND_CHECKPOINT`.
- Extends execution lease.
- Survives Hub restart within retention.
- Master may use L1 to update UI / intermediate planning without waiting for
  terminal completion.

### Layer L2 — Worker resume blob (optional, best-effort)

- `resume_blob` is **opaque to Hub and Master**.
- **Size cap:** `resume_blob` is capped at `resume_blob_max_bytes` (default
  1 MiB, configurable in `config.yaml`). A checkpoint exceeding the cap is
  rejected by the Hub with `INVALID_ARGUMENT`; the worker MUST fall back to a
  `ContextRef` (Hub-stored, addressed by digest) for large state and emit only
  a small pointer-bearing `resume_blob`. This protects the `checkpoints` table
  and the `task.run` payload from multi-MB blobs on every checkpoint.
- Only the **same worker software** that wrote it may interpret it.
- On redispatch, Hub sets `resume_from_checkpoint` (and the stored
  `resume_blob`) **in the `task.run` payload only** — never in `TaskSpec`, which
  stays purely Master-authored.
- Worker MAY continue, MAY ignore and restart, or MAY fail closed if blob
  version mismatches.

### What is NOT guaranteed

- Arbitrary ACP/`AIAgent` mid-turn state restore from an opaque blob.
- Side effects outside the worker (remote APIs, host files) remain correct
  after resume — that is the worker author’s responsibility.
- Cross-worker resume (worker-01 blob on worker-02) unless both agree on a
  shared resume format advertised in `resume_formats`.

### ACP interaction

```
AcpTaskBackend.run(spec):
  if resume_from_checkpoint:
    load resume_blob → backend-specific restore OR start fresh with L1 summary
      injected into prompt as prior progress
  session/create (or reopen if backend supports)
  prompt/submit  // one full agent run until completion (multi tool-call loop OK)
  periodically: emit L1 checkpoint (+ optional L2 blob)
  map stream → task.progress (throttled)
  extract summary + fields → task.complete
```

Default safe behavior when L2 restore is unsupported: **reinject L1 summary
into the new prompt** and rerun. That preserves Master-visible progress
without pretending the conversation memory was restored.

---

## Cancellation & Interrupt Mapping

Cancellation is **cooperative**, and the design says so explicitly because the
underlying execution engine is cooperative: `AIAgent.interrupt()` sets an
interrupt flag that the agent loop checks at iteration boundaries, and ACP's
session `cancel` sets the session cancel event plus calls `agent.interrupt()`.
Neither one kills an in-flight tool call. A `task.cancel` that arrives while the
worker is 4 minutes into a `terminal` command takes effect when that command
returns, not immediately.

Stating this in the protocol matters: `fail_fast`, `CancelTask`, and execution
timeout all ride this path, so their latency is bounded by the current tool
call, not by the network.

### Required worker mapping

A conforming worker MUST map the frame to a real interrupt — never treat it as
advisory:

```
← task.cancel {task_id, reason, hard_deadline_at}
→ cancel.ack  {task_id, accepted, in_flight_tool?, will_settle_by}
   … agent unwinds at its next loop boundary …
→ task.complete {task_id, status: "cancelled", summary, fields, usage, error}
```

| Worker state when cancel arrives | Required behavior |
|---|---|
| Claimed, execution not started | Abort before session create; settle `cancelled` immediately |
| Running under `AcpTaskBackend` | Call ACP session `cancel` (sets cancel event + `agent.interrupt()`); await the run's cancelled return |
| Running under `RemoteAcpTaskBackend` | Send cancel over the JSON-RPC channel; if the child does not settle by `hard_deadline_at`, terminate the child process |
| Already settled | Ignore; the earlier terminal write stands (idempotent) |

The worker SHOULD salvage partial work into the final frame: reuse the L1
checkpoint pipeline so `summary` / `fields` carry what was accomplished before
the interrupt. A cancelled task with a useful partial summary is far more
valuable to a Master than an empty terminal event.

### Grace window and escalation

`task.cancel` carries `hard_deadline_at` derived from
`cancel_grace_seconds` (Hub config default 60s, overridable per request via
`CancelTaskRequest.grace_seconds`).

1. Hub pushes `task.cancel`, expects `cancel.ack`.
2. If the worker settles before the deadline, the reported status wins
   (normally `cancelled`).
3. If the deadline passes with no terminal frame, Hub marks the task terminal
   itself and stops waiting. The worker's late `task.complete` is then ignored
   by ordinary terminal idempotency.
4. If the worker never responds at all, the lease/heartbeat path applies and the
   task lands `lost`.

### Status attribution

The reason a task stopped must stay distinguishable, because a Master retries
differently for each:

| Trigger | Terminal status | `error` content |
|---|---|---|
| Master `CancelTask` | `cancelled` | cancel reason from the request |
| `fail_fast` sibling failure | `cancelled` | `"batch fail_fast: <task_id> failed"` |
| Dependency ended non-`completed` | `cancelled` | `"dependency <task_id> ended <status>"` |
| Execution timeout (`timeout_seconds`) | `failed` | `"execution timeout after Ns"` |
| Cancel requested but worker never settled | `cancelled` | `"cancel grace expired; worker did not settle"` |
| Worker vanished | `lost` | lease / heartbeat detail |

Execution timeout uses the same cancel frame as a delivery mechanism but is
**not** reported as `cancelled` — the task failed on its own budget.

### Side effects are the worker's responsibility

Interrupting the agent loop does not undo what tools already did. Background
processes the task spawned (`terminal(background=True)`, browser sessions,
detached jobs) can outlive the interrupt. A worker SHOULD scope task-spawned
processes so it can best-effort terminate them on cancel, and SHOULD note
un-reversed side effects in the cancelled result's `error` / `summary`. Hub
makes no claim about external state after cancellation.

---

## Orchestration Model

### Primary path — Master-owned join

Master dispatches a batch, watches `callback_topic`, and applies its own join
logic (wait all, fail-fast, threshold, custom DAG). This is the **authoritative**
orchestration model and always works even if Hub helpers are disabled.

### Optional Hub helpers

Hub may implement helpers so Masters that want less client logic can opt in.

| Helper | Behavior | Failure mode |
|---|---|---|
| `depends_on` | Hold task until listed task_ids are `completed`; cycle → reject batch | Dependency reaches a non-`completed` terminal → dependents are `cancelled` immediately (see below) |
| `BatchPolicy` | Emit guidance / cancel siblings per mode; on threshold met may emit AGGREGATE | Master still receives per-task TERMINAL events |
| `aggregate_key` | When every task in the key is terminal, emit one AGGREGATE event carrying `AggregateResult` | Master may ignore AGGREGATE and DIY |

**Dependency failure propagation (not policy-dependent).** When a dependency
reaches `failed`, `lost`, or `cancelled`, Hub transitively marks every waiting
dependent `cancelled` with
`error = "dependency <task_id> ended <status>"` and emits their TERMINAL
events. This rule holds **whether or not** `BatchPolicy` was supplied, so a
`depends_on` graph without a policy can never leave dependents `pending`
forever. `queue_timeout_seconds` remains a backstop for tasks waiting on a
worker rather than on a dependency.

**AGGREGATE contents.** `AggregateResult` carries `status_counts`, the member
`task_ids`, member summaries joined in dispatch order, and the concatenation of
member `fields.metrics`. `TaskFields.extensions` is a `map<string, bytes>` with
worker-defined values, so Hub does **not** attempt to merge it — aggregation is
limited to data whose shape the Hub actually knows. Masters needing custom
reduction read per-task TERMINAL events.

**Metric name collisions.** Member `fields.metrics` are concatenated, not
merged — two members emitting a metric named `tokens_used` produce two entries,
not a sum. Because the Hub stamps each aggregated `Metric` with `origin_task_id`
(field 5), a Master disambiguates by task (cross-referencing `task_ids`) rather
than guessing which member a value came from. The Hub MUST NOT collapse
same-named metrics by name; that would silently destroy information. A Master
that wants a sum/average computes it client-side from the per-task values.

**Invariant:** Hub helpers never suppress per-task TERMINAL events. Master can
always ignore AGGREGATE / BatchPolicy side effects and join from TERMINAL
alone.

**fail_fast:** Hub cancels remaining non-terminal tasks; running workers receive
`task.cancel` and settle `cancelled` per Cancellation & Interrupt Mapping, so
sibling shutdown latency is bounded by each worker's current tool call plus
`cancel_grace_seconds`.

---

## ACP Integration

Task Relay owns **transport + envelope**. ACP owns **session isolation + turn
execution**.

```
task_worker
  └─ task_executor
       ├─ resolve ContextPayload (verify sha256 on refs)
       ├─ build delegation-style prompt
       └─ TaskBackend.run(spec) → TaskResult
            └─ AcpTaskBackend
                 SessionManager.create_session
                 prompt/submit  (full run until agent completes)
                 summary + TaskFields extraction
```

```python
class TaskBackend(Protocol):
    async def run(self, spec: TaskSpec, on_progress: ProgressFn,
                  on_checkpoint: CheckpointFn) -> TaskResult: ...

class AcpTaskBackend(TaskBackend):
    """In-process acp_adapter SessionManager."""

class RemoteAcpTaskBackend(TaskBackend):
    """JSON-RPC to a co-located hermes ACP process (same host)."""
```

### Structured output

Do not rely on free-form `---STRUCTURED---` markers.

1. Prompt uses the same outcome-first summary style as `delegate_task`.
2. `summary` = final assistant text (budgeted).
3. `fields` = optional second pass when `params["structured_output"]` is set
   (auxiliary LLM or constrained JSON extraction).
4. `schema_version = 1` for the above contract.

### Tool isolation

`TaskSpec.toolsets` limits tools (same idea as `delegate_task`). ACP path
passes them through session toolset expansion.

### Cancellation

`AcpTaskBackend` maps `task.cancel` onto the ACP session's `cancel`, which sets
the session cancel event and calls `agent.interrupt()`; the run returns with a
cancelled stop reason at the next agent loop boundary. `RemoteAcpTaskBackend`
forwards cancel over its JSON-RPC channel and terminates the child process if it
does not settle by the hard deadline. Full semantics, escalation, and status
attribution are in Cancellation & Interrupt Mapping.

---

## Worker Scheduling

### Inputs

From `worker.announce` / long-session heartbeats:

```json
{
  "worker_id": "agent-01",
  "session_modes": ["a", "c"],
  "wake_url": null,
  "max_concurrent": 2,
  "credit": 2,
  "resume_formats": ["acp-l1-summary", "custom.v1"],
  "capabilities": {
    "toolsets": ["terminal", "file", "web"],
    "os": "linux",
    "arch": "aarch64",
    "region": "ap-southeast-1"
  },
  "resources": {
    "cpu_cores": 4,
    "memory_gb": 8,
    "gpu": {"count": 1, "model": "…"},
    "disk_gb": 120,
    "network_profile": "unmetered"
  },
  "load": {
    "running_tasks": 1,
    "cpu_percent": 35.0,
    "memory_percent": 60.0
  }
}
```

Self-reported resources are **advisory for scoring** but `min_resources` is a
**hard gate, not a preference**. Hub treats missing worker fields as
unspecified: a `min_resources` field the worker did not report is treated as
unsatisfied (the Hub cannot confirm it), so the worker is excluded — there is
no "best-effort satisfy" path. A task whose `min_resources` no worker can
satisfy stays `pending` until `queue_timeout_seconds` → `lost`; it is never
dispatched to an under-resourced worker. Masters that want soft preferences
(e.g. "prefer more memory") must put them in `params` / hints, not in
`min_resources`.

### Scoring order

1. ACL: `allowed_worker_ids` / `deny_worker_ids` / `target_worker`
2. Capability: required `toolsets` ⊆ worker toolsets (hard gate)
3. Resources: `min_resources` satisfied when specified (hard gate; unspecified worker field = unsatisfied)
4. Load: lowest `running_tasks / max_concurrent` among eligible
5. Optional region preference from `params` / Master hints

`ListWorkers` returns the same view Master needs for manual targeting.

---

## Relay Hub

### File Layout

```
task_relay/
├── proto/task_relay_v1.proto
├── hub/
│   ├── main.py
│   ├── config.py
│   ├── models.py
│   ├── db.py                    # store adapter (SQLite default)
│   ├── grpc_server.py
│   ├── ws_server.py
│   ├── task_router.py           # state machine + idempotency
│   ├── worker_registry.py
│   ├── wake_scheduler.py        # Mode B HTTP wake only
│   ├── session_manager.py       # Mode C sessions, credit, heartbeat, drain
│   ├── checkpoint_store.py
│   ├── batch_orchestrator.py    # depends_on + BatchPolicy + AGGREGATE
│   ├── resource_scheduler.py
│   ├── event_bus.py
│   ├── metrics.py
│   └── auth.py
└── worker/
    ├── task_worker.py
    ├── task_worker_http.py      # Mode B
    ├── task_worker_ws.py
    ├── session_client.py        # Mode C
    ├── task_executor.py
    ├── checkpoint_handler.py
    ├── resource_probe.py
    └── backends/
        ├── acp_backend.py
        └── remote_acp_backend.py
```

### TaskRouter (core)

```
states: pending → running → completed|failed|lost|cancelled

dispatch_task:
  idempotency (task_id) → attempts check → ACL validate
  → persist pending (+ queue_deadline_at)
  → resolve worker → Mode C push (if credit) | Mode B wake | Mode A queue
  → emit STATUS

dispatch_task_batch:
  idempotency on batch_id → compare batch_spec_hash (mismatch → reject)
  → validate DAG (reject cycles)
  → enqueue independent tasks; hold dependents
  → start batch_deadline if policy set
  → ACK

worker_poll / session push / wake claim:
  select pending (ACL, caps, resources, priority), up to worker capacity
  → atomic claim (default) or offer token + preview
  → attempt += 1; set first_progress_deadline_at
  → emit STATUS running
  → deliver task.run (+ resume_from_checkpoint, resume_blob)

on_progress / on_checkpoint:
  clear first-progress deadline → extend lease → emit PROGRESS / CHECKPOINT

on_complete:
  idempotent terminal write → return worker credit → emit TERMINAL
  → unblock depends_on; cancel dependents on non-completed terminal
  → maybe AGGREGATE / BatchPolicy actions

on_cancel(task_id, grace):
  if pending → cancelled immediately (no worker involved)
  if running → push task.cancel {hard_deadline_at}; await cancel.ack
             → on grace expiry, settle cancelled ourselves

on_drain:
  status = draining → stop offering/pushing → keep leases valid

on_disconnect / lease / first-progress / queue expiry:
  → lost; re-enqueue only if attempts remain → TERMINAL when exhausted
```

### Timeout Layers

| Layer | Default | Meaning |
|---|---|---|
| Queue timeout | `queue_timeout_seconds` (default 900) | Time a task may sit `pending` without a worker → `lost` |
| Wake timeout | 30s | Mode B: no claim after POST (falls back to poll, does not fail the task) |
| Claim lease | 30s | Two-step offer not claimed |
| First-progress deadline | `first_progress_seconds` (default 120; worker emits an immediate claim-ack progress frame) | Claimed but silent worker → `lost` fast |
| Session heartbeat | 30s interval; 2 misses → stale | Mode C liveness |
| Progress/checkpoint lease | reset on each progress/checkpoint | Running task still alive |
| Execution timeout | `timeout_seconds` (default 600) | Hard fail (`failed`, delivered via cancel frame) |
| resume_blob size | `resume_blob_max_bytes` (default 1 MiB) | Checkpoint rejected on exceed; worker must use `ContextRef` |
| Cancel grace | `cancel_grace_seconds` (default 60) | Cancel pushed but worker has not settled → Hub settles it itself |
| Batch timeout | `BatchPolicy.batch_timeout_ms` | Unfinished → lost/failed per policy |

Queue timeout is measured from `created_at` (or from re-enqueue on redispatch)
and is what makes "no eligible worker ever appeared" a bounded outcome instead
of an indefinite `pending`.

### Persistence

**Default store: SQLite** for single-Hub deployments.

**Scaling path (same schema contract):** Postgres (or equivalent) behind
`db.py` adapter when concurrent writers / multi-Hub HA are required. Design
does not hard-code SQL dialect features beyond portable types.

```sql
CREATE TABLE tasks (
    task_id TEXT PRIMARY KEY,
    batch_id TEXT,
    master_session_id TEXT,
    goal TEXT NOT NULL,
    params_json TEXT,
    context_json TEXT,
    toolsets_json TEXT,
    worker_id TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    result_json TEXT,
    summary TEXT,
    fields_json TEXT,
    usage_json TEXT,
    error TEXT,
    callback_topic TEXT NOT NULL,
    allow_redispatch INTEGER DEFAULT 0,
    claim_token TEXT,
    claim_expires_at REAL,
    first_progress_deadline_at REAL,
    queue_deadline_at REAL,
    attempt INTEGER DEFAULT 0,
    max_attempts INTEGER DEFAULT 1,
    priority INTEGER DEFAULT 0,
    depends_on_json TEXT,
    aggregate_key TEXT,
    min_resources_json TEXT,
    trace_context_json TEXT,
    allowed_worker_ids_json TEXT,
    deny_worker_ids_json TEXT,
    resume_from_checkpoint TEXT,
    created_at REAL NOT NULL,
    started_at REAL,
    completed_at REAL
);

CREATE INDEX idx_tasks_pending ON tasks(status, priority DESC, created_at);
CREATE INDEX idx_tasks_batch ON tasks(batch_id, status);
CREATE INDEX idx_tasks_aggregate ON tasks(batch_id, aggregate_key, status);

CREATE TABLE batches (
    batch_id TEXT PRIMARY KEY,
    master_session_id TEXT,
    callback_topic TEXT NOT NULL,
    batch_spec_hash TEXT NOT NULL,   -- canonical hash of submitted specs
    policy_json TEXT,
    created_at REAL NOT NULL,
    batch_deadline_at REAL
);

CREATE TABLE workers (
    worker_id TEXT PRIMARY KEY,
    wake_url TEXT,
    session_modes TEXT NOT NULL DEFAULT 'a',  -- comma-joined capability set, e.g. "a,c"; A always present
    capabilities_json TEXT,
    resources_json TEXT,
    load_json TEXT,
    max_concurrent INTEGER DEFAULT 1,
    credit_available INTEGER DEFAULT 0,  -- Mode C push credit
    running_tasks INTEGER DEFAULT 0,
    last_announce_at REAL,
    last_heartbeat_at REAL,
    status TEXT DEFAULT 'offline'        -- offline | idle | busy | stale | draining
);

CREATE TABLE task_events (
    event_id INTEGER PRIMARY KEY AUTOINCREMENT,
    callback_topic TEXT NOT NULL,
    task_id TEXT,                       -- nullable for AGGREGATE rows (describe a group, not a task)
    batch_id TEXT,
    kind TEXT NOT NULL,
    payload_json TEXT,
    event_at REAL NOT NULL,
    CHECK (kind = 'AGGREGATE' OR task_id IS NOT NULL)   -- non-AGGREGATE events must carry a task_id
);

CREATE INDEX idx_events_topic ON task_events(callback_topic, event_id);

CREATE TABLE checkpoints (
    checkpoint_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    event_id INTEGER NOT NULL,
    checkpoint_at REAL NOT NULL,
    summary TEXT,
    fields_json TEXT,
    resume_blob BLOB,
    lease_until REAL,
    PRIMARY KEY (task_id, checkpoint_id)
);

CREATE INDEX idx_checkpoints_task ON checkpoints(task_id, checkpoint_at DESC);
```

### Dependencies

Python Hub / Worker (upper-bounded pins follow repo policy when added to
`pyproject.toml`):

| Package | Role |
|---|---|
| `grpclib` | Master-facing gRPC server |
| `protobuf` / generated stubs | Schema runtime |
| `websockets` | Worker-facing WebSocket server + client |
| `aiosqlite` | Async SQLite store (default) |
| `httpx` | Mode B HTTP wake client |
| `aiohttp` (optional) | Mode B wake HTTP endpoint on the worker |
| `PyJWT` (optional, M3) | Hardened short-lived JWT for master/worker auth |

Go Hub port: stdlib + `google.golang.org/grpc` + protobuf stubs; WS library
chosen at port time (e.g. `nhooyr.io/websocket` or `gorilla/websocket`).

Postgres HA path (store adapter): `asyncpg` or equivalent behind `db.py` —
optional; not required for the SQLite default.

### CLI

```bash
hermes relay-hub \
  --grpc-port 9090 \
  --ws-port 9000 \
  --db "$HERMES_HOME/relay/tasks.db"   # default; resolves via get_hermes_home()

hermes task-worker \
  --worker-id agent-01 \
  --relay-url wss://relay-hub:9000/ws/worker \
  --worker-jwt-file "$HERMES_HOME/relay/worker.jwt" \
  --session-modes a,c \
  --max-concurrent 1
```

The worker obtains its JWT once from the Hub token endpoint (presenting a
long-lived bootstrap credential), caches it at `--worker-jwt-file`, and
refreshes before `exp`. Static `--worker-secret` is intentionally absent — see
the Security section rationale.

### Go Hub Port

Behavior-identical production port after the Python Hub validates the protocol.
Same protobuf, same state machine, higher concurrency / smaller footprint.

| Aspect | Python Hub | Go Hub |
|---|---|---|
| Concurrency model | asyncio | goroutines |
| Role in design | Protocol validation / first shippable Hub | Production-scale twin |
| Footprint | Interpreter + deps | Static binary |

---

## Master SDK Sketch (Go)

```go
batch, _ := client.DispatchTaskBatch(ctx, &pb.DispatchTaskBatchRequest{
    BatchId:       "batch_001",
    CallbackTopic: "session_xyz",
    Specs: []*pb.TaskSpec{
        {TaskId: "t1", Goal: "analyze web-1 5xx", TargetWorker: "agent-01"},
        {TaskId: "t2", Goal: "analyze web-2 5xx", TargetWorker: "agent-02"},
    },
})

stream, _ := client.WatchTask(ctx, &pb.WatchTaskRequest{
    Filter:       &pb.WatchTaskRequest_Topic{Topic: batch.CallbackTopic},
    SinceEventId: since,
})

done := map[string]bool{}
for {
    ev, err := stream.Recv()
    if err != nil { return err }
    since = ev.EventId
    switch ev.Kind {
    case pb.TaskEventKind_TASK_EVENT_KIND_CHECKPOINT:
        // optional intermediate planning
    case pb.TaskEventKind_TASK_EVENT_KIND_TERMINAL:
        done[ev.TaskId] = true
        inject(ev.Result)
    }
    if len(done) == len(batch.Tasks) { break }
}
```

Rust: same `.proto` via `tonic`.

---

## End-to-End Flows

### Mode C (always-on NAT worker)

```
Master                 Hub                      Worker
  │ DispatchBatch        │                         │
  │─────────────────────▶│                         │  (session already open)
  │ WatchTask            │── task.run ────────────▶│
  │◀─ STATUS running ────│                         │── ACP full run
  │◀─ CHECKPOINT ────────│◀─ task.checkpoint ──────│
  │◀─ TERMINAL ──────────│◀─ task.complete ────────│
  │ aggregate → continue │                         │
```

### Mode A poll fallback

```
Master                 Hub                      Worker
  │ DispatchTask         │                         │
  │─────────────────────▶│ pending                 │
  │                      │◀─ worker.poll ──────────│
  │◀─ STATUS running ────│── atomic claim+run ────▶│
  │◀─ TERMINAL ──────────│◀─ task.complete ────────│
```

---

## Error Handling

| Scenario | Behavior |
|---|---|
| Mode B wake timeout | Stay `pending` for poll (Mode A is always available); bounded by `queue_timeout_seconds` |
| Mode C heartbeat missed | Session `stale`; running task → `lost` after lease; credit released; redispatch may attach checkpoint |
| WS drop mid-task | Lease expiry → `lost`; checkpoint retained |
| Claimed but silent worker | `first_progress_seconds` expiry → `lost` without waiting for execution timeout |
| Stale claim | `worker.nack`; task back to `pending` |
| Duplicate complete/checkpoint | Idempotent ACK; first payload wins |
| Attempts exhausted | Task stays terminal; redispatch refused with last `existing_result` |
| Master disconnect | Hub continues; Master replays Watch |
| Watch cursor older than retention | Stream fails `FAILED_PRECONDITION` + `CursorOutOfRange`; Master reconciles via `GetTaskResult` / `ListTasks` |
| Hub crash | `pending` survives; stale `running` reclaimed |
| Execution timeout | `failed` (not `cancelled`); cancel frame pushed to unwind the worker |
| Master cancel | `task.cancel` → ACP interrupt at next loop boundary; worker settles `cancelled` with partial summary |
| Cancel grace expired | Hub settles the task `cancelled` itself; the worker's late terminal frame is ignored idempotently |
| Cancel while task still `pending` | `cancelled` immediately; no worker ever involved |
| Batch resubmit with different specs | `ALREADY_EXISTS` + `BatchSpecMismatch`; nothing enqueued or mutated |
| Batch timeout | Unfinished → `lost`/`failed` per policy; fail_fast cancels siblings |
| Dependency cycle | Reject batch; nothing enqueued |
| Dependency ended non-`completed` | Waiting dependents transitively `cancelled` with a dependency error (policy-independent) |
| Resource mismatch | Stay `pending` until an eligible worker appears or `queue_timeout_seconds` → `lost` |
| Worker draining | Excluded from offers/pushes/wakes; running tasks finish normally; no `lost` churn on redeploy |
| Context sha256 mismatch | Worker fails task with explicit error; never executes |

---

## Security

| Layer | Base (M1) | Hardened (M3) |
|---|---|---|
| Master → Hub | TLS + short-lived Hub-signed JWT | mTLS |
| Mode B wake | single-use wake token (HMAC, per-task, short-lived) | Same + stricter expiry / audience |
| Mode C session | worker JWT on announce; push bound to that session | mTLS-pinned session |
| Worker → Hub | Hub-issued worker JWT (scoped `allowed_toolsets`, `max_concurrent`) | mTLS |
| Context | Size limit; prefer refs | Encrypt inline at rest; signed ContextRef |
| ACL | `allowed_worker_ids` / `deny_worker_ids` | Same + audit log |

Static `worker_secret` bearer is **not** a shippable base. Task Relay crosses
networks and executes arbitrary tool calls, so a long-lived shared secret
transmitted on every `worker.announce` is an identity-equivalent risk: once
leaked it grants full worker impersonation. Worker JWT (short-lived,
Hub-issued, scoped to `allowed_toolsets` and `max_concurrent`) is therefore the
M1 baseline. A worker obtains its JWT by presenting a long-lived bootstrap
credential to the Hub's token endpoint once, then refreshes before `exp`; the
JWT travels as a bearer on the WS upgrade, never in the `worker.announce` body.
mTLS remains optional hardening for high-trust internal deployments.

Worker JWT claims (M1 base):

```json
{
  "sub": "worker-01",
  "aud": "task-relay-hub",
  "iss": "hermes-relay-hub",
  "allowed_toolsets": ["terminal", "file"],
  "max_concurrent": 2,
  "exp": 1710003600
}
```

---

## Observability

`TraceContext` rides on `TaskSpec`, `TaskEvent`, `task.run`, checkpoints, and
completes.

| Metric | Type | Labels |
|---|---|---|
| `relay_tasks_dispatched_total` | counter | `status`, `batch` |
| `relay_task_latency_seconds` | histogram | `status`, `worker_id` |
| `relay_worker_sessions_active` | gauge | `worker_id`, `session_modes` |
| `relay_checkpoint_count` | counter | `worker_id` |
| `relay_batch_completion_seconds` | histogram | `completion_mode` |

Align naming with `docs/observability/relay-shared-metrics.md` when present.

---

## Capability Modules (design inventory)

Implementation priority is deferred. Below is the **design dependency graph**
(what must be consistent with what), not a shipping schedule.

### M1 — Delivery Core

- `task_relay_v1.proto` (core messages + RPCs)
- Hub: db, auth (base), router, registry, event_bus, grpc, ws poll/claim
- Worker: poll loop, ACP backend, executor, CLI
- Semantics: idempotency, timeouts, Watch replay, Cancel, ListWorkers
- Cancel path: `task.cancel` → ACP interrupt, grace escalation, status attribution
- Tests: claim race, idempotent dispatch/complete, Watch reconnect, ACP path,
  cancel-during-tool-call settles `cancelled` with partial summary,
  timeout settles `failed` (not `cancelled`), cancel grace expiry

### M2 — Connection & Continuity

- Mode C session_manager + session_client + heartbeat + credit push + drain
- Mode B wake HTTP (optional capability)
- Checkpoint store + L1/L2 contract + `task.run` resume payload on redispatch
- Context fetch/decode + sha256 verify over plaintext
- Tests: long-session push latency, credit cap enforcement, drain without `lost`,
  lease via checkpoint, resume_blob ignore-safe path

### M3 — Orchestration & Hardening

- `depends_on` DAG + BatchPolicy + AGGREGATE
- Resource announce + scheduler scoring
- Hardened JWT/mTLS + signed refs + ACL enforcement
- TraceContext + metrics
- Store adapter for Postgres/HA
- Tests: DAG cycle reject, fail_fast cancel, resource filter, auth negative cases

### Go Hub Port

- Behavior-identical Hub after protocol stability

---

## Files Changed / Created

Inventory of the complete design surface. Line counts are order-of-magnitude
estimates for the finished design (not a shipping schedule).

| File / path | Est. lines | Action | Module |
|---|---|---|---|
| `task_relay/proto/task_relay_v1.proto` | ~200 | Create | M1–M3 |
| `task_relay/gen/py/` | ~250 | Generate | M1–M3 |
| `task_relay/gen/go/` | ~250 | Generate | M1–M3 |
| `task_relay/hub/__init__.py` | ~10 | Create | M1 |
| `task_relay/hub/main.py` | ~80 | Create | M1 |
| `task_relay/hub/config.py` | ~60 | Create | M1 |
| `task_relay/hub/models.py` | ~120 | Create | M1 |
| `task_relay/hub/db.py` | ~200 | Create | M1 (+ Postgres adapter in M3) |
| `task_relay/hub/grpc_server.py` | ~200 | Create | M1 |
| `task_relay/hub/ws_server.py` | ~220 | Create | M1 + M2 |
| `task_relay/hub/task_router.py` | ~250 | Create | M1 |
| `task_relay/hub/worker_registry.py` | ~100 | Create | M1 |
| `task_relay/hub/wake_scheduler.py` | ~100 | Create | M2 |
| `task_relay/hub/event_bus.py` | ~120 | Create | M1 |
| `task_relay/hub/auth.py` | ~120 | Create | M1 + M3 |
| `task_relay/hub/session_manager.py` | ~150 | Create | M2 |
| `task_relay/hub/checkpoint_store.py` | ~100 | Create | M2 |
| `task_relay/hub/batch_orchestrator.py` | ~180 | Create | M3 |
| `task_relay/hub/resource_scheduler.py` | ~100 | Create | M3 |
| `task_relay/hub/metrics.py` | ~80 | Create | M3 |
| `task_relay/worker/task_worker.py` | ~150 | Create | M1 |
| `task_relay/worker/task_worker_ws.py` | ~120 | Create | M1 |
| `task_relay/worker/task_worker_http.py` | ~80 | Create | M2 |
| `task_relay/worker/session_client.py` | ~120 | Create | M2 |
| `task_relay/worker/task_executor.py` | ~120 | Create | M1 |
| `task_relay/worker/checkpoint_handler.py` | ~80 | Create | M2 |
| `task_relay/worker/resource_probe.py` | ~60 | Create | M3 |
| `task_relay/worker/backends/acp_backend.py` | ~150 | Create | M1 |
| `task_relay/worker/backends/remote_acp_backend.py` | ~80 | Create | M1 |
| `hermes_cli/subcommands/relay_hub.py` | ~40 | Create | M1 |
| `hermes_cli/subcommands/task_worker.py` | ~40 | Create | M1 |
| `hermes_cli/main.py` | ~15 | Modify | M1 |
| `pyproject.toml` | ~10 | Modify | M1 (+ optional M3 deps) |
| `task_relay/go/` (full Hub port) | ~1600 | Create | Go Hub Port |

**Totals (approx.):** Hub Python ~2100 · Worker Python ~960 · Proto/gen ~700 ·
CLI/wiring ~100 · Go Hub ~1600.

---

## Design Decisions

| Decision | Conclusion |
|---|---|
| Hub vs Master | Hub = delivery + optional helpers; Master = authoritative orchestration |
| Params / context | Hub transparent pass-through; Master controls content |
| Connection | Mode A mandatory (so no `can_poll` flag); Mode C preferred for always-on NAT; Mode B optional when inbound HTTP exists |
| Mode C delivery | Push-only within worker-granted credit; no Hub→worker wake frame |
| Concurrency | `max_concurrent` honored in both modes: `max_tasks` offers in Mode A, credit in Mode C |
| Poll claim | Atomic claim-on-poll default; two-step offer carries metadata-only `preview` |
| Retries | `max_attempts` bounds all executions; lease expiry consumes an attempt |
| Liveness | `queue_timeout_seconds` + `first_progress_seconds` bound pending and silent-claim states |
| Event cursor | One global monotonic sequence; expired cursor fails with `CursorOutOfRange`, never silently |
| Batch conflict | Same `batch_id` with different specs is rejected via `batch_spec_hash` mismatch |
| Dependency failure | Non-`completed` dependency cancels dependents regardless of `BatchPolicy` |
| Aggregation | `AggregateResult` (group-shaped, empty `task_id`); Hub merges metrics only, never opaque `extensions` |
| Cancellation | Cooperative by design: `task.cancel` → ACP session cancel + `agent.interrupt()`, effective at the next agent loop boundary; in-flight tool calls are not killed |
| Cancel escalation | `cancel_grace_seconds` (default 60), then Hub settles the task itself; `RemoteAcpTaskBackend` may kill its child |
| Cancel vs timeout | Requested cancel → `cancelled`; budget exhaustion → `failed`, even though both use the cancel frame |
| Shutdown | `worker.drain` → finish running → `worker.close`; drained workers get no new work |
| Context payload | oneof `inline` / `inline_gzip` / `ref`; `sha256` always over final plaintext |
| Checkpoint | L1 observable + lease; L2 opaque resume_blob best-effort; no false ACP perfect-restore promise |
| Structured output | Delegation-style summary; optional structured pass via params |
| Batch helpers | Never suppress per-task TERMINAL; Master can ignore AGGREGATE |
| Model override | `params["model"]` may override worker default |
| Retention | 7 days default for tasks, events, checkpoints |
| Store | SQLite default; Postgres-capable adapter for HA |
| Execution backend | `AcpTaskBackend` default; remote ACP optional |
| Security | Base tokens; hardened JWT/mTLS/ACL as part of complete design |

---

## Revision History

| Date | Change |
|---|---|
| 2026-07-31 | Initial draft |
| 2026-07-30 | rev 2: Poll, Batch, TaskEvent replay, idempotency, ACP, ContextRef |
| 2026-07-30 | rev 3: Mode C, checkpoint, DAG/policy, resources, hardened security, tracing |
| 2026-07-30 | rev 4: Responsibility split; Mode A/B/C as one matrix; atomic claim default; L1/L2 checkpoint contract; Master-primary orchestration with opt-in Hub helpers; `deny_worker_ids` in schema; capability modules instead of shipping-priority phases; store scaling path; SQL/consistency fixes |
| 2026-07-30 | rev 4a: Restore Dependencies section and Files Changed / Created inventory |
| 2026-07-30 | rev 5a: Cancellation & Interrupt Mapping — cooperative `task.cancel` → ACP session cancel + `agent.interrupt()`, `cancel.ack`, `hard_deadline_at`, `cancel_grace_seconds` escalation, cancelled-vs-failed attribution, partial-result salvage, side-effect ownership |
| 2026-07-30 | rev 5: Define `TaskUsage` / `ListWorkers*` / `ListTasks*` / `CancelTask*` / `TaskResultRequest` / `WorkerInfo`; replace `already_terminal` with `idempotent_hit`; define two-step `preview`; add `queue_timeout_seconds`, `max_attempts`/`attempt`, `first_progress_seconds`; `max_concurrent` via `max_tasks` (Mode A) and credit (Mode C); Mode C is push-only (drop `session.wake`); move `resume_from_checkpoint` out of `TaskSpec` into `task.run`; dependency-failure cancels dependents; `CursorOutOfRange`; global `event_id` sequence; `batch_spec_hash` conflict rejection; `AggregateResult` message; `worker.drain`/`worker.close`; remove `can_poll`; restructure `ContextPayload` oneof with plaintext-scoped `sha256` |
| 2026-07-30 | rev 6 (review fixes): **P0-1** corrected `delegate_task` alignment claim — only `goal`/`context` are borrowed; `toolsets`/`timeout_seconds` are deliberately promoted to explicit fields because remote workers cross trust boundaries (verified against `tools/delegate_tool.py`). **P0-2** corrected gateway-relay-wake reuse claim — wake poke is an unsigned GET and its HMAC is a per-gateway WS-upgrade secret (not single-use); Task Relay's single-use wake token is independently designed, only outbound-only connectivity is shared. **P1-3** raised `first_progress_seconds` default 60→120 and mandated an immediate claim-ack `task.progress` frame before first model round-trip (ACP cold start can exceed 60s). **P1-4** promoted worker JWT + Master→Hub JWT from M3 (optional) to M1 baseline; static `worker_secret` bearer is no longer a shippable base for a cross-network tool-execution system; mTLS stays optional M3 hardening |
| 2026-07-30 | rev 7 (consistency fixes): **S1** `session_mode` (singular `string`) → `repeated SessionMode session_modes` capability set + new `enum SessionMode { A; B; C; }` — resolves the contradiction with "Modes are capabilities, not mutually exclusive" (A always present, mandatory); updated proto, 4 flow diagrams, JSON example, `workers` table schema, metrics label, and CLI flag (`--session-modes a,c`). **S2** `task_events.task_id` `NOT NULL` → nullable + `CHECK (kind='AGGREGATE' OR task_id IS NOT NULL)` — AGGREGATE rows describe a group and carry an empty task_id per the TaskEvent contract. **S3** `resume_formats` moved out of the announce `capabilities` block to the top level of the JSON, matching `WorkerInfo` proto field 8. **S4** CLI `--db /path/to/tasks.db` → `--db "$HERMES_HOME/relay/tasks.db"` (default via `get_hermes_home()`). **S5** CLI `--worker-secret` → `--worker-jwt-file` (worker obtains JWT from Hub token endpoint, caches + refreshes before `exp`), aligning with the rev 6 P1-4 rationale that static bearer is not a shippable base |
| 2026-07-30 | rev 8 (limits & policy fixes): **P1-5** `resume_blob` capped at `resume_blob_max_bytes` (default 1 MiB); Hub rejects oversized checkpoints with `INVALID_ARGUMENT`, worker must fall back to `ContextRef` (added proto comment, L2 contract bullet, Timeout Layers table row). **P1-6** global `event_id` sequence HA scope clarified — single Hub instance (M1) owns an `AUTOINCREMENT`/`SEQUENCE`; multi-Hub HA turns it into a write-contention point, partitioned/hybrid sequence deferred to M3 and MUST preserve globally-monotonic cross-filter comparability. **P1-7** `WatchTask` slow-consumer policy: bounded per-stream buffer (`watch_stream_buffer_events`, default 1024); on overflow Hub closes stream with `RESOURCE_EXHAUSTED` + new `SlowConsumer{delivered_event_id, newest_event_id}` detail, never blocks production nor drops silently. **P3-12** `AggregateResult.metrics` name-collision policy: concatenation not merge; Hub stamps each aggregated `Metric` with new `origin_task_id` field (field 5); Hub MUST NOT collapse same-named metrics; Master disambiguates by task and sums client-side. **P3-13** `ListTasksRequest.limit` capped: default 100, max `list_tasks_max_limit` (default 500); Hub clamps over-limit values. **P3-14** Mode C cancel credit return: a `cancelled` task refunds one credit exactly like `task.complete`; only a never-acknowledged push (lost on disconnect) is recovered at `lost` settlement — preserves `granted = in_flight + available`. **P3-15** `min_resources` is a hard gate not a preference: unspecified worker field = unsatisfied (excluded, never best-effort); unsatisfiable → permanent pending until `queue_timeout_seconds` → `lost`; soft preferences belong in `params`/hints (added proto comment + scoring-order annotation) |
