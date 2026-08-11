#!/usr/bin/env bash
# Build offline install bundles for the headless wheel:
#   frozen   — relocatable venv + embedded CPython (extract and run bin/xhermes)
#   vendored — wheels/ + install.sh for air-gapped pip install
#   aos      — frozen + runtime libs + ld.so launcher for Huawei AOS (linux-aarch64)
#   all      — frozen + vendored (default)
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

# Populate python/ + venv/ for a frozen-style tree. Prints embedded python path.
offline_populate_frozen_tree() {
  local bundle_root="$1"
  local embedded_py venv_py
  mkdir -p "$bundle_root/bin"

  echo "→ embedding standalone Python ${PYTHON} into bundle..."
  embedded_py="$(offline_stage_embedded_python "$bundle_root/python" "$PYTHON")"
  echo "  embedded: ${embedded_py}"

  uv venv "$(offline_win_path "$bundle_root/venv")" --python "$(offline_win_path "$embedded_py")" --relocatable
  venv_py="$(offline_venv_python_path "$bundle_root")" || {
    echo "ERROR: venv python missing after uv venv under ${bundle_root}/venv" >&2
    exit 1
  }
  uv pip install --python "$(offline_win_path "$venv_py")" "$(offline_win_path "$WHEEL")"
  offline_rewire_venv_to_embedded_python "$bundle_root" "$PYTHON"
}

offline_write_frozen_launcher() {
  local bundle_root="$1"
  # Cross-platform bash launcher (Unix + Git Bash on Windows CI/smoke).
  cat >"$bundle_root/bin/xhermes" <<EOF
#!/usr/bin/env bash
set -euo pipefail
ROOT="\$(cd "\$(dirname "\${BASH_SOURCE[0]}")/.." && pwd)"
PY=""
if [[ -x "\${ROOT}/python/bin/python${PYTHON}" ]]; then
  PY="\${ROOT}/python/bin/python${PYTHON}"
elif [[ -x "\${ROOT}/python/bin/python3" ]]; then
  PY="\${ROOT}/python/bin/python3"
elif [[ -f "\${ROOT}/python/python.exe" ]]; then
  PY="\${ROOT}/python/python.exe"
elif [[ -f "\${ROOT}/python/bin/python.exe" ]]; then
  PY="\${ROOT}/python/bin/python.exe"
else
  echo "ERROR: embedded Python ${PYTHON} missing under \${ROOT}/python" >&2
  exit 1
fi
SITE=""
if [[ -d "\${ROOT}/venv/Lib/site-packages" ]]; then
  SITE="\${ROOT}/venv/Lib/site-packages"
else
  SITE="\$(printf '%s\\n' "\${ROOT}"/venv/lib/python*/site-packages | head -1)"
fi
if [[ -z "\${SITE}" || ! -d "\${SITE}" ]]; then
  echo "ERROR: venv site-packages missing under \${ROOT}/venv" >&2
  exit 1
fi
export VIRTUAL_ENV="\${ROOT}/venv"
export PYTHONPATH="\${SITE}\${PYTHONPATH:+:\${PYTHONPATH}}"
if [[ -d "\${ROOT}/venv/Scripts" ]]; then
  export PATH="\${ROOT}/venv/Scripts:\${ROOT}/bin:\${PATH}"
else
  export PATH="\${ROOT}/venv/bin:\${ROOT}/bin:\${PATH}"
fi
exec "\${PY}" -m hermes_cli.main "\$@"
EOF
  cat >"$bundle_root/bin/serve" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
exec "$ROOT/bin/xhermes" serve "$@"
EOF
  chmod +x "$bundle_root/bin/xhermes" "$bundle_root/bin/serve"

  # Native Windows cmd launchers (cmd.exe / PowerShell users).
  if offline_is_windows || [[ "$PLATFORM" == windows-* ]]; then
    cat >"$bundle_root/bin/xhermes.cmd" <<'EOF'
@echo off
setlocal EnableExtensions
set "ROOT=%~dp0.."
for %%I in ("%ROOT%") do set "ROOT=%%~fI"
set "PY="
if exist "%ROOT%\python\python.exe" set "PY=%ROOT%\python\python.exe"
if not defined PY if exist "%ROOT%\python\bin\python.exe" set "PY=%ROOT%\python\bin\python.exe"
if not defined PY (
  echo ERROR: embedded Python missing under %ROOT%\python 1>&2
  exit /b 1
)
if not exist "%ROOT%\venv\Lib\site-packages\" (
  echo ERROR: venv site-packages missing under %ROOT%\venv 1>&2
  exit /b 1
)
set "VIRTUAL_ENV=%ROOT%\venv"
set "PYTHONPATH=%ROOT%\venv\Lib\site-packages;%PYTHONPATH%"
set "PATH=%ROOT%\venv\Scripts;%ROOT%\bin;%PATH%"
"%PY%" -m hermes_cli.main %*
exit /b %ERRORLEVEL%
EOF
    cat >"$bundle_root/bin/serve.cmd" <<'EOF'
@echo off
setlocal EnableExtensions
call "%~dp0xhermes.cmd" serve %*
exit /b %ERRORLEVEL%
EOF
  fi
}

build_frozen() {
  local bundle_root="$STAGING/frozen/xhermes-agent-frozen-py${PYTHON}-${PLATFORM}"
  local out="$ROOT/dist/xhermes-agent-frozen-py${PYTHON}-${PLATFORM}.tar.gz"
  offline_populate_frozen_tree "$bundle_root"
  offline_write_frozen_launcher "$bundle_root"

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

Usage (Unix / macOS / Linux):
  tar xzf $(basename "$out")
  cd xhermes-agent-frozen-py${PYTHON}-${PLATFORM}
  ./bin/xhermes --version
  ./bin/serve --host 127.0.0.1 --port 0

Usage (Windows):
  tar xzf $(basename "$out")
  cd xhermes-agent-frozen-py${PYTHON}-${PLATFORM}
  bin\\xhermes.cmd --version
  bin\\serve.cmd --host 127.0.0.1 --port 0

This bundle includes a standalone CPython ${PYTHON} runtime under ./python/
plus a pre-populated venv with xhermes-agent and all core dependencies.
No system Python install is required on the target host.
EOF

  tar -czf "$out" -C "$STAGING/frozen" "xhermes-agent-frozen-py${PYTHON}-${PLATFORM}"
  echo "Frozen bundle: $out ($(du -h "$out" | awk '{print $1}'))"
}

build_aos() {
  local host_os host_arch
  host_os="$(uname -s)"
  host_arch="$(uname -m)"
  if [[ "$host_os" != "Linux" ]] || { [[ "$host_arch" != "aarch64" ]] && [[ "$host_arch" != "arm64" ]]; }; then
    echo "ERROR: aos bundle must be built on linux/aarch64 (use: make dist-offline-aos-linux-aarch64)" >&2
    exit 1
  fi

  # AOS targets are always linux-aarch64 regardless of uname alias (arm64).
  local aos_platform="linux-aarch64"
  local bundle_name="xhermes-agent-aos-py${PYTHON}-${aos_platform}"
  local bundle_root="$STAGING/aos/${bundle_name}"
  local out="$ROOT/dist/${bundle_name}.tar.gz"
  local site

  offline_populate_frozen_tree "$bundle_root"

  site="$(printf '%s\n' "$bundle_root"/venv/lib/python*/site-packages | head -1)"
  echo "→ staging AOS runtime libs into runtime/lib ..."
  offline_stage_aos_runtime_libs "$bundle_root/runtime/lib" "$site"

  install -m 0755 "$ROOT/scripts/offline/aos-xhermes.sh" "$bundle_root/bin/xhermes"
  install -m 0755 "$ROOT/scripts/offline/aos-serve.sh" "$bundle_root/bin/serve"
  # Keep a discoverable alias for operators who type run.sh.
  install -m 0755 "$ROOT/scripts/offline/aos-xhermes.sh" "$bundle_root/bin/run"

  offline_write_manifest \
    "$bundle_root/manifest.json" \
    "aos-frozen" \
    "$VERSION" \
    "$PYTHON" \
    "$aos_platform" \
    "$(basename "$out")" \
    "Huawei AOS offline bundle: embedded CPython ${PYTHON}, runtime/lib (libgcc/libstdc++), ld.so launcher. Run: bash bin/xhermes ..."

  cat >"$bundle_root/README.txt" <<EOF
XHermes AOS offline bundle (embedded Python + runtime libs)
Version: ${VERSION}
Python: ${PYTHON} (embedded under ./python/)
Platform: ${aos_platform}
Kind: aos-frozen

Contents:
  python/       standalone CPython ${PYTHON}
  venv/         xhermes-agent + dependencies
  runtime/lib/  libgcc_s / libstdc++ (and related) for native wheels
  bin/xhermes   AOS launcher (uses system ld.so)
  bin/serve     serve shortcut
  bin/run       alias of bin/xhermes

Deploy on AOS (example):
  tar xzf $(basename "$out") -C /opt/usr
  cd /opt/usr/${bundle_name}
  bash bin/xhermes version
  bash bin/serve --host 127.0.0.1 --port 8642

Why \`bash bin/xhermes\` (not ./bin/xhermes):
  Some AOS mounts block execve() of foreign ELF/scripts under /opt/usr.
  The launcher loads CPython via /lib/ld-linux-aarch64.so.1 and sets
  LD_LIBRARY_PATH to ./runtime/lib so pydantic/cryptography native
  extensions can resolve libgcc_s / libstdc++.

Optional env:
  AOS_LDSO=/lib/ld-linux-aarch64.so.1
  XHERMES_EMBEDDED_PYTHON=${PYTHON}
EOF

  tar -czf "$out" -C "$STAGING/aos" "$bundle_name"
  echo "AOS bundle: $out ($(du -h "$out" | awk '{print $1}'))"
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
  aos) build_aos ;;
  all)
    build_frozen
    build_vendored
    ;;
  *)
    echo "Usage: $0 [frozen|vendored|aos|all]" >&2
    exit 1
    ;;
esac
