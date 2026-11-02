"""Work backend over a persistent psql process, without a Python database driver."""

from __future__ import annotations

import json
import os
import re
import selectors
import subprocess
import time
import uuid
from collections.abc import Iterable, Iterator
from typing import Final, assert_never

from hermes_cli import store_migrations

Scalar = str | int | float | bool | None
Json = Scalar | list["Json"] | dict[str, "Json"]
TOKEN: Final = re.compile(r"'(''|[^'])*'|\"(\"\"|[^\"])*\"|\$([A-Za-z_][A-Za-z_0-9]*)?\$[\s\S]*?\$\3\$|--[^\n]*|/\*[\s\S]*?\*/|\?")
TIMEOUT: Final = 60.0


class PgError(RuntimeError):
    """psql failed or failed to answer within the statement deadline."""


def literal(value: Scalar) -> str:
    match value:
        case None:
            return "NULL"
        case bool():
            text = "true" if value else "false"
        case str() | int() | float():
            text = str(value)
        case unreachable:
            assert_never(unreachable)
    if "\0" in text:
        raise PgError("Postgres SQL parameters cannot contain NUL")
    return "'" + text.replace("'", "''") + "'"


def bind(sql: str, params: Iterable[Scalar]) -> str:
    values = iter(params)

    def replace(match: re.Match[str]) -> str:
        if match[0] != "?":
            return match[0]
        try:
            return literal(next(values))
        except StopIteration as exc:
            raise PgError("Not enough SQL parameters") from exc

    result = TOKEN.sub(replace, sql)
    if next(values, ...) is not ...:
        raise PgError("Too many SQL parameters")
    return result


class Cursor:
    """A consumed psql result with SQLite-compatible cursor operations."""

    def __init__(self, rows: list[list[Json]]):
        self.rows = iter(tuple(row) for row in rows)

    def fetchone(self) -> tuple[Json, ...] | None:
        return next(self.rows, None)

    def fetchall(self) -> list[tuple[Json, ...]]:
        return list(self.rows)

    def __iter__(self) -> Iterator[tuple[Json, ...]]:
        return self.rows


class PgDb:
    """Own a mutable psql session; a failed process is discarded before reconnecting."""

    dialect = "postgres"

    def __init__(self, argv: list[str], human: str, names: dict[str, str]):
        self.argv, self.human, self.names = list(argv), human, names
        self.process: subprocess.Popen[bytes] | None = None

    def _start(self) -> None:
        self.process = subprocess.Popen(
            [*self.argv, "-X", "-q", "-At", "-v", "ON_ERROR_STOP=1"],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        self.run_sql("SET standard_conforming_strings = on; SET search_path TO work, public; SET timezone TO 'UTC';")

    def run_sql(self, sql: str) -> str:
        """Run raw SQL, collecting both pipes under one bounded I/O deadline."""
        if self.process is None:
            self._start()
        proc = self.process
        assert proc is not None and proc.stdin is not None and proc.stdout is not None and proc.stderr is not None
        sentinel = "lina_" + uuid.uuid4().hex
        data = memoryview((sql.rstrip().rstrip(";") + f";\n\\echo {sentinel}\n").encode("utf-8"))
        stdout, stderr = bytearray(), bytearray()
        deadline = time.monotonic() + TIMEOUT
        try:
            with selectors.DefaultSelector() as selector:
                for stream in (proc.stdin, proc.stdout, proc.stderr):
                    os.set_blocking(stream.fileno(), False)
                selector.register(proc.stdin, selectors.EVENT_WRITE)
                selector.register(proc.stdout, selectors.EVENT_READ)
                selector.register(proc.stderr, selectors.EVENT_READ)
                while True:
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        raise PgError(f"psql timed out after {TIMEOUT}s: {stderr.decode('utf-8', errors='replace')}")
                    for key, _ in selector.select(remaining):
                        if key.fileobj is proc.stdin:
                            data = data[os.write(key.fd, data):]
                            if not data:
                                selector.unregister(proc.stdin)
                            continue
                        chunk = os.read(key.fd, 65536)
                        target = stdout if key.fileobj is proc.stdout else stderr
                        target.extend(chunk)
                        if not chunk:
                            selector.unregister(key.fileobj)
                    marker = sentinel.encode() + b"\n"
                    if stdout.endswith(marker):
                        return stdout[:-len(marker)].decode("utf-8")
                    if not selector.get_map():
                        raise PgError("psql exited: " + stderr.decode("utf-8", errors="replace").strip())
        except (OSError, PgError) as exc:
            self.close()
            raise PgError(str(exc)) from exc

    def execute(self, sql: str, params: Iterable[Scalar] = ()) -> Cursor:
        statement = sql.strip().rstrip(";")
        statement = bind(statement, params)
        returns = re.match(r"(?is)^\s*(SELECT|WITH|VALUES|TABLE)\b", statement)
        dml = re.match(r"(?is)^\s*(INSERT|UPDATE|DELETE)\b", statement)
        if returns or (dml and re.search(r"(?i)\bRETURNING\b", sql)):
            prefix = ""
            if dml:
                prefix, statement = f"WITH changed AS ({statement}) ", "SELECT * FROM changed"
            output = self.run_sql(prefix + "SELECT coalesce(json_agg((SELECT json_agg(value) "
                                  "FROM json_each(row_to_json(t)))), '[]') FROM (" + statement + ") t")
            return Cursor(json.loads(output))
        self.run_sql(statement)
        return Cursor([])

    def begin(self) -> None:
        self.run_sql("BEGIN")

    def commit(self) -> None:
        self.run_sql("COMMIT")

    def rollback(self) -> None:
        if self.process is not None:
            self.run_sql("ROLLBACK")

    def close(self) -> None:
        proc, self.process = self.process, None
        if proc is not None:
            if proc.poll() is None:
                proc.kill()
            proc.wait(timeout=TIMEOUT)
            for stream in (proc.stdin, proc.stdout, proc.stderr):
                if stream is not None:
                    stream.close()

    def columns(self, table: str) -> list[str]:
        return [str(row[0]) for row in self.execute(
            "SELECT column_name FROM information_schema.columns WHERE table_schema = 'work' "
            "AND table_name = ? ORDER BY ordinal_position", (table,))]

    @staticmethod
    def jget(column: str, key: str) -> str:
        return f"({column}->>{literal(key)})"

    @staticmethod
    def jtext(column: str) -> str:
        return f"{column}::text"


def connect(argv: list[str], human: str, names: dict[str, str], *, ensure_schema: bool = True) -> PgDb:
    """Open psql and optionally bring the numbered store migrations up to date."""
    db = PgDb(argv, human, names)
    try:
        if ensure_schema:
            store_migrations.apply(db.run_sql)
        else:
            db.run_sql("SELECT 1")
    except (PgError, OSError):
        db.close()
        raise
    return db
