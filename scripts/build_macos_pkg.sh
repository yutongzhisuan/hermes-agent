#!/bin/bash
# ============================================================================
# build_macos_pkg.sh — Build a self-contained macOS installer pkg for the
# xHermes Agent CLI.
#
# Mirrors the verified manual process (see docs/packaging/macos.md):
#   bundled CPython + core deps in site-packages/ + lean source tree, wrapped
#   by a pkg with a postinstall that creates /usr/local/bin/xhermes.
#
# Usage:
#   scripts/build_macos_pkg.sh [--python-version 3.11] [--out-dir ./dist]
#                              [--version X.Y.Z]
#
# Outputs: <out-dir>/xHermes-CLI-<version>-<arch>.pkg  (+ staging dir for debug)
# Requirements: macOS arm64, uv >= 0.11, network (first dep install)
# ============================================================================

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IDENTIFIER="com.xhermes.cli"
PYTHON_VERSION="3.11"
OUT_DIR="$REPO_ROOT/dist"
VERSION=""
ARCH="$(uname -m)"

# ---- arg parsing ----------------------------------------------------------
while [ $# -gt 0 ]; do
    case "$1" in
        --python-version) PYTHON_VERSION="$2"; shift 2 ;;
        --out-dir) OUT_DIR="$2"; shift 2 ;;
        --version) VERSION="$2"; shift 2 ;;
        *) echo "error: unknown argument: $1" >&2; exit 1 ;;
    esac
done

if [ "$(uname -s)" != "Darwin" ]; then
    echo "error: this script builds macOS pkgs only" >&2
    exit 1
fi
if [ "$ARCH" != "arm64" ]; then
    echo "error: only arm64 supported (current: $ARCH)" >&2
    exit 1
fi
if ! command -v uv >/dev/null 2>&1; then
    echo "error: uv not found on PATH" >&2
    exit 1
fi

if [ -z "$VERSION" ]; then
    VERSION="$(cd "$REPO_ROOT" && uv run python -c 'import tomllib; print(tomllib.load(open("pyproject.toml","rb"))["project"]["version"])')"
fi

PKG_NAME="xHermes-CLI-${VERSION}-${ARCH}.pkg"
STAGE_DIR="$OUT_DIR/pkg-stage"
PKG_PATH="$OUT_DIR/$PKG_NAME"

echo "==> xHermes CLI pkg build"
echo "    version: $VERSION   arch: $ARCH   out: $PKG_PATH"

# ---- locate a uv-managed CPython (install if missing) ---------------------
UV_PY_BIN="$("uv python find" "$PYTHON_VERSION" 2>/dev/null || true)"
if [ -z "$UV_PY_BIN" ] || [ ! -f "$UV_PY_BIN" ]; then
    echo "==> uv-managed Python $PYTHON_VERSION not found; installing..."
    uv python install "$PYTHON_VERSION"
    UV_PY_BIN="$(uv python find "$PYTHON_VERSION")"
fi
UV_PY_DIR="$(cd "$(dirname "$UV_PY_BIN")/.." && pwd)"
echo "    bundled python: $UV_PY_DIR"

# ---- fresh staging ---------------------------------------------------------
rm -rf "$STAGE_DIR"
mkdir -p "$STAGE_DIR/usr/local/lib/xhermes-agent"
STAGE_ROOT="$STAGE_DIR/usr/local/lib/xhermes-agent"

# 1. self-contained CPython (relocatable, no symlinks to uv cache)
echo "==> copying CPython $PYTHON_VERSION"
cp -R "$UV_PY_DIR" "$STAGE_ROOT/python"

# 2. extract core deps from pyproject.toml (skip platform markers)
echo "==> extracting core dependencies"
REQ_FILE="$STAGE_DIR/req.txt"
python3 - "$REQ_FILE" <<'PYEOF'
import re, sys
s = open("pyproject.toml").read()
m = re.search(r'^dependencies = \[(.*?)^\]', s, re.S | re.M)
deps = re.findall(r'^\s*"([^"]+)"\s*,?\s*(?:#.*)?$', m.group(1), re.M)
keep = [d for d in deps if "sys_platform" not in d and "platform_machine" not in d]
open(sys.argv[1], "w").write("\n".join(keep) + "\n")
print(f"    {len(keep)} core deps")
PYEOF

# 3. install deps into standalone site-packages (managed-python is read-only)
echo "==> installing core dependencies"
mkdir -p "$STAGE_ROOT/site-packages"
uv pip install --target "$STAGE_ROOT/site-packages" -r "$REQ_FILE"

# 4. lean source tree
echo "==> copying source tree (lean)"
rsync -a \
    --exclude='.git' --exclude='.venv' --exclude='node_modules' \
    --exclude='tests' --exclude='web/' --exclude='apps' \
    --exclude='ui-tui/node_modules' --exclude='ui-tui/dist' \
    --exclude='*.pyc' --exclude='.pytest_cache' --exclude='.mypy_cache' \
    --exclude='__pycache__' --exclude='xhermes_agent.egg-info' \
    --exclude='.codegraph' --exclude='.cortexkit' \
    --exclude='website' --exclude='docs' --exclude='scripts/ci' \
    --exclude='dist' \
    "$REPO_ROOT"/ "$STAGE_ROOT/xhermes-agent/"

# 5. drop runtime-unneeded build artifacts inside the source copy
rm -rf "$STAGE_ROOT/xhermes-agent/extend/task_relay/hub/go"
rm -rf "$STAGE_ROOT/xhermes-agent/extend/task_relay/master" \
       "$STAGE_ROOT/xhermes-agent/extend/task_relay/worker" \
       "$STAGE_ROOT/xhermes-agent/extend/task_relay/tests" \
       "$STAGE_ROOT/xhermes-agent/extend/task_relay/scripts"
rm -rf "$STAGE_ROOT/xhermes-agent/contributors"

# 6. strip xattrs + block AppleDouble so pkgbuild emits no ._* BOM entries
echo "==> cleaning xattrs"
xattr -cr "$STAGE_DIR" 2>/dev/null || true

# 7. postinstall: launcher at /usr/local/bin/xhermes
echo "==> writing postinstall"
mkdir -p "$STAGE_DIR/scripts"
cat > "$STAGE_DIR/scripts/postinstall" <<'POSTINSTALL'
#!/bin/sh
BASE=/usr/local/lib/xhermes-agent
LAUNCHER="$BASE/bin/xhermes"
mkdir -p "$BASE/bin" /usr/local/bin
cat > "$LAUNCHER" <<'LAUNCHER_EOF'
#!/bin/sh
BASE=/usr/local/lib/xhermes-agent
export PYTHONPATH="$BASE/site-packages:$BASE/xhermes-agent"
exec "$BASE/python/bin/python3.11" -m hermes_cli.main "$@"
LAUNCHER_EOF
chmod 755 "$LAUNCHER"
ln -sf "$LAUNCHER" /usr/local/bin/xhermes
exit 0
POSTINSTALL
chmod 755 "$STAGE_DIR/scripts/postinstall"

# 8. pkgbuild (COPYFILE_DISABLE blocks AppleDouble metadata in the BOM)
echo "==> building pkg"
mkdir -p "$OUT_DIR"
COPYFILE_DISABLE=1 pkgbuild \
    --root "$STAGE_DIR" \
    --identifier "$IDENTIFIER" \
    --version "$VERSION" \
    --scripts "$STAGE_DIR/scripts" \
    --install-location / \
    "$PKG_PATH"

echo ""
echo "==> done: $PKG_PATH"
echo "    staging (debug): $STAGE_DIR"
echo "    install with: sudo installer -pkg $PKG_PATH -target /"
