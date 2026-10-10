"""Resolve the ``xhermes`` executable for HermesRuntime."""

from __future__ import annotations

import os
import shutil
import sys
from importlib.metadata import distribution, entry_points


def find_xhermes_executable(explicit: str | None = None) -> str | None:
    if explicit:
        path = shutil.which(explicit) or explicit
        return path if os.path.isfile(path) and os.access(path, os.X_OK) else None

    for name in ("xhermes", "xhermes.exe"):
        found = shutil.which(name)
        if found:
            return found

    try:
        dist = distribution("xhermes-agent")
    except Exception:
        dist = None
    if dist is not None:
        for ep in entry_points(group="console_scripts"):
            if ep.name == "xhermes" and ep.dist.name == dist.name:
                script = dist.locate_file(f"../../../bin/xhermes")
                if script.is_file():
                    return str(script)

    # Editable / venv: same interpreter's Scripts/bin directory
    scripts = os.path.join(os.path.dirname(sys.executable), "xhermes")
    if os.path.isfile(scripts) and os.access(scripts, os.X_OK):
        return scripts
    if sys.platform == "win32":
        scripts_exe = scripts + ".exe"
        if os.path.isfile(scripts_exe):
            return scripts_exe
    return None
