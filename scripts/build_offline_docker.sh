#!/usr/bin/env bash
# Build offline bundles inside a Linux container (for hosts that are not Linux/<arch>).
# Usage: build_offline_docker.sh <x86_64|aarch64> [frozen|vendored|all]
set -euo pipefail

ARCH="${1:?arch required: x86_64 or aarch64}"
MODE="${2:-all}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

case "$ARCH" in
  x86_64)
    docker_platform="linux/amd64"
    ;;
  aarch64|arm64)
    docker_platform="linux/arm64"
    ARCH="aarch64"
    ;;
  *)
    echo "ERROR: unsupported arch ${ARCH} (use x86_64 or aarch64)" >&2
    exit 1
    ;;
esac

case "$MODE" in
  frozen|vendored|all) ;;
  *)
    echo "ERROR: mode must be frozen, vendored, or all" >&2
    exit 1
    ;;
esac

IMAGE="${OFFLINE_DOCKER_IMAGE:-ghcr.io/astral-sh/uv:python3.11-bookworm-slim}"

# Pre-download standalone CPython on the host (better network than qemu container)
# so frozen embeds do not depend on in-container GitHub downloads.
STANDALONE_TGZ=""
if [[ "$MODE" == "frozen" || "$MODE" == "all" ]]; then
  STANDALONE_TAG="${OFFLINE_PYTHON_STANDALONE_TAG:-20260610}"
  STANDALONE_PY="${OFFLINE_PYTHON_STANDALONE_VERSION:-3.11.15}"
  case "$ARCH" in
    aarch64) STANDALONE_TRIPLE="aarch64-unknown-linux-gnu" ;;
    x86_64) STANDALONE_TRIPLE="x86_64-unknown-linux-gnu" ;;
  esac
  STANDALONE_TGZ="${ROOT}/dist/cpython-${STANDALONE_PY}-${STANDALONE_TRIPLE}-install_only_stripped.tar.gz"
  mkdir -p "${ROOT}/dist"
  if [[ ! -f "$STANDALONE_TGZ" ]]; then
    url="https://github.com/astral-sh/python-build-standalone/releases/download/${STANDALONE_TAG}/cpython-${STANDALONE_PY}%2B${STANDALONE_TAG}-${STANDALONE_TRIPLE}-install_only_stripped.tar.gz"
    echo "→ downloading standalone Python ${STANDALONE_PY} (${STANDALONE_TRIPLE})..."
    curl -fL --retry 3 -o "$STANDALONE_TGZ" "$url"
  else
    echo "→ using cached standalone Python: ${STANDALONE_TGZ}"
  fi
fi

echo "Building offline bundle (${MODE}) for linux-${ARCH} via Docker (${docker_platform})..."
docker_env=(-e XHERMES_HEADLESS_WHEEL_BUILD=1)
if [[ -n "$STANDALONE_TGZ" ]]; then
  docker_env+=(-e "OFFLINE_PYTHON_STANDALONE_TGZ=/work/dist/$(basename "$STANDALONE_TGZ")")
fi
docker run --rm --platform "$docker_platform" \
  -v "${ROOT}:/work" -w /work \
  "${docker_env[@]}" \
  "$IMAGE" \
  bash -lc "
    set -euo pipefail
    apt-get update -qq
    apt-get install -y -qq build-essential git ca-certificates >/dev/null
    uv sync --locked --python 3.11 --extra dev
    PYTHON=3.11 scripts/build_offline_bundle.sh ${MODE}
  "

echo "Artifacts in ${ROOT}/dist/"
