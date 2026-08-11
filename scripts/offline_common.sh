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

# Populate dest/ with a standalone CPython tree (bin/lib/...).
# Prefer OFFLINE_PYTHON_STANDALONE_TGZ (pre-downloaded install_only tarball)
# so Docker/cross builds are not blocked by in-container GitHub downloads.
# Otherwise install via `uv python install --install-dir` (never uses project .venv).
offline_stage_embedded_python() {
  local dest="$1"
  local ver="$2"
  local staging prefix tgz="${OFFLINE_PYTHON_STANDALONE_TGZ:-}"

  rm -rf "$dest"
  mkdir -p "$dest"

  if [[ -n "$tgz" ]]; then
    if [[ ! -f "$tgz" ]]; then
      echo "ERROR: OFFLINE_PYTHON_STANDALONE_TGZ not found: ${tgz}" >&2
      exit 1
    fi
    staging="$(mktemp -d "${TMPDIR:-/tmp}/xhermes-embed-py.XXXXXX")"
    tar xzf "$tgz" -C "$staging"
    # python-build-standalone install_only layout: python/bin/python3.x
    if [[ -x "$staging/python/bin/python${ver}" || -x "$staging/python/bin/python3" ]]; then
      cp -a "$staging/python"/. "$dest"/
    else
      prefix="$(find "$staging" -mindepth 1 -maxdepth 1 -type d | head -1)"
      if [[ -z "$prefix" || ! -d "$prefix/bin" ]]; then
        echo "ERROR: unrecognized standalone Python layout in ${tgz}" >&2
        find "$staging" | head -40 >&2 || true
        rm -rf "$staging"
        exit 1
      fi
      cp -a "$prefix"/. "$dest"/
    fi
    rm -rf "$staging"
  else
    if ! command -v uv >/dev/null 2>&1; then
      echo "ERROR: uv is required to embed a standalone Python ${ver} runtime" >&2
      exit 1
    fi
    staging="$(mktemp -d "${TMPDIR:-/tmp}/xhermes-embed-py.XXXXXX")"
    if ! uv python install "$ver" --install-dir "$staging" --no-bin >/dev/null; then
      rm -rf "$staging"
      echo "ERROR: uv python install ${ver} failed (set OFFLINE_PYTHON_STANDALONE_TGZ to use a pre-downloaded tarball)" >&2
      exit 1
    fi
    # Prefer the concrete versioned directory (e.g. cpython-3.11.15-...), not the
    # short cpython-3.11-... symlink which may point outside the staging tree.
    prefix="$(find "$staging" -mindepth 1 -maxdepth 1 -type d -name "cpython-${ver}.*" | sort | tail -1)"
    if [[ -z "$prefix" || ! -d "$prefix/bin" ]]; then
      echo "ERROR: uv python install ${ver} did not produce a usable prefix under ${staging}" >&2
      ls -la "$staging" >&2 || true
      rm -rf "$staging"
      exit 1
    fi
    cp -a "$prefix"/. "$dest"/
    rm -rf "$staging"
  fi

  if [[ -x "$dest/bin/python${ver}" ]]; then
    echo "$dest/bin/python${ver}"
  elif [[ -x "$dest/bin/python3" ]]; then
    echo "$dest/bin/python3"
  else
    echo "ERROR: embedded Python binary missing under ${dest}/bin" >&2
    ls -la "$dest/bin" >&2 || true
    exit 1
  fi
}

# Point venv/bin/python* at the embedded runtime with relative symlinks and
# rewrite pyvenv.cfg home so the tree stays relocatable after extract.
offline_rewire_venv_to_embedded_python() {
  local bundle_root="$1"
  local ver="$2"
  local venv_bin="$bundle_root/venv/bin"
  local embedded_py="python${ver}"
  local cfg="$bundle_root/venv/pyvenv.cfg"

  if [[ ! -x "$bundle_root/python/bin/${embedded_py}" ]]; then
    if [[ -x "$bundle_root/python/bin/python3" ]]; then
      embedded_py="python3"
    else
      echo "ERROR: embedded python not found for venv rewire" >&2
      exit 1
    fi
  fi

  mkdir -p "$venv_bin"
  ln -sfn "../../python/bin/${embedded_py}" "$venv_bin/python"
  ln -sfn "python" "$venv_bin/python3"
  ln -sfn "python" "$venv_bin/python${ver}"

  if [[ -f "$cfg" ]]; then
    if grep -q '^home = ' "$cfg"; then
      # Relative to venv/; resolved by the launcher against absolute ROOT.
      sed -i.bak 's|^home = .*|home = ../python/bin|' "$cfg"
      rm -f "${cfg}.bak"
    else
      echo 'home = ../python/bin' >>"$cfg"
    fi
    if ! grep -q '^relocatable' "$cfg"; then
      echo 'relocatable = true' >>"$cfg"
    fi
  fi
}

# Copy host shared libraries needed by native wheels on minimal AOS images
# (libgcc / libstdc++ are often absent from /lib64). Only whitelist safe libs —
# never ship glibc or the dynamic linker (use the target's).
#
# Prefer OFFLINE_AOS_RUNTIME_LIB_DIR when set (pre-staged libs from a
# glibc-2.34-compatible image such as rockylinux:9). Bookworm libs require
# GLIBC_2.35+ and will fail on AOS 2.34.
offline_stage_aos_runtime_libs() {
  local dest="$1"
  local site="${2:-}"
  local name src real
  local -a wanted=(libgcc_s.so.1 libstdc++.so.6 libgomp.so.1 libatomic.so.1)
  local preset="${OFFLINE_AOS_RUNTIME_LIB_DIR:-}"

  mkdir -p "$dest"

  if [[ -n "$preset" ]]; then
    if [[ ! -d "$preset" ]]; then
      echo "ERROR: OFFLINE_AOS_RUNTIME_LIB_DIR not a directory: ${preset}" >&2
      exit 1
    fi
    echo "→ copying AOS runtime libs from ${preset}"
    # Copy SONAME links and real objects; dereference nothing yet.
    find "$preset" -maxdepth 1 \( -type f -o -type l \) \( \
      -name 'libgcc_s.so*' -o -name 'libstdc++.so*' -o \
      -name 'libgomp.so*' -o -name 'libatomic.so*' \
    \) -exec cp -a {} "$dest/" \;
    # Ensure canonical SONAME names exist even if only versioned files were present.
    for name in "${wanted[@]}"; do
      if [[ -e "$dest/$name" ]]; then
        continue
      fi
      src="$(find "$dest" -maxdepth 1 -type f -name "${name%.*}.*" 2>/dev/null | sort | tail -1 || true)"
      if [[ -n "${src:-}" ]]; then
        ln -sfn "$(basename "$src")" "$dest/$name"
      fi
    done
  else
    _aos_resolve_lib() {
      local n="$1" p
      p="$(ldconfig -p 2>/dev/null | awk -v n="$n" '$1 == n { print $NF; exit }')"
      if [[ -n "$p" && -e "$p" ]]; then
        echo "$p"
        return 0
      fi
      find /lib /lib64 /usr/lib /usr/lib64 -name "$n" 2>/dev/null | head -1
    }

    for name in "${wanted[@]}"; do
      src="$(_aos_resolve_lib "$name" || true)"
      if [[ -z "${src:-}" || ! -e "$src" ]]; then
        echo "  skip missing runtime lib: ${name}" >&2
        continue
      fi
      real="$(readlink -f "$src" 2>/dev/null || echo "$src")"
      cp -aL "$real" "$dest/$(basename "$real")"
      if [[ "$(basename "$real")" != "$name" ]]; then
        ln -sfn "$(basename "$real")" "$dest/$name"
      fi
      echo "  staged ${name} <- ${real}"
    done

    if [[ -n "$site" && -d "$site" ]]; then
      while IFS= read -r so; do
        [[ -f "$so" ]] || continue
        while IFS= read -r dep; do
          case "$dep" in
            libgcc_s.so*|libstdc++.so*|libgomp.so*|libatomic.so*) ;;
            *) continue ;;
          esac
          if [[ -e "$dest/$dep" ]]; then
            continue
          fi
          src="$(_aos_resolve_lib "$dep" || true)"
          if [[ -n "${src:-}" && -e "$src" ]]; then
            real="$(readlink -f "$src" 2>/dev/null || echo "$src")"
            cp -aL "$real" "$dest/$(basename "$real")"
            ln -sfn "$(basename "$real")" "$dest/$dep"
            echo "  staged ${dep} <- ${real} (from $(basename "$so"))"
          fi
        done < <(ldd "$so" 2>/dev/null | awk '/=>/ { print $1 }' || true)
      done < <(find "$site" -type f \( -name '*.so' -o -name '*.so.*' \) 2>/dev/null | head -80)
    fi
  fi

  if [[ ! -e "$dest/libgcc_s.so.1" || ! -e "$dest/libstdc++.so.6" ]]; then
    echo "ERROR: AOS runtime requires libgcc_s.so.1 and libstdc++.so.6 in ${dest}" >&2
    ls -la "$dest" >&2 || true
    exit 1
  fi
  echo "→ AOS runtime lib inventory:"
  ls -la "$dest"
}
