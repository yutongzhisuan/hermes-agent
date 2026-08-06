"""Contract tests for headless wheel bundled data assets."""

from __future__ import annotations

import os
import subprocess
import sys
import zipfile
from pathlib import Path

import pytest

PROJECT_ROOT = Path(__file__).resolve().parents[1]


def _build_headless_wheel(tmp_path: Path) -> Path:
    env = os.environ.copy()
    env["XHERMES_HEADLESS_WHEEL_BUILD"] = "1"
    env["NIX_BUILD_TOP"] = "/build/devshell"
    scratch = tmp_path / "scratch"
    scratch.mkdir()
    extra_cfg = tmp_path / "dist-extra.cfg"
    extra_cfg.write_text(
        f"[build]\nbuild_base = {scratch / 'build'}\n\n[egg_info]\negg_base = {scratch}\n",
        encoding="utf-8",
    )
    env["DIST_EXTRA_CONFIG"] = str(extra_cfg)
    out = tmp_path / "wheel-out"
    out.mkdir()
    result = subprocess.run(
        [
            sys.executable,
            "-c",
            f"from setuptools.build_meta import build_wheel; build_wheel(r'{out}')",
        ],
        cwd=PROJECT_ROOT,
        env=env,
        text=True,
        capture_output=True,
        check=False,
    )
    assert result.returncode == 0, result.stderr
    wheels = list(out.glob("xhermes_agent-*.whl"))
    assert len(wheels) == 1
    return wheels[0]


@pytest.mark.integration
def test_headless_wheel_includes_bundled_data(tmp_path):
    wheel = _build_headless_wheel(tmp_path)
    with zipfile.ZipFile(wheel) as zf:
        names = zf.namelist()
        assert any("xhermes_agent_data/skills/" in n and n.endswith("SKILL.md") for n in names)
        assert any(n.endswith("xhermes_agent_data/locales/en.yaml") for n in names)
        assert any(".headless_wheel_dist" in n for n in names)
        forbidden = ("apps/desktop", "ui-tui/", "website/", "web_dist/")
        for bad in forbidden:
            assert not any(bad in n for n in names), bad


@pytest.mark.integration
def test_headless_wheel_install_resolves_bundled_skills(tmp_path):
    wheel = _build_headless_wheel(tmp_path)
    venv = tmp_path / "venv"
    subprocess.run([sys.executable, "-m", "venv", str(venv)], check=True)
    pip = venv / "bin" / "pip"
    py = venv / "bin" / "python"
    subprocess.run([str(pip), "install", str(wheel)], check=True, capture_output=True)
    env = os.environ.copy()
    env.pop("PYTHONPATH", None)
    out = subprocess.check_output(
        [
            str(py),
            "-I",
            "-c",
            "from hermes_constants import get_bundled_skills_dir; "
            "from xhermes_agent_data import is_headless_wheel_install, DATA_ROOT; "
            "p=get_bundled_skills_dir(); "
            "print(is_headless_wheel_install(), DATA_ROOT, p, any(p.glob('**/SKILL.md')))",
        ],
        cwd=tmp_path,
        env=env,
        text=True,
    ).strip()
    parts = out.split()
    assert parts[0] == "True"
    assert "site-packages" in parts[1]
    assert parts[-1] == "True"
