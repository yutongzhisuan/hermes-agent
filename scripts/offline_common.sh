#!/usr/bin/env bash
# Shared helpers for offline headless wheel bundles.
set -euo pipefail

offline_root() {
  cd "$(dirname "${BASH_SOURCE[1]}")/.." && pwd
}

offline_python_cmd() {
  if command -v uv >/dev/null 2>&1; then
    echo "uv run python"
  else
    echo "python3"
  fi
}

offline_platform_tag() {
  local os arch
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$os" in
    darwin) os="macos" ;;
    linux) os="linux" ;;
    mingw*|msys*|cygwin*) os="windows" ;;
  esac
  echo "${os}-${arch}"
}

offline_build_headless_wheel() {
  local root="$1"
  local out_dir="$2"
  mkdir -p "$out_dir"
  (
    cd "$root"
    export XHERMES_HEADLESS_WHEEL_BUILD=1
    if command -v uv >/dev/null 2>&1; then
      uv run python -c "from setuptools.build_meta import build_wheel; build_wheel('${out_dir}')" >&2
    else
      python3 -c "from setuptools.build_meta import build_wheel; build_wheel('${out_dir}')" >&2
    fi
  )
  ls -1 "$out_dir"/xhermes_agent-*.whl | head -1
}

offline_write_manifest() {
  local path="$1"
  local kind="$2"
  local version="$3"
  local python="$4"
  local platform="$5"
  local artifact="$6"
  local note="$7"
  cat >"$path" <<EOF
{
  "kind": "${kind}",
  "package": "xhermes-agent",
  "version": "${version}",
  "python": "${python}",
  "platform": "${platform}",
  "artifact": "${artifact}",
  "note": "${note}"
}
EOF
}
