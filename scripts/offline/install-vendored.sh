#!/usr/bin/env bash
# Offline install for the vendored-wheels bundle (air-gapped target).
# Requires Python 3.11+ on the target host; creates a local venv beside this script.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PYTHON="${PYTHON:-python3}"
VENV="${VENV:-$ROOT/venv}"
WHEELS="$ROOT/wheels"

if [[ ! -d "$WHEELS" ]]; then
  echo "ERROR: missing wheels/ directory next to install.sh" >&2
  exit 1
fi

XHERMES_WHEEL="$(ls -1 "$WHEELS"/xhermes_agent-*.whl 2>/dev/null | head -1)"
if [[ -z "${XHERMES_WHEEL:-}" ]]; then
  echo "ERROR: xhermes_agent-*.whl not found under wheels/" >&2
  exit 1
fi

if [[ ! -x "$VENV/bin/python" ]]; then
  "$PYTHON" -m venv "$VENV"
fi

PIP="$VENV/bin/pip"
if [[ ! -x "$PIP" ]]; then
  echo "ERROR: venv pip missing; ensure Python includes venv+pip support" >&2
  exit 1
fi

"$PIP" install --no-index --find-links="$WHEELS" "$XHERMES_WHEEL"
echo "Installed xhermes-agent into: $VENV"
echo "Run: $VENV/bin/xhermes serve"
