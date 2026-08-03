"""Invariant: every workspace link recorded in package-lock.json must resolve.

When a workspace directory (or its ``package.json`` name) is renamed, the
committed ``package-lock.json`` must be regenerated — its ``node_modules/<name>``
link entries point at the workspace via a relative ``resolved`` path and carry
the package's declared ``name``. A stale link (old path, old name) makes
``npm install`` fail: npm 10 crashes opaquely in arborist ("Cannot read
properties of undefined (reading 'extraneous')") and npm 11 aborts with
EMISSINGTARGET ("Missing target in lock file"). This bit the xhermes rename
(``xhermes-ink`` -> ``xhermes-ink``) across five workspaces, not just one.

These tests pin the relationship, not any specific package names — they are
contracts between the lockfile and the working tree, so they keep guarding
after future renames or dependency moves.
"""

from __future__ import annotations

import json
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
LOCKFILES = [REPO_ROOT / "package-lock.json"]
WORKSPACE_GLOBS = ["apps/*", "ui-tui", "ui-tui/packages/*", "web", "tests-js"]


def _load_lockfile(path: Path) -> dict:
    with path.open() as fh:
        return json.load(fh)


def _workspace_dirs() -> set[Path]:
    """Resolve the root package.json ``workspaces`` globs against the tree."""
    root_pkg = json.loads((REPO_ROOT / "package.json").read_text())
    globs = root_pkg.get("workspaces", [])
    assert globs, "package.json must declare workspaces"
    dirs: set[Path] = set()
    for glob in globs:
        dirs.update(REPO_ROOT.glob(glob))
    return {d for d in dirs if d.is_dir()}


def _link_nodes(lockfile: dict) -> dict[str, str]:
    """Map ``node_modules/<name>`` keys -> resolved workspace path (link entries)."""
    links: dict[str, str] = {}
    for key, meta in lockfile.get("packages", {}).items():
        if not key.startswith("node_modules/"):
            continue
        if meta.get("link"):
            links[key.removeprefix("node_modules/")] = meta.get("resolved", "")
    return links


def _declared_workspace_names() -> dict[str, Path]:
    """Map each workspace package.json ``name`` -> its directory."""
    names: dict[str, Path] = {}
    for d in _workspace_dirs():
        pkg_file = d / "package.json"
        if not pkg_file.is_file():
            continue
        name = json.loads(pkg_file.read_text()).get("name")
        if name:
            names[name] = d
    return names


def test_every_workspace_link_target_exists_and_matches_declared_name() -> None:
    """A link's resolved path must exist and its package name must match."""
    declared = _declared_workspace_names()
    for lockfile_path in LOCKFILES:
        lock = _load_lockfile(lockfile_path)
        links = _link_nodes(lock)
        assert links, f"no workspace links found in {lockfile_path.name}"
        for pkg_name, resolved in links.items():
            target = REPO_ROOT / resolved
            assert (target / "package.json").is_file(), (
                f"{lockfile_path.name}: link node_modules/{pkg_name} resolves to "
                f"{resolved!r} which does not exist (stale lockfile after a "
                f"workspace move/rename — regenerate with `npm install --package-lock-only`)"
            )
            declared_name = json.loads((target / "package.json").read_text())["name"]
            assert declared_name == pkg_name, (
                f"{lockfile_path.name}: node_modules/{pkg_name} links to {resolved!r} "
                f"whose package name is {declared_name!r} (renamed workspace — "
                f"regenerate the lockfile)"
            )
            assert declared_name in declared, (
                f"{lockfile_path.name}: workspace {declared_name!r} at {resolved!r} is "
                f"not covered by package.json workspaces {WORKSPACE_GLOBS}"
            )


def test_every_workspace_package_is_linked_in_the_lockfile() -> None:
    """Each named workspace package must appear as a lockfile link entry."""
    declared = _declared_workspace_names()
    for lockfile_path in LOCKFILES:
        lock = _load_lockfile(lockfile_path)
        links = _link_nodes(lock)
        missing = [name for name in declared if name not in links]
        assert not missing, (
            f"{lockfile_path.name}: workspaces missing a lockfile link: {missing} "
            f"(add/keep them in `packages`, e.g. \"node_modules/{missing[0]}\" -> "
            f"{{'resolved': '<path>', 'link': true}})"
        )
