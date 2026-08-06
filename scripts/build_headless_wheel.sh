#!/usr/bin/env bash
# Build the headless pip wheel (backend only — no TUI/Desktop SPA assets).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

export XHERMES_HEADLESS_WHEEL_BUILD=1

if command -v uv >/dev/null 2>&1; then
  PYTHON=(uv run python)
else
  PYTHON=(python3)
fi

rm -rf dist/
mkdir -p dist
"${PYTHON[@]}" -c "from setuptools.build_meta import build_wheel; print(build_wheel('dist/'))"

wheel="$(ls -1 dist/xhermes_agent-*.whl 2>/dev/null | head -1)"
if [[ -z "${wheel:-}" ]]; then
  wheel="$(ls -1 dist/*.whl 2>/dev/null | head -1)"
fi
if [[ -z "${wheel:-}" ]]; then
  echo "ERROR: no wheel produced under dist/" >&2
  exit 1
fi

echo "Built: $wheel ($(du -h "$wheel" | awk '{print $1}'))"
echo "Top-level wheel contents (sample):"
"${PYTHON[@]}" - <<'PY' "$wheel"
import sys
import zipfile
from collections import Counter

wheel_path = sys.argv[1]
with zipfile.ZipFile(wheel_path) as zf:
    tops = Counter(p.split("/", 1)[0] for p in zf.namelist())
for name, count in sorted(tops.items(), key=lambda x: (-x[1], x[0]))[:25]:
    print(f"  {name}/  ({count} entries)")
for forbidden in ("apps/desktop", "ui-tui/", "website/", "web_dist/"):
    hits = [p for p in zf.namelist() if forbidden in p]
    if hits:
        print(f"FAIL: forbidden path fragment {forbidden!r} in wheel", file=sys.stderr)
        sys.exit(1)
PY
