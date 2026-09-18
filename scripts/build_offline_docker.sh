#!/usr/bin/env bash
# Build offline bundles inside a Linux container (for hosts that are not Linux/<arch>).
# Usage: build_offline_docker.sh <x86_64|aarch64> [frozen|vendored|all|aos]
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
  frozen|vendored|all|aos) ;;
  *)
    echo "ERROR: mode must be frozen, vendored, all, or aos" >&2
    exit 1
    ;;
esac

IMAGE="${OFFLINE_DOCKER_IMAGE:-ghcr.io/astral-sh/uv:python3.11-bookworm-slim}"

# Pre-download standalone CPython on the host (better network than qemu container)
# so frozen/aos embeds do not depend on in-container GitHub downloads.
STANDALONE_TGZ=""
if [[ "$MODE" == "frozen" || "$MODE" == "all" || "$MODE" == "aos" ]]; then
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

if [[ "$MODE" == "aos" && "$ARCH" != "aarch64" ]]; then
  echo "ERROR: aos mode requires aarch64 (Huawei AOS target)" >&2
  exit 1
fi

# AOS needs libgcc/libstdc++ built against glibc <= 2.34 (AOS ships 2.34).
# Bookworm libs need GLIBC_2.35+, so stage from Rocky Linux 9 (or a pre-baked dir).
AOS_RUNTIME_DIR=""
if [[ "$MODE" == "aos" ]]; then
  AOS_RUNTIME_IMAGE="${OFFLINE_AOS_RUNTIME_IMAGE:-rockylinux:9}"
  AOS_RUNTIME_DIR="${ROOT}/dist/.aos-runtime-libs-${ARCH}"
  mkdir -p "${AOS_RUNTIME_DIR}"
  if [[ -f "${AOS_RUNTIME_DIR}/libgcc_s.so.1" && -e "${AOS_RUNTIME_DIR}/libstdc++.so.6" ]]; then
    echo "→ using cached AOS runtime libs: ${AOS_RUNTIME_DIR}"
  elif [[ -f "${ROOT}/dist/aos-runtime-libs.tgz" ]]; then
    echo "→ extracting ${ROOT}/dist/aos-runtime-libs.tgz into ${AOS_RUNTIME_DIR}"
    rm -rf "${AOS_RUNTIME_DIR}"
    mkdir -p "${AOS_RUNTIME_DIR}"
    tar xzf "${ROOT}/dist/aos-runtime-libs.tgz" -C "${AOS_RUNTIME_DIR}"
  else
    echo "→ staging glibc-2.34-compatible runtime libs from ${AOS_RUNTIME_IMAGE}..."
    rm -rf "${AOS_RUNTIME_DIR}"
    mkdir -p "${AOS_RUNTIME_DIR}"
    if ! docker run --rm --platform "$docker_platform" \
      -v "${AOS_RUNTIME_DIR}:/out" \
      "$AOS_RUNTIME_IMAGE" \
      bash -lc '
        set -euo pipefail
        dnf install -y -q libgcc libstdc++ libgomp libatomic >/dev/null
        cp -a /usr/lib64/libgcc_s.so* /usr/lib64/libstdc++.so* \
              /usr/lib64/libgomp.so* /usr/lib64/libatomic.so* /out/ 2>/dev/null || true
        cp -a /lib64/libgcc_s.so* /lib64/libstdc++.so* \
              /lib64/libgomp.so* /lib64/libatomic.so* /out/ 2>/dev/null || true
        ls -la /out
      '; then
      echo "ERROR: failed to stage AOS runtime libs from ${AOS_RUNTIME_IMAGE}." >&2
      echo "Place glibc-2.34-compatible libs in dist/aos-runtime-libs.tgz or ${AOS_RUNTIME_DIR}" >&2
      exit 1
    fi
  fi
fi

echo "Building offline bundle (${MODE}) for linux-${ARCH} via Docker (${docker_platform})..."
docker_env=(-e XHERMES_HEADLESS_WHEEL_BUILD=1)
if [[ -n "$STANDALONE_TGZ" ]]; then
  docker_env+=(-e "OFFLINE_PYTHON_STANDALONE_TGZ=/work/dist/$(basename "$STANDALONE_TGZ")")
fi
if [[ -n "$AOS_RUNTIME_DIR" ]]; then
  docker_env+=(-e "OFFLINE_AOS_RUNTIME_LIB_DIR=/work/dist/$(basename "$AOS_RUNTIME_DIR")")
fi
# Build image still needs toolchain; runtime libs come from Rocky via env above.
apt_pkgs="build-essential git ca-certificates"
docker run --rm --platform "$docker_platform" \
  -v "${ROOT}:/work" -w /work \
  "${docker_env[@]}" \
  "$IMAGE" \
  bash -lc "
    set -euo pipefail
    apt-get update -qq
    apt-get install -y -qq ${apt_pkgs} >/dev/null
    uv sync --locked --python 3.11 --extra dev
    PYTHON=3.11 scripts/build_offline_bundle.sh ${MODE}
  "

echo "Artifacts in ${ROOT}/dist/"
