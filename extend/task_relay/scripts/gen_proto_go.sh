#!/usr/bin/env bash
# Regenerate Go stubs for proto/task_relay_v1.proto into gen/go/.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT/proto"
buf generate
