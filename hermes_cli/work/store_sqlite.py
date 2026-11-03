"""The SQLite (WAL) backend of the work record: ``schema_sqlite.sql`` in one file (``lina work`` default)."""

from __future__ import annotations

import sqlite3
from pathlib import Path

SCHEMA = Path(__file__).resolve().parent / "schema_sqlite.sql"


class SqliteDb:
    dialect = "sqlite"

    def __init__(self, conn: sqlite3.Connection, human: str, names: dict[str, str]):
        self.conn, self.human, self.names = conn, human, names

    def execute(self, sql: str, params=()):
        return self.conn.execute(sql, tuple(params))

    def begin(self) -> None:
        self.conn.execute("BEGIN IMMEDIATE")

    def commit(self) -> None:
        self.conn.execute("COMMIT")

    def rollback(self) -> None:
        if self.conn.in_transaction:
            self.conn.execute("ROLLBACK")

    def close(self) -> None:
        self.conn.close()

    def columns(self, table: str) -> list[str]:
        return [c[1] for c in self.conn.execute(f"PRAGMA table_info({table})")]

    @staticmethod
    def jget(column: str, key: str) -> str:
        return f"json_extract({column}, '$.{key}')"

    @staticmethod
    def jtext(column: str) -> str:
        return column


def connect(path: Path, human: str, names: dict[str, str], *, create: bool = False,
            readonly: bool = False) -> SqliteDb:
    """Open a work.db (``create``: make it and its schema when missing; ``readonly``: an import source)."""
    path = Path(path)
    if not path.exists() and not create:
        raise FileNotFoundError(str(path))
    if readonly:
        conn = sqlite3.connect(f"{path.resolve().as_uri()}?mode=ro", uri=True, timeout=60, isolation_level=None)
        return SqliteDb(conn, human, names)
    path.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(str(path), timeout=60, isolation_level=None)
    conn.execute("PRAGMA busy_timeout=60000")
    conn.execute("PRAGMA journal_mode=WAL")
    conn.executescript(SCHEMA.read_text(encoding="utf-8-sig"))
    conn.execute("PRAGMA synchronous=FULL")
    conn.execute("PRAGMA foreign_keys=ON")
    return SqliteDb(conn, human, names)
