#!/usr/bin/env bash
# Cross-language Task Relay conformance runner.
# Default: Python Hub + Python tests + Go Master SDK against Python Hub.
# --hub=go: Go Hub unit/integration tests + Go Master SDK against Go Hub.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$ROOT/../.." && pwd)"
cd "$REPO_ROOT"

HUB="python"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --hub=*)
      HUB="${1#*=}"
      shift
      ;;
    --hub)
      HUB="${2:-python}"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      echo "usage: $0 [--hub=python|go]" >&2
      exit 2
      ;;
  esac
done

run_py() {
  echo "== $1 =="
  PYTHONPATH=. uv run --extra task-relay pytest -o addopts= "extend/task_relay/tests/$2" -q
}

run_python_conformance() {
  run_py "Python Mode A E2E" "test_e2e_mode_a.py"
  run_py "TaskWorker two-step E2E" "test_e2e_two_step_worker.py"
  run_py "Cancel during tool E2E" "test_e2e_cancel_during_tool.py"
  run_py "Worker unit tests" "test_worker.py"
  run_py "WS poll unit tests" "test_ws_poll.py"
  run_py "Resource probe" "test_resource_probe.py"
  run_py "Structured output" "test_structured_output.py"
  run_py "Remote ACP backend" "test_remote_acp_backend.py"
  run_py "ACP RPC server" "test_acp_rpc_server.py"
  run_py "M2 Mode C push" "test_m2_mode_c.py"
  run_py "M2 wake" "test_m2_wake.py"
  run_py "M2 ContextRef" "test_m2_context_ref.py"
  run_py "M3 orchestration" "test_m3_orchestration.py"
  run_py "M3 signed ContextRef" "test_m3_context_ref_sign.py"
  run_py "M3 security (encrypt + audit)" "test_m3_security.py"
  run_py "M3 two-step poll" "test_m3_two_step_poll.py"
  run_py "M3 metrics" "test_m3_metrics.py"
  run_py "M3 TLS" "test_m3_tls.py"
  run_py "Cancel grace" "test_cancel.py"
  run_py "Watch SlowConsumer" "test_event_bus.py"
  run_py "gRPC watch semantics" "test_grpc_watch.py"

  echo "== Go Master SDK E2E (Python Hub) =="
  HUB=python "$ROOT/scripts/run_go_master_e2e.sh"
}

run_go_conformance() {
  export TASK_RELAY_HUB=go
  echo "== Go Hub unit + integration tests =="
  (cd "$ROOT/hub/go" && go test ./... -count=1)

  echo "== Go Hub build =="
  (cd "$ROOT/hub/go" && go build ./...)

  echo "== Go Master SDK E2E (Go Hub) =="
  HUB=go "$ROOT/scripts/run_go_master_e2e.sh"

  echo "== Go Master mTLS E2E (Go Hub) =="
  "$ROOT/scripts/run_go_hub_mtls_e2e.sh"

  run_py "Python Mode A E2E" "test_e2e_mode_a.py"
  run_py "TaskWorker two-step E2E" "test_e2e_two_step_worker.py"
  run_py "Cancel during tool E2E" "test_e2e_cancel_during_tool.py"
  run_py "Worker unit tests" "test_worker.py"
  run_py "WS poll unit tests" "test_ws_poll.py"
  run_py "Resource probe" "test_resource_probe.py"
  run_py "Structured output" "test_structured_output.py"
  run_py "Remote ACP backend" "test_remote_acp_backend.py"
  run_py "ACP RPC server" "test_acp_rpc_server.py"
  run_py "M2 Mode C push" "test_m2_mode_c.py"
  run_py "M2 wake" "test_m2_wake.py"
  run_py "M2 ContextRef" "test_m2_context_ref.py"
  run_py "M3 orchestration" "test_m3_orchestration.py"
  run_py "M3 signed ContextRef" "test_m3_context_ref_sign.py"
  run_py "M3 security (encrypt + audit)" "test_m3_security.py"
  run_py "M3 two-step poll" "test_m3_two_step_poll.py"
  run_py "M3 metrics" "test_m3_metrics.py"
  run_py "M3 TLS" "test_m3_tls.py"
  run_py "Cancel grace" "test_cancel.py"
  run_py "Watch SlowConsumer" "test_event_bus.py"
  run_py "gRPC watch semantics" "test_grpc_watch.py"

  run_py "Go Hub token HTTP E2E" "test_e2e_go_hub_token_http.py"
  run_py "Go Hub event bus E2E" "test_e2e_go_hub_event_bus.py"
  run_py "Go Hub orchestration E2E" "test_e2e_go_hub_orchestration.py"
  run_py "Go Hub lifecycle E2E" "test_e2e_go_hub_lifecycle.py"
  run_py "Go Hub wake E2E" "test_e2e_go_hub_wake.py"
  run_py "Go Hub Mode C E2E" "test_e2e_go_hub_mode_c.py"
  run_py "Go Hub WS poll E2E" "test_e2e_go_hub_ws_poll.py"
  run_py "Go Hub security E2E" "test_e2e_go_hub_security.py"
  run_py "Go Hub metrics E2E" "test_e2e_go_hub_metrics.py"
  run_py "Go Hub two-step worker E2E" "test_e2e_go_hub_two_step_worker.py"
  run_py "Go Hub cancel during tool E2E" "test_e2e_go_hub_cancel_during_tool.py"
}

	case "$HUB" in
  python)
    run_python_conformance
    echo "== Go Hub unit tests (python conformance tail) =="
    (cd "$ROOT/hub/go" && go test ./... -count=1)
    ;;
  go)
    run_go_conformance
    ;;
  *)
    echo "unsupported hub: $HUB (use python or go)" >&2
    exit 2
    ;;
esac

echo "Conformance suite passed (hub=$HUB)."
