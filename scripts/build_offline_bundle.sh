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
  local embedded_py
  mkdir -p "$bundle_root/bin"

  # Embed a standalone CPython so the extract host does not need system Python.
  echo "→ embedding standalone Python ${PYTHON} into frozen bundle..."
  embedded_py="$(offline_stage_embedded_python "$bundle_root/python" "$PYTHON")"
  echo "  embedded: ${embedded_py}"

  # Create venv from the embedded interpreter, install into that venv, then
  # rewrite venv/bin/python* to relative symlinks for relocatable extract.
  uv venv "$bundle_root/venv" --python "$embedded_py" --relocatable
  uv pip install --python "$bundle_root/venv/bin/python" "$WHEEL"
  offline_rewire_venv_to_embedded_python "$bundle_root" "$PYTHON"

  # Launch via embedded interpreter + venv site-packages (relocatable; no host Python).
  cat >"$bundle_root/bin/xhermes" <<EOF
#!/usr/bin/env bash
set -euo pipefail
ROOT="\$(cd "\$(dirname "\${BASH_SOURCE[0]}")/.." && pwd)"
if [[ -x "\${ROOT}/python/bin/python${PYTHON}" ]]; then
  PY="\${ROOT}/python/bin/python${PYTHON}"
elif [[ -x "\${ROOT}/python/bin/python3" ]]; then
  PY="\${ROOT}/python/bin/python3"
else
  echo "ERROR: embedded Python ${PYTHON} missing under \${ROOT}/python/bin" >&2
  exit 1
fi
SITE="\$(printf '%s\\n' "\${ROOT}"/venv/lib/python*/site-packages | head -1)"
if [[ -z "\${SITE}" || ! -d "\${SITE}" ]]; then
  echo "ERROR: venv site-packages missing under \${ROOT}/venv" >&2
  exit 1
fi
export VIRTUAL_ENV="\${ROOT}/venv"
export PYTHONPATH="\${SITE}\${PYTHONPATH:+:\${PYTHONPATH}}"
export PATH="\${ROOT}/venv/bin:\${ROOT}/bin:\${PATH}"
exec "\${PY}" "\${ROOT}/venv/bin/xhermes" "\$@"
EOF
  cat >"$bundle_root/bin/serve" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
exec "$ROOT/bin/xhermes" serve "$@"
EOF
  chmod +x "$bundle_root/bin/xhermes" "$bundle_root/bin/serve"

  offline_write_manifest \
    "$bundle_root/manifest.json" \
    "frozen-venv" \
    "$VERSION" \
    "$PYTHON" \
    "$PLATFORM" \
    "$(basename "$out")" \
    "Extract anywhere on matching OS/arch; includes embedded CPython ${PYTHON}. Run bin/xhermes or bin/serve (no host Python required)."

  cat >"$bundle_root/README.txt" <<EOF
XHermes headless offline bundle (frozen venv + embedded Python)
Version: ${VERSION}
Python: ${PYTHON} (embedded under ./python/)
Platform: ${PLATFORM}

Usage:
  tar xzf $(basename "$out")
  cd xhermes-agent-frozen-py${PYTHON}-${PLATFORM}
  ./bin/serve --host 127.0.0.1 --port 0

This bundle includes a standalone CPython ${PYTHON} runtime under ./python/
plus a pre-populated venv with xhermes-agent and all core dependencies.
No system Python install is required on the target host.
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
