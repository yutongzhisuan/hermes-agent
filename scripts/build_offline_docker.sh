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

echo "Building offline bundle (${MODE}) for linux-${ARCH} via Docker (${docker_platform})..."
docker run --rm --platform "$docker_platform" \
  -v "${ROOT}:/work" -w /work \
  -e XHERMES_HEADLESS_WHEEL_BUILD=1 \
  "$IMAGE" \
  bash -lc "
    set -euo pipefail
    apt-get update -qq
    apt-get install -y -qq build-essential git ca-certificates >/dev/null
    uv sync --locked --python 3.11 --extra dev
    PYTHON=3.11 scripts/build_offline_bundle.sh ${MODE}
  "

echo "Artifacts in ${ROOT}/dist/"
