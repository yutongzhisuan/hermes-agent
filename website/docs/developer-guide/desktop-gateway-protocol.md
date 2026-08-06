---
sidebar_position: 8
title: "Desktop ↔ Agent Gateway Protocol"
description: "How XHermes Desktop talks to the agent: WebSocket JSON-RPC, method catalog, event stream, and UI mapping"
---

# Desktop ↔ Agent Gateway Protocol

This guide explains how **XHermes Desktop** drives the same `AIAgent` core as the CLI/TUI — over **WebSocket + JSON-RPC 2.0** — and how live turn progress (tokens, tools, approvals) reaches the React UI.

Use it when you need to:

- Add or change a Desktop RPC / event
- Debug “prompt sent but UI stuck”
- Understand why Desktop is **not** a PTY embed of the TUI
- Build a custom client that speaks the same gateway protocol

Related reading: [Programmatic Integration](./programmatic-integration.md) (protocol comparison), [Agent Loop Internals](./agent-loop.md) (what runs inside `AIAgent`), [Desktop Engineering Guide](https://github.com/NousResearch/xhermes-agent/blob/main/apps/desktop/AGENTS.md).

---

## 1. Three parties (authority split)

| Party | Owns | Must not own |
|-------|------|--------------|
| **Electron main** | Process spawn, ports, tokens, native FS/git, IPC bridge | Agent loop, tool execution |
| **Renderer (React)** | Navigation, transcript UI, ephemeral interaction state | Re-implementing agent/tools |
| **Backend (`xhermes serve`)** | Sessions, `AIAgent`, tools, streaming events | Window chrome |

Desktop is a **native chat surface**. It does **not** embed `xhermes --tui` over a PTY (that path is the dashboard `/chat` tab). Desktop and TUI share the **same** `tui_gateway` method handlers and event types; only the transport differs (WebSocket vs stdio).

```text
┌────────────────────┐     IPC (capabilities)      ┌─────────────────────┐
│  Electron main     │◄───────────────────────────►│  Renderer (React)    │
│  spawn / token /   │                             │  JsonRpcGatewayClient│
│  getConnection()   │                             │  transcript + tools  │
└─────────┬──────────┘                             └──────────┬──────────┘
          │ spawns `xhermes serve`                            │
          │ ws://127.0.0.1:<port>/api/ws?token=…              │
          ▼                                                   │
┌─────────────────────────────────────────────────────────────┴──────────┐
│  Headless gateway (FastAPI)                                            │
│    /api/ws  →  tui_gateway.ws.handle_ws  →  tui_gateway.server.dispatch│
│         │                                                              │
│         ▼                                                              │
│    AIAgent (run_agent.py) + tools/ + session DB                        │
└────────────────────────────────────────────────────────────────────────┘
```

---

## 2. Packages and key files

### Shared transport (Desktop + web consumers)

| Package / file | Role |
|----------------|------|
| `apps/shared` (`@xhermes/shared`) | Framework-agnostic WebSocket JSON-RPC client |
| `apps/shared/src/json-rpc-gateway.ts` | `JsonRpcGatewayClient` — connect, `request()`, event fan-out |
| `apps/shared/src/websocket-url.ts` | `resolveGatewayWsUrl` / `buildHermesWebSocketUrl` (token & OAuth ticket) |

### Desktop Electron (machine / boot)

| File | Role |
|------|------|
| `apps/desktop/electron/backend-command.ts` | Build `serve` argv; fall back to `dashboard --no-open` on old runtimes |
| `apps/desktop/electron/main.ts` | Spawn backend, mint session token, expose `getConnection` / WS URL |
| `apps/desktop/electron/connection-config.ts` | Local vs remote gateway URL construction |
| `apps/desktop/electron/backend-ready.ts` | Wait for `XHERMES_BACKEND_READY` (or legacy dashboard ready) |

### Desktop renderer (experience)

| File | Role |
|------|------|
| `apps/desktop/src/app/gateway/hooks/use-gateway-boot.ts` | Connect, subscribe, reconnect / re-mint OAuth tickets |
| `apps/desktop/src/app/gateway/hooks/use-gateway-request.ts` | `requestGateway(method, params)` wrapper |
| `apps/desktop/src/app/session/hooks/use-prompt-actions/` | Submit / slash / rewind → `prompt.submit` / `slash.exec` |
| `apps/desktop/src/app/session/hooks/use-message-stream/` | Map gateway events → assistant-ui message parts |
| `apps/desktop/src/app/session/hooks/use-message-stream/gateway-event.ts` | Per-event handlers (`tool.*`, `message.*`, …) |
| `apps/desktop/src/lib/gateway-events.ts` | Session routing for unscoped stream events |
| `apps/desktop/src/app/tool/fallback.tsx` | Default tool-call UI row |

### Backend gateway

| File | Role |
|------|------|
| `tui_gateway/ws.py` | WebSocket accept, `gateway.ready`, NDJSON frames, delta coalescing |
| `tui_gateway/server.py` | `dispatch` / `_emit` / agent callbacks / long-handler pool |
| `tui_gateway/methods_prompt.py` | `prompt.submit`, attachments, `approval.respond`, … |
| `tui_gateway/methods_session.py` | `session.create` / `resume` / `interrupt` / history / … |
| `tui_gateway/methods_tools.py` | `slash.exec`, `commands.catalog`, tools/cron/plugins |
| `tui_gateway/methods_config.py` | `config.get` / projects / setup |
| `tui_gateway/methods_complete.py` | `complete.slash` / `complete.path` / model helpers |
| `tui_gateway/method_ctx.py` | `@method` registry + profile scoping |
| `tui_gateway/host_supervisor.py` | Optional compute-host isolation for heavy turns |
| `tui_gateway/entry.py` | Stdio entry (Ink TUI); same `dispatch` as WS |

---

## 3. Backend process model

On local Desktop boot, Electron resolves a Python runtime and starts a **headless** gateway:

```text
xhermes [--profile <name>] serve --host 127.0.0.1 --port 0
```

- `--port 0` → OS picks a free port; readiness announces it (e.g. `XHERMES_BACKEND_READY`).
- Auth for `/api/ws` uses a **session token** (query `?token=…`) or, for remote OAuth gateways, a **single-use ticket** (`?ticket=…`).
- Older managed installs without `serve` fall back to `dashboard --no-open` — same headless JSON-RPC/WS surface, different CLI name.

`serve` sets `headless_backend=True`: no browser SPA; only API + WebSocket.

Remote mode: renderer dials a remote host’s `/api/ws` instead of a locally spawned child (still the same protocol).

---

## 4. Wire protocol

### Transport

- **WebSocket** endpoint: `/api/ws`
- **Framing**: one JSON object per WebSocket text message (same logical NDJSON shape as TUI stdio)
- **TCP**: Nagle disabled on accept so streamed tokens are not delayed
- **High-frequency deltas** (`message.delta`, `thinking.delta`, `reasoning.delta`): coalesced ~30 fps in `WSTransport` to cut event-loop wakeups; non-stream events flush the buffer first so ordering stays correct

Stdio and WebSocket share **identical** JSON-RPC method names and event `type` strings (`tui_gateway/ws.py` reuses `server.dispatch`).

### Request (client → server)

```json
{
  "jsonrpc": "2.0",
  "id": "r42",
  "method": "prompt.submit",
  "params": {
    "session_id": "<runtime-sid>",
    "text": "Fix the flaky test"
  }
}
```

### Success / error response

```json
{ "jsonrpc": "2.0", "id": "r42", "result": { "status": "streaming" } }
```

```json
{
  "jsonrpc": "2.0",
  "id": "r42",
  "error": { "code": 4009, "message": "session busy …" }
}
```

Long-running handlers (`prompt.submit`, `session.resume`, `slash.exec`, …) are scheduled on a **thread pool**. `dispatch()` returns `null` to the WS loop; the worker writes the response (and any events) on the bound transport.

### Server-push event (server → client)

Not a JSON-RPC *response* — a notification with `method: "event"`:

```json
{
  "jsonrpc": "2.0",
  "method": "event",
  "params": {
    "type": "tool.start",
    "session_id": "<runtime-sid>",
    "payload": {
      "tool_id": "call_abc",
      "name": "terminal",
      "context": "…"
    }
  }
}
```

Client library maps `params` into `GatewayEvent` (`type`, `session_id`, `payload`) and fans out via `onEvent` / `on(type)`.

### Connection hello

Immediately after accept, the server sends:

```json
{
  "jsonrpc": "2.0",
  "method": "event",
  "params": {
    "type": "gateway.ready",
    "payload": {
      "skin": { "...": "..." },
      "change_events": true
    }
  }
}
```

`change_events: true` lets Desktop demote legacy polls for pets/cron/sessions to slow backstops.

---

## 5. Connection lifecycle (Desktop)

```text
1. Electron spawns backend → ready port + auth token
2. Renderer: desktop.getConnection(profile) → { wsUrl, authMode, … }
3. resolveGatewayWsUrl() — re-mint OAuth ticket if needed
4. JsonRpcGatewayClient.connect(wsUrl)
5. gateway.onEvent(handleGatewayEvent)
6. Wait for gateway.ready → refresh config / sessions
7. On close/error → backoff reconnect (re-mint ticket; reset stale runtime ids)
```

Primary hooks:

- Boot / reconnect: `use-gateway-boot.ts`
- RPC calls: `use-gateway-request.ts` → `gateway.request(method, params)`

---

## 6. RPC method catalog (by concern)

Handlers live under `tui_gateway/methods_*.py` and are registered with `@method("…")`. Below is the **learning-oriented** grouping (not every billing/pet edge method).

### Session lifecycle

| Method | Purpose |
|--------|---------|
| `session.create` | Open a new live session |
| `session.resume` | Reattach + hydrate history |
| `session.list` / `session.most_recent` | Discover saved sessions |
| `session.active_list` / `session.activate` / `session.close` | Process-local live sessions |
| `session.interrupt` | Cancel in-flight turn |
| `session.delete` / `session.title` / `session.cwd.set` | Manage metadata / cwd |
| `session.history` / `session.compress` / `session.branch` / `session.undo` | History ops |
| `session.status` / `session.usage` / `session.steer` | Status / usage / mid-turn steer |
| `session.redirect` | Move/redirect session binding |

### Prompt & attachments

| Method | Purpose |
|--------|---------|
| `prompt.submit` | **Main chat turn** — returns quickly with `{ status: "streaming" }`; work continues via events |
| `prompt.background` | Background a turn |
| `image.attach` / `image.attach_bytes` / `image.detach` / `pdf.attach` / `file.attach` | Attachments |
| `clipboard.paste` / `input.detect_drop` | Clipboard / drag-drop helpers |

### Interactive blocks (human-in-the-loop)

| Method | Matches event |
|--------|---------------|
| `clarify.respond` | `clarify.request` |
| `approval.respond` | `approval.request` |
| `sudo.respond` | `sudo.request` |
| `secret.respond` | `secret.request` |
| `terminal.read.respond` | Terminal read prompts |

### Slash commands & tools config

| Method | Purpose |
|--------|---------|
| `slash.exec` | Run slash / skill pipeline in slash worker |
| `commands.catalog` / `complete.slash` / `complete.path` | Palette + completions |
| `command.resolve` / `command.dispatch` | Resolve / dispatch directives |
| `cli.exec` / `shell.exec` | CLI / shell helpers |
| `tools.list` / `tools.show` / `tools.configure` / `toolsets.list` | Tool surface |
| `reload.mcp` / `reload.env` | Hot reload |

### Config & projects

| Method | Purpose |
|--------|---------|
| `config.get` / `config.set` / `config.show` | Config read/write |
| `projects.*` | Repo discovery / project tree / project sessions |
| `setup.status` / `setup.runtime_check` | First-run / runtime checks |

### Subagents / cron / misc

| Method | Purpose |
|--------|---------|
| `delegation.status` / `delegation.pause` / `subagent.interrupt` | Delegation control |
| `spawn_tree.*` | Persist/load spawn trees |
| `cron.manage` | Cron from Desktop |
| `process.list` / `process.stop` / `process.kill` | Background processes |
| `wake.*` / `voice.*` | Wake word / voice (backend-owned; Desktop voice often renderer-local) |

---

## 7. Event catalog (server → UI)

Events are emitted with `_emit(type, session_id, payload)` in `tui_gateway/server.py`.

### Turn / text

| Event | Meaning |
|-------|---------|
| `message.start` | Assistant turn begins |
| `message.delta` | Streamed answer tokens |
| `message.interim` | Interim / preview text |
| `message.complete` | Turn finished (final text / status) |
| `thinking.delta` | Model “thinking” stream |
| `reasoning.delta` / `reasoning.available` | Reasoning content |

### Tools (progress visibility)

| Event | Meaning |
|-------|---------|
| `tool.generating` | Model is drafting a tool call (name may be known) |
| `tool.start` | Tool execution started (`tool_id`, `name`, `context`, optional `args_text`) |
| `tool.progress` | Mid-execution progress / risk metadata |
| `tool.complete` | Finished (`result` / `summary` / `duration_s` / optional `inline_diff`, `todos`) |

Gating: `display.tool_progress` in `config.yaml` (`all` / `verbose` / `off`). When `off`, most tool events are suppressed, but tools required for UI (e.g. clarify) still emit via `_tool_lifecycle_required_for_ui`.

### Human prompts & status

| Event | Meaning |
|-------|---------|
| `approval.request` / `clarify.request` / `sudo.request` / `secret.request` | Blocking prompts |
| `status.update` | Status line / activity |
| `session.info` | Session snapshot (running flag, model, …) |
| `error` | Turn / session error |
| `gateway.ready` / `skin.changed` | Connection / theme |
| `background.complete` | Background job finished |
| `notification.show` / `notification.clear` | Transient notices |
| `subagent.*` | Child-agent mirror / lifecycle (must keep `session_id` semantics) |

---

## 8. End-to-end: one user message

### Sequence

```text
User types in composer
        │
        ▼
use-prompt-actions / submit.ts
  requestGateway('prompt.submit', { session_id, text, … })
        │
        ▼
JsonRpcGatewayClient.request  ──WS──►  handle_ws → dispatch
        │
        ▼
methods_prompt.prompt.submit
  • sanitize text
  • ensure session slot / DB row
  • start deferred agent build if needed
  • spawn run thread → _run_prompt_submit
  • RPC returns { status: "streaming" }   ◄── UI already shows pending turn
        │
        ▼
AIAgent.run_conversation (callbacks from _agent_cbs)
  stream_callback      → message.delta
  thinking_callback    → thinking.delta
  reasoning_callback   → reasoning.delta
  tool_gen_callback    → tool.generating
  tool_start_callback  → tool.start
  tool_progress_…      → tool.progress
  tool_complete_…      → tool.complete
  (clarify/approval)   → *.request  → user RPC *.respond → resume
        │
        ▼
_emit → write_json → WSTransport → renderer onEvent
        │
        ▼
useMessageStream / gateway-event.ts
  appendAssistantDelta / upsertToolCall / completeAssistantMessage
        │
        ▼
@assistant-ui transcript
  text parts + tool-call parts → ToolFallback / specialty tools
```

### Important semantics

1. **`prompt.submit` is not the answer.** Success means “accepted; watch the event stream.”
2. **Events are authoritative for live UI.** History hydration on `session.resume` fills the transcript after reconnect; live tools/tokens come from events.
3. **Busy sessions** refuse overlapping submits (error codes like `4009`); Desktop queues or blocks in the composer.
4. **Interrupt** is `session.interrupt`, not closing the WebSocket.
5. **Unscoped stream events** (missing `session_id`) are pinned to the session that saw `message.start` so mid-turn chat switches do not steal deltas (`gateway-events.ts`).

### Agent callback wiring (principle)

The gateway does not scrape stdout. It injects callbacks when constructing/running the agent (`_agent_cbs` in `server.py`):

```text
AIAgent tool/stream hooks
        → _on_tool_start / _on_tool_complete / …
        → _emit("tool.start" | "tool.complete" | …)
        → JSON-RPC event frame on the session’s transport
```

That is why Desktop and Ink TUI see the **same** tool lifecycle: one emitter, two transports.

---

## 9. Frontend mapping (how progress is shown)

| Gateway event | Renderer effect (typical) |
|---------------|---------------------------|
| `message.delta` | Append streaming text to the active assistant bubble |
| `tool.generating` | “Drafting tool…” activity (cleared by any superseding output) |
| `tool.start` / `tool.progress` | `upsertToolCall(…, 'running')` → tool-call part in the message |
| `tool.complete` | `upsertToolCall(…, 'complete')` → result / summary / diff |
| `clarify.request` | Mount clarify tool UI (even if `tool.start` was missed) |
| `approval.request` | Approval dialog / inline prompt |
| `message.complete` | Seal bubble; settle pending; refresh session side effects |
| `session.info` (`running: false`) | Settle stale pending when `message.complete` never arrives |

UI components:

- Default tool row: `apps/desktop/src/app/tool/fallback.tsx`
- Run summaries: `apps/desktop/src/app/tool/run-summary.ts`
- Status / activity timer: thread status components under `apps/desktop/src/app/thread/`

**Yes — processing is shown to the frontend by design:** tool names, running state, summaries, streaming text, reasoning/thinking (when emitted), and blocking prompts. Verbosity is tunable via `display.tool_progress`.

---

## 10. Slash-command path (Desktop-specific curation)

Backend already exposes built-ins + skills + `quick_commands` via `commands.catalog` / `complete.slash`.

Desktop **curates discovery** (not execution) in `apps/desktop/src/lib/desktop-slash-commands.ts`:

- Hide terminal-only / messaging-only noise from the popover
- Still allow skill / quick-command **extensions** through suggestion filters

Dispatch (`use-prompt-actions/slash.ts`):

1. Local Desktop-owned commands (`/help`, `/new`, `/skin`, …)
2. Else `slash.exec`
3. Else `command.dispatch` (skills → often become a normal `prompt.submit` with an expanded message)

---

## 11. Comparison with other surfaces

| Surface | How it talks to the agent | Progress UI |
|---------|---------------------------|-------------|
| **Desktop** | WS JSON-RPC → `tui_gateway` | Structured React tool rows + stream |
| **Ink TUI** | stdio JSON-RPC → same `dispatch` | Ink thinking/tool trail |
| **Dashboard `/chat`** | Embeds `xhermes --tui` over **PTY** | Terminal bytes (not structured events) |
| **Messaging gateway** | Platform adapters → `GatewayRunner` → `AIAgent` | Platform messages / tool progress config |
| **ACP** | ACP JSON-RPC stdio (`acp_adapter/`) | IDE tool-call blocks |

Principle: **one agent core, multiple hosts.** Prefer extending gateway events/methods over re-implementing the loop in React.

---

## 12. Debugging checklist

| Symptom | Check |
|---------|--------|
| Composer stuck on “Starting…” | Backend spawn / `XHERMES_BACKEND_READY`; WS connect; OAuth ticket expiry |
| Submit returns streaming but no UI | Events missing `session_id` routing; turn cancelled before `message.start`; disk-full errors on persist |
| Tools never appear | `display.tool_progress: off`; filter for `tool.start` on the wire |
| Duplicate prompts | Guard against double `prompt.submit` (queue / optimistic submit paths) |
| Mid-turn switch steals stream | `resolveGatewayEventSessionId` / unscoped stream pinning |
| Method not found | Backend too old — `isMissingRpcMethod` in `gateway-rpc.ts` |

Useful local inspection:

- Watch WS frames in DevTools (renderer) Network → WS
- Backend logs under the active profile’s `~/.xhermes/logs/` (profile-aware via `get_hermes_home()`)
- Compare with Ink TUI on the same method — if TUI works and Desktop does not, bug is likely in renderer event mapping

---

## 13. Minimal mental model

```text
RPC  = control plane  (submit, interrupt, respond, config)
Event stream = data plane (tokens, tools, prompts, settle)
Transport = WebSocket (Desktop) or stdio (TUI)
Runtime = tui_gateway + AIAgent
UI = cache of events + resumed history — never the source of agent truth
```

When adding a feature:

1. Prefer a new **event** or **RPC** on `tui_gateway` (shared by TUI + Desktop + future clients).
2. Map it once in `gateway-event.ts`.
3. Keep the renderer free of agent/tool business logic.
4. Preserve prompt-cache invariants inside `AIAgent` (do not rebuild system prompt mid-turn from the UI).
