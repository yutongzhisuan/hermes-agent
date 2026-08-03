#!/usr/bin/env bash
# xhermes-agent/extend/task_relay/scripts/gen_proto.sh
#
# Regenerates Python stubs for proto/task_relay_v1.proto into gen/py/.
#
# Working command (verified in this repo's uv venv, Python 3.11):
#   .venv/bin/python -m grpc_tools.protoc \
#     -I extend/task_relay/proto \
#     --python_out=extend/task_relay/gen/py \
#     --grpclib_python_out=extend/task_relay/gen/py \
#     extend/task_relay/proto/task_relay_v1.proto
#
# Notes:
# - grpclib ships its own protoc plugin as the console script
#   `protoc-gen-grpclib_python`; grpc_tools' protoc picks it up when the
#   venv's bin/ directory is on PATH (this script prepends it).
# - The grpclib plugin emits `import task_relay_v1_pb2` (top-level absolute),
#   which breaks when the stubs are imported as a package
#   (`extend.task_relay.gen.py.task_relay_v1_grpc`). The script rewrites that
#   one line to a package-relative import after codegen.
# - Deps live in the `task-relay` optional-deps group in pyproject.toml:
#   grpclib (runtime + plugin), protobuf (runtime), grpcio-tools (codegen
#   only), aiosqlite (Hub persistence, later tasks).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/gen/py"
REPO_ROOT="$(cd "$ROOT/../.." && pwd)"
mkdir -p "$OUT"
# Put the repo venv on PATH so protoc finds protoc-gen-grpclib_python and
# `python` resolves to the venv interpreter.
export PATH="$REPO_ROOT/.venv/bin:$PATH"
python -m grpc_tools.protoc \
  -I "$ROOT/proto" \
  --python_out="$OUT" \
  --grpclib_python_out="$OUT" \
  "$ROOT/proto/task_relay_v1.proto"
# grpclib's plugin emits a top-level `import task_relay_v1_pb2`; rewrite it to
# a package-relative import so the stubs are importable as
# extend.task_relay.gen.py.task_relay_v1_grpc.
python - "$OUT/task_relay_v1_grpc.py" <<'PYEOF'
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
text = path.read_text()
lines = text.splitlines(keepends=True)
new_lines = []
replaced = 0
already_relative = False
for line in lines:
    if line == "import task_relay_v1_pb2\n":
        line = "from . import task_relay_v1_pb2\n"
        replaced += 1
    elif line == "from . import task_relay_v1_pb2\n":
        already_relative = True
    new_lines.append(line)
if replaced == 0 and not already_relative:
    print("expected top-level import task_relay_v1_pb2 not found", file=sys.stderr)
    sys.exit(1)
path.write_text("".join(new_lines))
PYEOF
touch "$OUT/__init__.py"
touch "$(dirname "$OUT")/__init__.py"
