"""
setup.py — wheel/sdist build guard.

pip/PyPI distribution is gated: only Nix (uv2nix) or an explicit headless-wheel
release build (``HERMES_HEADLESS_WHEEL_BUILD=1``) may produce artifacts.

Editable installs (``uv sync``, ``pip install -e .``) use ``build_editable`` and
are unaffected.
"""

from __future__ import annotations

import os
import shutil
from pathlib import Path

from setuptools import setup
from setuptools.command.sdist import sdist

_REPO_ROOT = Path(__file__).resolve().parent
_IN_NIX_BUILD = os.environ.get("HERMES_NIX_BUILD") == "1"
_IN_HEADLESS_WHEEL_BUILD = os.environ.get("HERMES_HEADLESS_WHEEL_BUILD") == "1"
_ALLOWED = _IN_NIX_BUILD or _IN_HEADLESS_WHEEL_BUILD

_BLOCK_MESSAGE = (
    "Building wheels or sdists for xhermes-agent is not supported.\n"
    "XHermes is distributed via the shell installer, Docker image, or Nix.\n"
    "See: https://xhermes-agent.nousresearch.com/docs/getting-started/installation\n"
    "\n"
    "If you are developing, use an editable install instead:\n"
    "  uv sync          # or: uv pip install -e .\n"
    "\n"
    "If you are building with Nix (uv2nix), set HERMES_NIX_BUILD=1.\n"
    "For the headless pip wheel release, set HERMES_HEADLESS_WHEEL_BUILD=1\n"
    "and run scripts/build_headless_wheel.sh."
)

_HEADLESS_DATA_DIRS = ("skills", "optional-skills", "locales", "optional-mcps")
_HEADLESS_MARKER = ".headless_wheel_dist"


def _stage_headless_data_assets() -> None:
    """Copy runtime data trees into xhermes_agent_data/ for the pip wheel."""
    data_root = _REPO_ROOT / "xhermes_agent_data"
    data_root.mkdir(exist_ok=True)
    (_REPO_ROOT / "xhermes_agent_data" / "__init__.py").touch(exist_ok=True)
    for name in _HEADLESS_DATA_DIRS:
        src = _REPO_ROOT / name
        dest = data_root / name
        if not src.is_dir():
            raise RuntimeError(f"headless wheel build missing data directory: {src}")
        if dest.exists():
            shutil.rmtree(dest)
        shutil.copytree(src, dest, symlinks=False)
    (data_root / _HEADLESS_MARKER).write_text("1\n", encoding="utf-8")


class _GuardedSdist(sdist):
    def run(self, *args, **kwargs):
        if not _ALLOWED:
            raise RuntimeError(_BLOCK_MESSAGE)
        if _IN_HEADLESS_WHEEL_BUILD:
            _stage_headless_data_assets()
        return super().run(*args, **kwargs)


cmdclass = {"sdist": _GuardedSdist}

try:
    from setuptools.command.bdist_wheel import bdist_wheel

    class _GuardedBdistWheel(bdist_wheel):
        def run(self, *args, **kwargs):
            if not _ALLOWED:
                raise RuntimeError(_BLOCK_MESSAGE)
            if _IN_HEADLESS_WHEEL_BUILD:
                _stage_headless_data_assets()
            return super().run(*args, **kwargs)

    cmdclass["bdist_wheel"] = _GuardedBdistWheel
except ImportError:
    pass

setup(cmdclass=cmdclass)
