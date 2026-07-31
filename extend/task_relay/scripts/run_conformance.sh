#!/usr/bin/env bash
# Cross-language Task Relay conformance runner (Python Hub + Go Master SDK).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$ROOT/../.." && pwd)"
cd "$REPO_ROOT"

PY="PYTHONPATH=. uv run --extra task-relay pytest -o addopts="

run_py() {
  echo "== $1 =="
  $PY extend/task_relay/tests/$2 -q
}

run_py "Python Mode A E2E" "test_e2e_mode_a.py"
run_py "M2 Mode C push" "test_m2_mode_c.py"
run_py "M2 wake" "test_m2_wake.py"
run_py "M2 ContextRef" "test_m2_context_ref.py"
run_py "M3 orchestration" "test_m3_orchestration.py"
run_py "M3 signed ContextRef" "test_m3_context_ref_sign.py"
run_py "M3 security (encrypt + audit)" "test_m3_security.py"
run_py "gRPC watch semantics" "test_grpc_watch.py"

echo "== Go Master SDK E2E =="
"$ROOT/scripts/run_go_master_e2e.sh"

echo "== Go Hub router unit tests =="
(cd "$ROOT/hub/go" && go test ./...)

echo "== Go Hub scaffold build =="
(cd "$ROOT/hub/go" && go build ./...)

echo "Conformance suite passed."
