#!/usr/bin/env bash
# Thin serve wrapper for the AOS offline bundle. Prefer:
#   bash bin/serve ...
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
exec bash "${ROOT}/bin/xhermes" serve "$@"
