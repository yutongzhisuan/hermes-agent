#!/usr/bin/env bash
# Verify the current host matches an offline bundle platform tag.
# Usage: check_offline_platform.sh <platform-tag>
# Example: check_offline_platform.sh macos-arm64
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/offline_common.sh
source "$ROOT/scripts/offline_common.sh"

expected="${1:?platform tag required (e.g. macos-arm64)}"
actual="$(offline_platform_tag)"

if [[ "$actual" != "$expected" ]]; then
  echo "ERROR: target platform is ${expected}, but this host is ${actual}" >&2
  exit 1
fi
