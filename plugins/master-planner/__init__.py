"""Bundled discovery shim for the INFA master_planner plugin.

Implementation lives under ``extend.master_planner`` (fork-owned code). This
package exists so PluginManager's bundled ``plugins/`` scan finds the
manifest; ``register`` / ``check_requirements`` are re-exported unchanged.
"""

from __future__ import annotations

from extend.master_planner import check_requirements, register

__all__ = ["check_requirements", "register"]
