#!/usr/bin/env bash
# Build offline install bundles for the headless wheel:
#   frozen  — relocatable venv with all deps (extract and run bin/xhermes)
#   vendored — wheels/ + install.sh for air-gapped pip install
#   all     — both artifacts (default)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/offline_common.sh
source "$ROOT/scripts/offline_common.sh"

MODE="${1:-all}"
PYTHON="${PYTHON:-3.11}"
PLATFORM="$(offline_platform_tag)"
STAGING="$ROOT/dist/.offline-staging-$$"
WHEEL_DIR="$STAGING/wheel-out"
mkdir -p "$STAGING"

cleanup() {
  rm -rf "$STAGING"
}
trap cleanup EXIT

WHEEL="$(offline_build_headless_wheel "$ROOT" "$WHEEL_DIR")"
VERSION="$(basename "$WHEEL" | sed -E 's/xhermes_agent-([0-9.]+)-.*/\1/')"
mkdir -p "$ROOT/dist"

build_frozen() {
  local bundle_root="$STAGING/frozen/xhermes-agent-frozen-py${PYTHON}-${PLATFORM}"
  local out="$ROOT/dist/xhermes-agent-frozen-py${PYTHON}-${PLATFORM}.tar.gz"
  mkdir -p "$bundle_root/bin"
  uv venv "$bundle_root/venv" --python "$PYTHON" --relocatable
  uv pip install --python "$bundle_root/venv/bin/python" "$WHEEL"

  cat >"$bundle_root/bin/xhermes" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
exec "$ROOT/venv/bin/xhermes" "$@"
EOF
  cat >"$bundle_root/bin/serve" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
exec "$ROOT/venv/bin/xhermes" serve "$@"
EOF
  chmod +x "$bundle_root/bin/xhermes" "$bundle_root/bin/serve"

  offline_write_manifest \
    "$bundle_root/manifest.json" \
    "frozen-venv" \
    "$VERSION" \
    "$PYTHON" \
    "$PLATFORM" \
    "$(basename "$out")" \
    "Extract anywhere on matching OS/arch; run bin/xhermes or bin/serve. Target host needs compatible Python ${PYTHON} runtime used to create the venv."

  cat >"$bundle_root/README.txt" <<EOF
XHermes headless offline bundle (frozen venv)
Version: ${VERSION}
Python: ${PYTHON}
Platform: ${PLATFORM}

Usage:
  tar xzf $(basename "$out")
  cd xhermes-agent-frozen-py${PYTHON}-${PLATFORM}
  ./bin/serve --host 127.0.0.1 --port 0

The venv is pre-populated with xhermes-agent and all core dependencies.
EOF

  tar -czf "$out" -C "$STAGING/frozen" "xhermes-agent-frozen-py${PYTHON}-${PLATFORM}"
  echo "Frozen bundle: $out ($(du -h "$out" | awk '{print $1}'))"
}

build_vendored() {
  local bundle_root="$STAGING/vendored/xhermes-agent-vendored-py${PYTHON}-${PLATFORM}"
  local out="$ROOT/dist/xhermes-agent-vendored-py${PYTHON}-${PLATFORM}.tar.gz"
  mkdir -p "$bundle_root/wheels"
  uv run --with pip python -m pip download -d "$bundle_root/wheels" "$WHEEL"
  cp "$ROOT/scripts/offline/install-vendored.sh" "$bundle_root/install.sh"
  chmod +x "$bundle_root/install.sh"

  offline_write_manifest \
    "$bundle_root/manifest.json" \
    "vendored-wheels" \
    "$VERSION" \
    "$PYTHON" \
    "$PLATFORM" \
    "$(basename "$out")" \
    "Air-gapped install via ./install.sh (creates ./venv). Target needs Python ${PYTHON}+ with venv and pip."

  cat >"$bundle_root/README.txt" <<EOF
XHermes headless offline bundle (vendored wheels)
Version: ${VERSION}
Python: ${PYTHON}
Platform: ${PLATFORM}

Usage:
  tar xzf $(basename "$out")
  cd xhermes-agent-vendored-py${PYTHON}-${PLATFORM}
  ./install.sh
  ./venv/bin/xhermes serve --host 127.0.0.1 --port 0

All dependency wheels are included under wheels/ for offline pip install.
EOF

  tar -czf "$out" -C "$STAGING/vendored" "xhermes-agent-vendored-py${PYTHON}-${PLATFORM}"
  echo "Vendored bundle: $out ($(du -h "$out" | awk '{print $1}'))"
}

case "$MODE" in
  frozen) build_frozen ;;
  vendored) build_vendored ;;
  all)
    build_frozen
    build_vendored
    ;;
  *)
    echo "Usage: $0 [frozen|vendored|all]" >&2
    exit 1
    ;;
esac
