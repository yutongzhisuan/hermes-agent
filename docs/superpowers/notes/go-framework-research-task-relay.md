# Go Framework Research for Task Relay Master Agent & Relay Hub

**Date:** 2026-07-30  
**Spec:** `docs/superpowers/specs/2026-07-31-task-relay-design.md`  
**Scope:** Libraries and frameworks for implementing the Go/Rust Master Agent and the Go port of the Relay Hub. Findings are based on primary sources (official docs, source code, first-party GitHub repositories, Go package docs).

---

## Executive Summary

The Task Relay spec calls for a **Master Agent** (Go/Rust) that dispatches tasks to a **Relay Hub** and joins streamed results, plus a **Go Hub port** that is behavior-identical to the Python Hub. The Hub must expose:

- A **gRPC server** to Masters (`DispatchTask`, `WatchTask`, `ListWorkers`, etc.).
- A **WebSocket JSON-RPC server** to workers (poll/claim, long-session push, heartbeat, credit, drain).
- Internals: task state machine, idempotency, lease/heartbeat, checkpoint store, optional DAG/batch helpers, auth, metrics, tracing.

This note surveys Go libraries for each layer and recommends a minimal, production-ready stack. All claims cite primary sources.

---

## 1. gRPC / Protocol Buffers

### 1.1 Runtime & server/client: `google.golang.org/grpc`

- **GitHub:** https://github.com/grpc/grpc-go
- **Official docs:** https://grpc.io/docs/languages/go/
- **Go package:** https://pkg.go.dev/google.golang.org/grpc
- **Maturity:** Stable, GA. The canonical Go implementation of gRPC. Supports unary, client/server/bidi streaming, keepalive, TLS/mTLS, interceptors, stats handlers, health, reflection, and load balancing.
- **Fit for Task Relay:** Directly implements every `TaskRelay` service RPC in the spec, including the streaming `WatchTask`. The spec's gRPC service definition maps one-to-one onto `grpc-go` server/client stubs.
- **Caveats:** Heavy dependency tree; `google.golang.org/protobuf` v2 API is required for modern code generation. Connection management can be subtle (see the FAQ on `"transport is closing"` errors). [grpc-go README](https://github.com/grpc/grpc-go)

### 1.2 Alternative RPC layer: `connectrpc.com/connect`

- **GitHub:** https://github.com/connectrpc/connect-go
- **Go package:** https://pkg.go.dev/connectrpc.com/connect
- **Maturity:** Stable (v1.x, semantic-versioning guaranteed). Backed by Buf.
- **What it is:** A slim RPC library over `net/http` that speaks three protocols: Connect, gRPC, and gRPC-Web. Generated handlers are plain `http.Handler`s; clients use `http.Client`. Streaming works over HTTP/1.1 and HTTP/2.
- **Fit for Task Relay:** Could run the Master↔Hub API on a single HTTP port with both gRPC and curl-friendly Connect clients. Useful if the Hub port wants to avoid a separate gRPC-only listener or expose debug endpoints alongside the RPC surface.
- **Caveats:** It is *not* `grpc-go`; existing gRPC middleware/interceptors do not plug in directly. Vanguard-go can bridge some interoperability, but for a strict gRPC spec, `grpc-go` is the reference implementation. [connect-go README](https://github.com/connectrpc/connect-go)

### 1.3 Protobuf toolchain: `bufbuild/buf`

- **GitHub:** https://github.com/bufbuild/buf
- **Docs:** https://buf.build/docs/generate/tutorial/
- **Go package:** https://pkg.go.dev/github.com/bufbuild/buf
- **Maturity:** Stable CLI (v1.x, no breaking changes until v2.0). Widely adopted.
- **What it is:** A modern Protobuf toolchain: compiler, linter (`buf lint`), breaking-change detector (`buf breaking`), formatter, code generator (`buf generate`), and dependency manager (BSR).
- **Fit for Task Relay:** The spec's `task_relay_v1.proto` can be versioned, linted, and used to generate Go (and Python/Rust) stubs from a checked-in `buf.gen.yaml`. Remote plugins remove the need to install `protoc-gen-go` on every machine/CI runner.
- **Caveats:** BSR cloud features are optional; core CLI works offline. Breaking-change detection should be run in CI to enforce the spec's schema-compatibility rules. [buf README](https://github.com/bufbuild/buf)

### 1.4 Protobuf code generation: `google.golang.org/protobuf`

- **GitHub:** https://github.com/protocolbuffers/protobuf-go
- **Go package:** https://pkg.go.dev/google.golang.org/protobuf
- **Maturity:** Stable, the official v2 API for Protocol Buffers in Go.
- **Fit for Task Relay:** Generated Go types for `TaskSpec`, `TaskEvent`, `TaskResult`, etc. Required by both `grpc-go` and Connect.
- **Caveats:** Do not use the deprecated `github.com/golang/protobuf` path. [protobuf-go repo](https://github.com/protocolbuffers/protobuf-go)

### Recommendation

- Use **`google.golang.org/grpc`** for the Master↔Hub RPC runtime (matches the spec exactly).
- Use **`buf`** for schema management and code generation.
- Evaluate **`connectrpc.com/connect`** only if the Hub port needs to co-locate HTTP/REST-style callers on the same port; otherwise keep the stack uniform with `grpc-go`.

---

## 2. WebSocket + JSON-RPC (Worker ↔ Hub)

The spec defines a bidirectional WebSocket JSON-RPC frame protocol for workers: `worker.announce`, `worker.poll`, `task.run`, `task.checkpoint`, `task.complete`, `worker.credit`, `worker.heartbeat`, `worker.drain`, etc.

### 2.1 WebSocket library: `github.com/gorilla/websocket`

- **GitHub:** https://github.com/gorilla/websocket
- **Go package:** https://pkg.go.dev/github.com/gorilla/websocket
- **Maturity:** Mature, de facto standard. Maintained by the Gorilla toolkit.
- **Fit for Task Relay:** Battle-tested for concurrent reads/writes, prepared writes, ping/pong, compression, and subprotocols. The spec's per-task-keyed message interleaving maps naturally to goroutines + mutex-protected `WriteMessage` calls.
- **Caveats:** Does not have built-in automatic reconnect or heartbeat helpers; those must be built. API is lower-level than `coder/websocket`. [gorilla/websocket package docs](https://pkg.go.dev/github.com/gorilla/websocket)

### 2.2 WebSocket library: `github.com/coder/websocket`

- **GitHub:** https://github.com/coder/websocket
- **Go package:** https://pkg.go.dev/github.com/coder/websocket
- **Maturity:** Stable, actively maintained by Coder (formerly `nhooyr.io/websocket`, which is now deprecated). Zero dependencies.
- **Fit for Task Relay:** First-class `context.Context` support, idiomatic API, concurrent writes, close handshake, `net.Conn` wrapper, permessage-deflate compression, and a `wsjson` subpackage. The comparison in its README explicitly notes it is faster/easier for idiomatic Go than `gobwas/ws` and has full `context` support unlike Gorilla. [coder/websocket README](https://github.com/coder/websocket)
- **Caveats:** Smaller ecosystem than Gorilla; prepared-message optimization is missing (Gorilla advantage for high fan-out). Roadmap lists "ping pong heartbeat helper" as not yet done, so heartbeat logic must still be custom.

### 2.3 Low-level WebSocket: `github.com/gobwas/ws`

- **GitHub:** https://github.com/gobwas/ws
- **Go package:** https://pkg.go.dev/github.com/gobwas/ws
- **Maturity:** v1.x stable; ~78% coverage, passes Autobahn Test Suite.
- **Fit for Task Relay:** Extremely low-level, zero-copy upgrade, buffer reuse. Appropriate only if the Hub port needs to handle tens of thousands of concurrent worker sockets and is willing to manage its own framing.
- **Caveats:** The `coder/websocket` README calls it "quite bloated" for idiomatic Go; overkill for the Task Relay worker protocol unless benchmarking proves otherwise. [gobwas/ws README](https://github.com/gobwas/ws)

### 2.4 JSON-RPC framing: write a small codec

The worker protocol is JSON-RPC-ish but domain-specific (method names like `worker.announce`, `task.run`). None of the surveyed generic JSON-RPC libraries natively understand the spec's credit/heartbeat/drain semantics, so they are best used only for message framing/dispatch.

- **`github.com/sourcegraph/jsonrpc2`** — client/server JSON-RPC 2.0 implementation; small but maintenance mode (orphaned package in Debian as of 2026). https://github.com/sourcegraph/jsonrpc2
- **`github.com/filecoin-project/go-jsonrpc`** — low-boilerplate RPC 2.0 with custom transport support. https://github.com/filecoin-project/go-jsonrpc
- **`github.com/viant/jsonrpc`** — supports HTTP streamable/NDJSON transports. https://github.com/viant/jsonrpc

**Recommendation:** Implement the spec's frame codec directly on top of `coder/websocket` or `gorilla/websocket`; the message shapes are few and the dispatch logic is domain-specific. A generic JSON-RPC library adds little value and may constrain the credit/heartbeat/drain design.

### Recommendation

- **Primary:** `github.com/coder/websocket` for new code — idiomatic, `context`-native, zero deps, actively maintained.
- **Alternative:** `github.com/gorilla/websocket` if the team already has operational expertise or needs prepared writes/high fan-out.
- **Avoid:** `gobwas/ws` unless profiling shows it is necessary.

---

## 3. Agent / Task Orchestration Frameworks

The spec explicitly keeps orchestration **Master-primary**; the Hub only offers optional helpers (`depends_on`, `BatchPolicy`, `AggregateResult`). Therefore a heavyweight workflow engine is not required for the Hub. The Master Agent, however, may benefit from a durable-execution library if it needs to survive its own restarts while long-running batches are in flight.

### 3.1 Temporal (`go.temporal.io/sdk`)

- **GitHub:** https://github.com/temporalio/sdk-go
- **Docs:** https://docs.temporal.io/
- **Go package:** https://pkg.go.dev/go.temporal.io/sdk
- **Maturity:** Stable, production, backed by Temporal Technologies. Recent v1.33+ moved from gogo/protobuf to golang/protobuf v2.
- **Fit for Task Relay:** A Master Agent implemented as a Temporal workflow could durably dispatch batches, `WatchTask`, apply join policies, and aggregate results across process restarts. Temporal handles timers, retries, sagas, and signals (e.g., cancel).
- **Caveats:** Adds a Temporal server/sidecar dependency. The spec's Hub is *not* a Temporal application; the Master would be the only Temporal workflow. Determinism constraints (no real time, no random, no external I/O inside workflows) must be respected. [sdk-go README](https://github.com/temporalio/sdk-go)

### 3.2 Cadence (`go.uber.org/cadence` / `github.com/cadence-workflow/cadence-go-client`)

- **GitHub:** https://github.com/uber/cadence and https://github.com/cadence-workflow/cadence-go-client
- **Docs:** https://cadenceworkflow.io/docs/go-client
- **Maturity:** Stable, open-source since 2017, CNCF-adjacent community. Temporal is a fork of Cadence.
- **Fit for Task Relay:** Same durable-execution value proposition as Temporal. Cadence Go client requires deterministic workflow code using `workflow.Channel`/`workflow.Selector` instead of native Go channels.
- **Caveats:** Smaller commercial momentum than Temporal. Requires running the Cadence backend (Cassandra/MySQL/PostgreSQL + optional Kafka/Elasticsearch). [Cadence README](https://github.com/uber/cadence)

### 3.3 Dapr Durable Task Framework (`github.com/dapr/durabletask-go`)

- **GitHub:** https://github.com/dapr/durabletask-go
- **Go package:** https://pkg.go.dev/github.com/dapr/durabletask-go/task
- **Maturity:** Active, intended as the basis for Dapr Workflows embedded engine. Includes SQLite backend and gRPC sidecar.
- **Fit for Task Relay:** Embeddable durable orchestration engine; can run in-process without a separate cluster. Supports activities, timers, external events, fan-out/fan-in, and OpenTelemetry tracing.
- **Caveats:** Go SDK is less complete than .NET/Java; full feature set "will be added over time." Sidecar model is designed for one client at a time per pod. [durabletask-go README](https://github.com/dapr/durabletask-go)

### 3.4 Lightweight state machine: `github.com/looplab/fsm`

- **GitHub:** https://github.com/looplab/fsm
- **Go package:** https://pkg.go.dev/github.com/looplab/fsm
- **Maturity:** Stable v1.x, simple and widely used.
- **Fit for Task Relay:** Useful inside the Hub for the per-task state machine (`pending → running → completed|failed|lost|cancelled`). Callbacks can trigger side effects (lease deadlines, event emission).
- **Caveats:** Not durable by itself; persistence must be layered underneath. [looplab/fsm README](https://github.com/looplab/fsm)

### Recommendation

- **For the Hub port:** Do **not** adopt Temporal/Cadence/Dapr as the core. Use an in-House state machine (possibly `looplab/fsm`) backed by SQLite/Postgres, matching the Python Hub design.
- **For the Master Agent (optional):** If the Master must survive restarts with in-flight batches, evaluate **Temporal** first (momentum, docs, managed cloud option), then **Dapr Durable Tasks** if an embedded sidecar is preferred, then **Cadence** if the organization already runs it.

---

## 4. Durable Task / Workflow Engines

This section overlaps with §3 but focuses on job-queue / background-task libraries that could back Hub task dispatch or worker-side retries.

### 4.1 River (`github.com/riverqueue/river`)

- **GitHub:** https://github.com/riverqueue/river
- **Go package:** https://pkg.go.dev/github.com/riverqueue/river
- **Maturity:** Stable, high-performance, Postgres-native.
- **Fit for Task Relay:** If the Go Hub uses Postgres as its store adapter, River could manage internal background jobs (e.g., lease expiry reclamation, batch timeout enforcement, wake retries) transactionally within the same database. Transactional enqueueing is a strong fit for Hub internal tasks.
- **Caveats:** Tied to Postgres. Not a substitute for the worker↔Hub WebSocket protocol. [River README](https://github.com/riverqueue/river)

### 4.2 Machinery (`github.com/RichardKnop/machinery`)

- **GitHub:** https://github.com/RichardKnop/machinery
- **Go package:** https://pkg.go.dev/github.com/RichardKnop/machinery/v2
- **Maturity:** Mature, v2 recommended. Supports Redis, AMQP, SQS, GCP Pub/Sub backends.
- **Fit for Task Relay:** Generic async task queue with groups, chords, chains, and periodic tasks. Could be used behind the Hub for internal task scheduling, but is not aligned with the spec's idempotent `task_id`/event-stream model.
- **Caveats:** Heavy dependency set if multiple brokers are imported; use v2 to avoid pulling unused drivers. [Machinery README](https://github.com/RichardKnop/machinery)

### Recommendation

- If the Hub stores state in **Postgres**, consider **River** for internal Hub maintenance jobs (lease reclamation, batch deadlines).
- Avoid adopting a general-purpose workflow engine as the primary Hub task router; the spec's state machine and event-stream design are intentionally Hub-specific.

---

## 5. Connection Management / NAT / Outbound-Only Workers

The spec's connectivity model is: workers are outbound-only (Mode A poll, Mode C long session), with optional Mode B HTTP wake. The Hub must manage reconnect, heartbeat, credit-based backpressure, and graceful drain.

### 5.1 WebSocket reconnect helpers

No first-party Go library fully implements the spec's worker lifecycle, but small wrappers exist:

- **`github.com/tenrok/recws`** — auto-reconnect WebSocket client based on Gorilla. https://pkg.go.dev/github.com/tenrok/recws
- Most production code writes a small reconnect loop around `coder/websocket` or `gorilla/websocket` to honor the spec's exponential backoff (1 s → 30 s), announce on connect, and resume `since_event_id`.

### 5.2 Long-poll helpers

- **`github.com/jcuga/golongpoll`** — HTTP long-poll pub/sub library. https://github.com/jcuga/golongpoll
- The spec's Mode A is WebSocket-based long-poll (`worker.poll {max_wait_ms}`), not HTTP long-poll, so a generic long-poll library is mostly irrelevant. The Hub can hold the poll with a simple goroutine + channel.

### 5.3 NATS (`github.com/nats-io/nats.go`)

- **GitHub:** https://github.com/nats-io/nats.go
- **Go package:** https://pkg.go.dev/github.com/nats-io/nats.go
- **Maturity:** Stable, mature, widely deployed. JetStream provides persistence.
- **Fit for Task Relay:** Could be used as an internal event bus or worker-to-Hub transport alternative, especially with JetStream for at-least-once delivery. NATS is excellent for NAT/firewall traversal via outbound-only connections and has built-in reconnect, queue groups, and request/reply.
- **Caveats:** The spec calls for a custom WebSocket JSON-RPC worker protocol, so NATS would be a second transport, not a replacement. If the Hub needs an internal pub/sub for events, NATS or NATS Streaming/JetStream is a strong candidate. [nats.go README](https://github.com/nats-io/nats.go)

### 5.4 Cluster membership: `github.com/hashicorp/memberlist`

- **GitHub:** https://github.com/hashicorp/memberlist
- **Go package:** https://pkg.go.dev/github.com/hashicorp/memberlist
- **Maturity:** Stable, used by Consul/Serf. SWIM + Lifeguard gossip protocol.
- **Fit for Task Relay:** If the Go Hub is scaled to multiple instances, memberlist can maintain Hub-node membership and failure detection. Not needed for the single-Hub SQLite deployment.
- **Caveats:** Eventually consistent; useful for gossiping worker/session presence between Hub nodes, but the authoritative state must remain in Postgres. [memberlist README](https://github.com/hashicorp/memberlist)

### Recommendation

- Implement the worker reconnect/heartbeat/credit logic directly on top of the chosen WebSocket library; the spec's semantics are too specific for an off-the-shelf wrapper.
- Use **NATS** only if the architecture later adds an internal event-bus requirement.
- Use **memberlist** only for multi-Hub clustering (M3/HA path).

---

## 6. Observability / Tracing

The spec requires `TraceContext` propagation, Prometheus-style metrics, and distributed tracing across gRPC and WebSocket paths.

### 6.1 OpenTelemetry Go

- **GitHub:** https://github.com/open-telemetry/opentelemetry-go
- **Go package:** https://pkg.go.dev/go.opentelemetry.io/otel
- **Maturity:** Traces and Metrics stable; Logs beta.
- **Fit for Task Relay:** Propagate `TraceContext` from Master → Hub → worker and into checkpoints/completes. Export via OTLP to any backend.
- **Caveats:** Logs are still beta; use structured logging (`log/slog`) alongside OTel if needed. [opentelemetry-go README](https://github.com/open-telemetry/opentelemetry-go)

### 6.2 gRPC OpenTelemetry instrumentation

- **Go package:** https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc
- **What it is:** Official gRPC instrumentation using the `stats.Handler` API (recommended) or legacy interceptors.
- **Fit for Task Relay:** One-line tracing and metrics for the Master↔Hub gRPC service. The `TraceContext` protobuf field can be linked to the OTel span context via a custom interceptor or stats handler.
- **Caveats:** Older interceptor-based APIs are deprecated; use `otelgrpc.NewServerHandler()` / `NewClientHandler()` with `grpc.StatsHandler`. A CVE-2023-47108 advisory noted unbounded cardinality in older metrics; ensure current versions and bounded attributes. [otelgrpc package docs](https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc)

### 6.3 Prometheus client

- **GitHub:** https://github.com/prometheus/client_golang
- **Go package:** https://pkg.go.dev/github.com/prometheus/client_golang/prometheus
- **Maturity:** Stable, de facto standard for Go metrics.
- **Fit for Task Relay:** Implements the spec's counters/gauges/histograms (`relay_tasks_dispatched_total`, `relay_task_latency_seconds`, `relay_worker_sessions_active`, `relay_checkpoint_count`, `relay_batch_completion_seconds`).
- **Caveats:** API client subpackage is still experimental. [client_golang README](https://github.com/prometheus/client_golang)

### Recommendation

- Use **OpenTelemetry Go** for tracing and **Prometheus client_golang** for metrics.
- Instrument the gRPC surface with **`otelgrpc` stats handlers**.
- For WebSocket frames, manually create OTel spans from the propagated `TraceContext` and emit Prometheus counters per frame kind.

---

## 7. Recommended Stack

| Layer | Recommended library | Rationale |
|---|---|---|
| Protobuf toolchain | `github.com/bufbuild/buf` | Lint, breaking-change detection, managed code generation. |
| Protobuf runtime | `google.golang.org/protobuf` | Official v2 API. |
| Master↔Hub gRPC | `google.golang.org/grpc` | Matches spec exactly; stable; streaming support. |
| Worker↔Hub WebSocket | `github.com/coder/websocket` | Idiomatic, context-native, zero deps, maintained. |
| Worker JSON-RPC codec | In-house small codec | Domain-specific frame protocol; generic libraries don't add value. |
| Hub task state machine | In-House + optionally `github.com/looplab/fsm` | Spec's state machine is load-bearing and persistence-backed. |
| Hub store | `database/sql` + `lib/pq` or `pgx` (Postgres), `modernc.org/sqlite` or `mattn/go-sqlite3` (SQLite) | Matches spec's SQLite-default/Postgres-HA design. |
| Internal Hub jobs (Postgres) | `github.com/riverqueue/river` | Transactional background jobs if Postgres is used. |
| Master durability (optional) | `go.temporal.io/sdk` or `github.com/dapr/durabletask-go` | Only if Master must survive restarts with in-flight batches. |
| Tracing | `go.opentelemetry.io/otel` + `otelgrpc` | Propagate `TraceContext` across gRPC and WS. |
| Metrics | `github.com/prometheus/client_golang` | Implement spec metrics directly. |
| Internal event bus (optional) | `github.com/nats-io/nats.go` (+ JetStream) | If Hub needs decoupled event distribution. |
| Multi-Hub clustering (optional) | `github.com/hashicorp/memberlist` | Gossip membership for HA Hub nodes. |

---

## 8. Caveats & Open Questions

1. **Spec fidelity vs. framework convenience.** The spec's worker protocol (credit, drain, Mode A/B/C selection, idempotent checkpoints) is domain-specific. Adopting a generic workflow engine or messaging system as the primary Hub implementation would require bending the spec; build the Hub state machine directly.
2. **Go Hub port priority.** The Python Hub validates the protocol first. The Go port should be behavior-identical; using the same protobuf + store schema makes cross-language parity feasible.
3. **Durable execution for the Master.** Temporal/Cadence/Dapr are powerful but heavy. Defer until there is a concrete requirement for Master-agent crash recovery mid-batch.
4. **JSON-RPC library choice.** No surveyed Go JSON-RPC library understands the spec's bidirectional credit/heartbeat/drain frames. Writing a small codec is simpler and safer.
5. **WebSocket library.** `coder/websocket` is recommended for new code, but the team should choose `gorilla/websocket` if existing code/reviews favor it. Either is production-ready.
6. **Dependency pinning.** The repo policy requires upper bounds (`>=floor,<next_major`). When adding Go modules to the project, pin accordingly and regenerate lockfiles if using a Go-module-aware build.

---

## Sources

All libraries and claims above are linked inline. The primary sources used are:

- gRPC Go: https://github.com/grpc/grpc-go
- Connect-go: https://github.com/connectrpc/connect-go
- Buf: https://github.com/bufbuild/buf
- Coder WebSocket: https://github.com/coder/websocket
- Gorilla WebSocket: https://github.com/gorilla/websocket
- gobwas/ws: https://github.com/gobwas/ws
- Temporal Go SDK: https://github.com/temporalio/sdk-go
- Cadence: https://github.com/uber/cadence
- Dapr Durable Task Go: https://github.com/dapr/durabletask-go
- River: https://github.com/riverqueue/river
- looplab/fsm: https://github.com/looplab/fsm
- NATS Go: https://github.com/nats-io/nats.go
- memberlist: https://github.com/hashicorp/memberlist
- OpenTelemetry Go: https://github.com/open-telemetry/opentelemetry-go
- otelgrpc: https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc
- Prometheus client_golang: https://github.com/prometheus/client_golang

