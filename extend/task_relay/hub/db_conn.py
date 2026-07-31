"""Database connection adapters shared by SQLite and Postgres backends."""

from __future__ import annotations

import re
from typing import Any, Mapping, Sequence

import aiosqlite

_NAMED_PARAM = re.compile(r":([a-zA-Z_][a-zA-Z0-9_]*)")
_QMARK = re.compile(r"\?")


class DbCursor:
    """Minimal cursor wrapper compatible with existing Hub call sites."""

    def __init__(
        self,
        rows: Sequence[Any] | None = None,
        *,
        lastrowid: int | None = None,
        _sqlite_cursor: aiosqlite.Cursor | None = None,
    ):
        self._rows = list(rows or [])
        self._index = 0
        self._lastrowid = lastrowid
        self._sqlite_cursor = _sqlite_cursor

    @property
    def lastrowid(self) -> int | None:
        if self._sqlite_cursor is not None:
            return self._sqlite_cursor.lastrowid
        return self._lastrowid

    async def fetchone(self) -> Any | None:
        if self._sqlite_cursor is not None:
            return await self._sqlite_cursor.fetchone()
        if self._index >= len(self._rows):
            return None
        row = self._rows[self._index]
        self._index += 1
        return row

    async def fetchall(self) -> list[Any]:
        if self._sqlite_cursor is not None:
            return await self._sqlite_cursor.fetchall()
        remaining = self._rows[self._index :]
        self._index = len(self._rows)
        return remaining


def _translate_qmark(sql: str, params: Sequence[Any]) -> tuple[str, list[Any]]:
    out: list[str] = []
    param_index = 0
    pos = 0
    for match in _QMARK.finditer(sql):
        out.append(sql[pos : match.start()])
        param_index += 1
        out.append(f"${param_index}")
        pos = match.end()
    out.append(sql[pos:])
    return "".join(out), list(params)


def _translate_named(sql: str, params: Mapping[str, Any]) -> tuple[str, list[Any]]:
    ordered: list[Any] = []
    index_by_name: dict[str, int] = {}

    def repl(match: re.Match[str]) -> str:
        name = match.group(1)
        if name not in params:
            raise KeyError(f"missing SQL parameter: {name}")
        if name not in index_by_name:
            index_by_name[name] = len(ordered) + 1
            ordered.append(params[name])
        return f"${index_by_name[name]}"

    pg_sql = _NAMED_PARAM.sub(repl, sql)
    return pg_sql, ordered


class SqliteDbConn:
    """Wraps an aiosqlite connection behind the shared execute API."""

    def __init__(self, conn: aiosqlite.Connection):
        self._conn = conn
        self.dialect = "sqlite"

    async def execute(
        self, sql: str, params: Sequence[Any] | Mapping[str, Any] | None = None
    ) -> DbCursor:
        cursor = await self._conn.execute(sql, params or ())
        return DbCursor(_sqlite_cursor=cursor)

    async def execute_fetchall(
        self, sql: str, params: Sequence[Any] | Mapping[str, Any] | None = None
    ) -> list[Any]:
        return await self._conn.execute_fetchall(sql, params or ())

    async def executescript(self, script: str) -> None:
        await self._conn.executescript(script)

    async def commit(self) -> None:
        await self._conn.commit()

    async def close(self) -> None:
        await self._conn.close()


class PostgresDbConn:
    """asyncpg-backed connection with SQLite-style placeholder translation."""

    def __init__(self, conn: Any):
        self._conn = conn
        self.dialect = "postgres"
        self._lastrowid: int | None = None

    async def execute(
        self, sql: str, params: Sequence[Any] | Mapping[str, Any] | None = None
    ) -> DbCursor:
        pg_sql, pg_params = self._adapt_sql(sql, params)
        upper = pg_sql.strip().upper()

        if upper.startswith("INSERT INTO TASK_EVENTS"):
            if "RETURNING" not in upper:
                pg_sql = f"{pg_sql.rstrip('; ')} RETURNING event_id"
            row = await self._fetchone_row(pg_sql, pg_params)
            self._lastrowid = int(row["event_id"]) if row is not None else None
            return DbCursor(lastrowid=self._lastrowid)

        if upper.startswith("SELECT"):
            rows = await self._fetch_rows(pg_sql, pg_params)
            return DbCursor(rows=rows)

        await self._run(pg_sql, pg_params)
        return DbCursor(lastrowid=self._lastrowid)

    async def execute_fetchall(
        self, sql: str, params: Sequence[Any] | Mapping[str, Any] | None = None
    ) -> list[Any]:
        pg_sql, pg_params = self._adapt_sql(sql, params)
        return await self._fetch_rows(pg_sql, pg_params)

    async def executescript(self, script: str) -> None:
        for statement in _split_sql_script(script):
            if statement.strip():
                await self._conn.execute(statement)

    async def commit(self) -> None:
        pass

    async def close(self) -> None:
        await self._conn.close()

    async def _run(self, pg_sql: str, pg_params: list[Any]) -> None:
        if pg_params:
            await self._conn.execute(pg_sql, *pg_params)
        else:
            await self._conn.execute(pg_sql)

    async def _fetchone_row(self, pg_sql: str, pg_params: list[Any]) -> Any | None:
        if pg_params:
            return await self._conn.fetchrow(pg_sql, *pg_params)
        return await self._conn.fetchrow(pg_sql)

    async def _fetch_rows(self, pg_sql: str, pg_params: list[Any]) -> list[Any]:
        if pg_params:
            return await self._conn.fetch(pg_sql, *pg_params)
        return await self._conn.fetch(pg_sql)

    @staticmethod
    def _adapt_sql(
        sql: str, params: Sequence[Any] | Mapping[str, Any] | None
    ) -> tuple[str, list[Any]]:
        if params is None:
            return sql, []
        if isinstance(params, Mapping):
            return _translate_named(sql, params)
        if "?" in sql:
            return _translate_qmark(sql, params)
        return sql, list(params)


def _split_sql_script(script: str) -> list[str]:
    statements: list[str] = []
    current: list[str] = []
    for line in script.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("--"):
            continue
        current.append(line)
        if stripped.endswith(";"):
            statements.append("\n".join(current))
            current = []
    if current:
        statements.append("\n".join(current))
    return statements


def is_postgres_url(url: str) -> bool:
    return url.startswith(("postgres://", "postgresql://"))
