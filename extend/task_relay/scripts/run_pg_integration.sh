#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
COMPOSE_FILE="$ROOT/docker-compose.test.yml"
export TASK_RELAY_TEST_PG="${TASK_RELAY_TEST_PG:-postgres://relay:relay@127.0.0.1:5433/relay_test}"

cd "$ROOT"
docker compose -f "$COMPOSE_FILE" up -d --wait

cleanup() {
  docker compose -f "$COMPOSE_FILE" down -v
}
trap cleanup EXIT

cd "$(dirname "$ROOT")/.."
PYTHONPATH=. uv run --extra task-relay pytest \
  -o addopts= \
  extend/task_relay/tests/test_m3_postgres.py -v "$@"

echo "== Go Hub Postgres integration =="
(cd "$ROOT/hub/go" && go test -tags=integration ./internal/store/ -run TestPostgres -v -count=1)
