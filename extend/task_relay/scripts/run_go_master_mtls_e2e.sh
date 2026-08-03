#!/usr/bin/env bash
# Run Go Master mTLS integration test against a TLS Hub fixture.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$ROOT/../.." && pwd)"
FIXTURE="$ROOT/scripts/e2e_go_master_tls_fixture.py"
PYTHON="${REPO_ROOT}/.venv/bin/python"

if [[ ! -x "$PYTHON" ]]; then
  echo "xhermes-agent .venv not found; run uv sync --extra task-relay first" >&2
  exit 1
fi

OUT="$(mktemp)"
cleanup() {
  rm -f "$OUT"
  if [[ -n "${FIXTURE_PID:-}" ]]; then
    kill "$FIXTURE_PID" 2>/dev/null || true
    wait "$FIXTURE_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

cd "$REPO_ROOT"
PYTHONPATH=. "$PYTHON" "$FIXTURE" >"$OUT" 2>&1 &
FIXTURE_PID=$!

CONFIG=""
for _ in $(seq 1 100); do
  if [[ -s "$OUT" ]]; then
    CONFIG="$(head -1 "$OUT")"
    break
  fi
  sleep 0.1
done

if [[ -z "$CONFIG" ]]; then
  echo "fixture failed to start:" >&2
  cat "$OUT" >&2 || true
  exit 1
fi

export HUB_GRPC_ADDR="$("$PYTHON" -c "import json,sys; print(json.loads(sys.argv[1])['grpc_addr'])" "$CONFIG")"
export MASTER_JWT="$("$PYTHON" -c "import json,sys; print(json.loads(sys.argv[1])['master_jwt'])" "$CONFIG")"
export HUB_TLS_CA="$("$PYTHON" -c "import json,sys; print(json.loads(sys.argv[1])['tls_ca'])" "$CONFIG")"
export HUB_TLS_CERT="$("$PYTHON" -c "import json,sys; print(json.loads(sys.argv[1])['tls_cert'])" "$CONFIG")"
export HUB_TLS_KEY="$("$PYTHON" -c "import json,sys; print(json.loads(sys.argv[1])['tls_key'])" "$CONFIG")"

cd "$ROOT/master/go"
go test -tags=integration ./client/ -run TestGoMasterMTLSDispatch -v -count=1
