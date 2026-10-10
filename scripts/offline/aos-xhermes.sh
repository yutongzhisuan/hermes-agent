#!/usr/bin/env bash
# AOS launcher for the offline aos bundle.
# Huawei AOS may block execve() of foreign ELF under /opt/usr; invoke CPython
# through the system dynamic linker and keep bundled libgcc/libstdc++ on
# LD_LIBRARY_PATH.
#
# Preferred invocation (works even when this file is not executable):
#   bash bin/xhermes ...
#   bash bin/serve ...
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PYTHON_VER="${XHERMES_EMBEDDED_PYTHON:-3.11}"
LDSO="${AOS_LDSO:-/lib/ld-linux-aarch64.so.1}"
if [[ ! -x "$LDSO" ]]; then
  LDSO="/lib64/ld-linux-aarch64.so.1"
fi
if [[ ! -x "$LDSO" ]]; then
  echo "ERROR: dynamic linker not found (tried /lib/ld-linux-aarch64.so.1 and /lib64/...)" >&2
  exit 1
fi

if [[ -f "${ROOT}/python/bin/python${PYTHON_VER}" ]]; then
  PY="${ROOT}/python/bin/python${PYTHON_VER}"
elif [[ -f "${ROOT}/python/bin/python3" ]]; then
  PY="${ROOT}/python/bin/python3"
else
  echo "ERROR: embedded Python missing under ${ROOT}/python/bin" >&2
  exit 1
fi

SITE="$(printf '%s\n' "${ROOT}"/venv/lib/python*/site-packages | head -1)"
if [[ -z "${SITE}" || ! -d "${SITE}" ]]; then
  echo "ERROR: venv site-packages missing under ${ROOT}/venv" >&2
  exit 1
fi

RUNTIME_LIB="${ROOT}/runtime/lib"
export VIRTUAL_ENV="${ROOT}/venv"
export PYTHONPATH="${SITE}${PYTHONPATH:+:${PYTHONPATH}}"
export PATH="${ROOT}/venv/bin:${ROOT}/bin:${PATH}"
if [[ -d "${RUNTIME_LIB}" ]]; then
  export LD_LIBRARY_PATH="${RUNTIME_LIB}${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"
fi

# Load foreign ELF via system ld.so (AOS blocks direct execve of copied binaries).
exec "${LDSO}" "${PY}" "${ROOT}/venv/bin/xhermes" "$@"
