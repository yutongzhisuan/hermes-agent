"""Local task ledger (sqlite) — the ONLY reliable state-recovery path.

Spec §4.3: hermes compaction replaces old tool results with placeholders and
there is no state-pin mechanism, so after compaction the ledger is the sole
record of which tasks were dispatched and which are still open. Truth lives
in the server and in this ledger — never in the LLM context.

Stored per task: ``(run_id, task_id, batch_id, goal, status,
cursor_event_id, gateway_instance_id, submitted_at, updated_at)``.

``cursor_event_id`` is segmented by ``gateway_instance_id`` (spec §4.3 #3):
event ids are only monotonic within one gateway instance, so after an M2
failover to a different CN instance the old cursor is meaningless — always
resume with the cursor recorded for the instance you are watching.

Follows the ``~/.xhermes`` state convention (``XHERMES_HOME`` override).
All writes are serialized through a lock — spec §12.4 #21 leaves the
parallel-safety of plugin tools unverified, so ledger access must be
thread-safe regardless.
"""

from __future__ import annotations

import os
import sqlite3
import threading
import time
from typing import Any, Optional

ENV_DB_PATH = "INFA_MASTER_PLANNER_DB"

_SCHEMA = """
CREATE TABLE IF NOT EXISTS tasks (
    task_id              TEXT PRIMARY KEY,
    run_id               TEXT NOT NULL,
    batch_id             TEXT NOT NULL DEFAULT '',
    goal                 TEXT NOT NULL DEFAULT '',
    status               TEXT NOT NULL DEFAULT 'submitted',
    cursor_event_id      TEXT NOT NULL DEFAULT '',
    gateway_instance_id  TEXT NOT NULL DEFAULT '',
    submitted_at         REAL NOT NULL,
    updated_at           REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_run_id ON tasks(run_id);
CREATE INDEX IF NOT EXISTS idx_tasks_batch_id ON tasks(batch_id);

CREATE TABLE IF NOT EXISTS run_seq (
    run_id  TEXT PRIMARY KEY,
    high    INTEGER NOT NULL
);
"""

TERMINAL_STATUSES = frozenset({"completed", "failed", "cancelled", "lost"})


def default_db_path() -> str:
    override = os.getenv(ENV_DB_PATH, "").strip()
    if override:
        return override
    home = os.getenv("XHERMES_HOME", "").strip() or os.path.expanduser("~/.xhermes")
    return os.path.join(home, "master_planner.db")


class Ledger:
    """Thread-safe sqlite task ledger."""

    def __init__(self, db_path: Optional[str] = None):
        self.db_path = db_path or default_db_path()
        parent = os.path.dirname(os.path.abspath(self.db_path))
        os.makedirs(parent, exist_ok=True)
        self._lock = threading.Lock()
        self._conn = sqlite3.connect(self.db_path, check_same_thread=False)
        self._conn.row_factory = sqlite3.Row
        with self._lock:
            self._conn.executescript(_SCHEMA)
            self._conn.commit()

    # ------------------------------------------------------------------
    # Writes
    # ------------------------------------------------------------------

    def record(
        self,
        *,
        run_id: str,
        task_id: str,
        goal: str,
        batch_id: str = "",
        status: str = "submitted",
        gateway_instance_id: str = "",
        seq: int = 0,
    ) -> None:
        """Insert (or idempotently replace) a task row at dispatch time.

        ``seq`` is the sequence issued for this task_id; it raises the run's
        high-water mark so the id is never issued again, even if task rows are
        later pruned. A reissued task_id would be dropped by the node-side
        dedup table, whose retention outlives the platform's task state.
        """
        now = time.time()
        with self._lock:
            self._conn.execute(
                """
                INSERT INTO tasks (task_id, run_id, batch_id, goal, status,
                                   gateway_instance_id, submitted_at, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(task_id) DO UPDATE SET
                    status = excluded.status,
                    gateway_instance_id = excluded.gateway_instance_id,
                    updated_at = excluded.updated_at
                """,
                (
                    task_id,
                    run_id,
                    batch_id,
                    goal,
                    status,
                    gateway_instance_id,
                    now,
                    now,
                ),
            )
            if seq > 0:
                self._conn.execute(
                    "INSERT INTO run_seq (run_id, high) VALUES (?, ?)"
                    " ON CONFLICT(run_id) DO UPDATE SET"
                    " high = MAX(high, excluded.high)",
                    (run_id, seq),
                )
            self._conn.commit()

    def update_status(self, task_id: str, status: str) -> None:
        with self._lock:
            self._conn.execute(
                "UPDATE tasks SET status = ?, updated_at = ? WHERE task_id = ?",
                (status, time.time(), task_id),
            )
            self._conn.commit()

    def update_cursor(
        self, task_id: str, cursor_event_id: str, gateway_instance_id: str = ""
    ) -> None:
        """Persist the watch resume cursor, segmented by gateway instance."""
        with self._lock:
            if gateway_instance_id:
                self._conn.execute(
                    "UPDATE tasks SET cursor_event_id = ?, gateway_instance_id = ?,"
                    " updated_at = ? WHERE task_id = ?",
                    (cursor_event_id, gateway_instance_id, time.time(), task_id),
                )
            else:
                self._conn.execute(
                    "UPDATE tasks SET cursor_event_id = ?, updated_at = ? WHERE task_id = ?",
                    (cursor_event_id, time.time(), task_id),
                )
            self._conn.commit()

    def next_seq(self, run_id: str) -> int:
        """Next per-run sequence (task_id idempotency key).

        Reads the high-water mark of recorded tasks, falling back to the row
        count for ledgers written before ``run_seq`` existed. Deliberately not
        an allocating counter: a dispatch that fails before ``record`` must
        retry under the same task_id so the platform can answer it as a
        duplicate instead of running the goal twice.
        """
        with self._lock:
            row = self._conn.execute(
                "SELECT COALESCE("
                " (SELECT high FROM run_seq WHERE run_id = ?),"
                " (SELECT COUNT(*) FROM tasks WHERE run_id = ?)"
                ") AS n",
                (run_id, run_id),
            ).fetchone()
            return int(row["n"]) + 1

    # ------------------------------------------------------------------
    # Reads
    # ------------------------------------------------------------------

    def get(self, task_id: str) -> Optional[dict[str, Any]]:
        with self._lock:
            row = self._conn.execute(
                "SELECT * FROM tasks WHERE task_id = ?", (task_id,)
            ).fetchone()
        return dict(row) if row else None

    def open_tasks(self, run_id: str = "") -> list[dict[str, Any]]:
        """Non-terminal tasks — the recovery inventory after restart/compaction."""
        placeholders = ",".join("?" for _ in TERMINAL_STATUSES)
        sql = f"SELECT * FROM tasks WHERE status NOT IN ({placeholders})"
        params: list[Any] = sorted(TERMINAL_STATUSES)
        if run_id:
            sql += " AND run_id = ?"
            params.append(run_id)
        sql += " ORDER BY submitted_at"
        with self._lock:
            rows = self._conn.execute(sql, params).fetchall()
        return [dict(r) for r in rows]

    def tasks_in_batch(self, batch_id: str) -> list[dict[str, Any]]:
        with self._lock:
            rows = self._conn.execute(
                "SELECT * FROM tasks WHERE batch_id = ? ORDER BY submitted_at",
                (batch_id,),
            ).fetchall()
        return [dict(r) for r in rows]

    def close(self) -> None:
        with self._lock:
            self._conn.close()
