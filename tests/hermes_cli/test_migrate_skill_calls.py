"""Exercise skill aggregation through a synthetic SQLite session database."""

import json
import os
import sqlite3
import subprocess
import sys
from collections import Counter
from contextlib import closing
from pathlib import Path
from types import SimpleNamespace

import pytest

from hermes_cli.migrate_skill_calls import skill_calls

_NOW = 1_800_000_000.0
_EARLIEST = _NOW - 30 * 86_400
_CALL = '[{"function":{"name":"skill_view","arguments":{"name":"sample"}}}]'


@pytest.fixture
def database(tmp_path: Path) -> Path:
    folder = tmp_path / "database"
    folder.mkdir()
    path = folder / "source #%.db"
    with closing(sqlite3.connect(path)) as connection, connection:
        connection.execute(
            """CREATE TABLE messages (
                id INTEGER PRIMARY KEY, role TEXT NOT NULL, timestamp REAL NOT NULL,
                content TEXT, tool_calls TEXT
            )"""
        )
    return path


def _insert(database: Path, rows: list[tuple[str, float, str | None]]) -> None:
    with closing(sqlite3.connect(database)) as connection, connection:
        connection.executemany(
            "INSERT INTO messages (role, timestamp, tool_calls, content) "
            "VALUES (?, ?, ?, CAST(X'80' AS TEXT))",
            rows,
        )


@pytest.mark.platforms("posix")
def test_counts_assistant_calls_at_both_inclusive_boundaries(database: Path) -> None:
    plain = {"function": {"name": "skill_view", "arguments": '{"name":"sample"}'}}
    linked = {"function": {"name": "skill_view", "arguments": {"name": "sample", "file_path": "ref.md"}}}
    null_path = {"function": {"name": "skill_view", "arguments": '{"name":"other","file_path":null}'}}
    another_tool = {"function": {"name": "other_tool", "arguments": {"name": "ignored"}}}
    outside = json.dumps([{"function": {"name": "skill_view", "arguments": {"name": "outside"}}}])
    _insert(database, [
        ("assistant", _EARLIEST, json.dumps([plain, linked, another_tool])),
        ("assistant", _NOW, json.dumps([plain, null_path])),
        ("assistant", _EARLIEST - 0.25, outside),
        ("assistant", _NOW + 0.25, outside),
        ("user", _NOW, outside),
        ("tool", _NOW, outside),
    ])

    assert skill_calls(database, _NOW) == (
        Counter({"sample": 3, "other": 1}),
        Counter({"sample": 1, "other": 1}),
    )


@pytest.mark.platforms("posix")
def test_skips_malformed_nested_json_without_losing_valid_calls(database: Path) -> None:
    valid = {"function": {"name": "skill_view", "arguments": {"name": "sample"}}}
    args_cases = [
        "{", '"{}"', [], "[{}]", None, False, 17, {}, {"name": []}, {"name": 5},
        {"name": None}, {"name": ""},
    ]
    malformed_calls = [
        None, 42, True, "{", json.dumps(valid), [], {"function": None},
        {"function": json.dumps(valid["function"])},
        {"function": {"name": "skill_view"}},
        *({"function": {"name": "skill_view", "arguments": args}} for args in args_cases),
    ]
    _insert(database, [
        ("assistant", _NOW, raw)
        for raw in (None, "{", '"[]"', json.dumps(valid), json.dumps([*malformed_calls, valid]))
    ])

    assert skill_calls(database, _NOW) == (Counter({"sample": 1}), Counter())


@pytest.mark.platforms("posix")
def test_sqlite_exposes_only_counted_fields(database: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    calls = json.dumps([{"function": {
        "name": "skill_view",
        "arguments": {"name": "sample", "file_path": "ref.md", "extra": "_sentinel_"},
    }}])
    _insert(database, [("assistant", _NOW, calls), ("assistant", _NOW, calls)])
    connect = sqlite3.connect
    projected_rows: list[tuple[str, int, int]] = []
    read_columns: set[str] = set()

    def authorize(
        action: int, table: str | None, column: str | None, _db: str | None, _origin: str | None
    ) -> int:
        if action == sqlite3.SQLITE_READ and table == "messages" and column:
            read_columns.add(column)
        return sqlite3.SQLITE_OK

    def inspect_row(_cursor: sqlite3.Cursor, row: tuple[str, int, int]) -> tuple[str, int, int]:
        projected_rows.append(row)
        return row

    def inspect_connection(path: str, *, uri: bool) -> sqlite3.Connection:
        connection = connect(path, uri=uri)
        connection.set_authorizer(authorize)
        connection.row_factory = inspect_row
        return connection

    monkeypatch.setattr(sqlite3, "connect", inspect_connection)
    assert skill_calls(database, _NOW) == (Counter({"sample": 2}), Counter({"sample": 2}))
    assert read_columns == {"role", "timestamp", "tool_calls"}
    assert projected_rows == [("sample", 1, 2)]


def test_missing_database_raises_without_creating_file(tmp_path: Path) -> None:
    missing = tmp_path / "missing.db"

    with pytest.raises(sqlite3.OperationalError):
        skill_calls(missing, _NOW)

    assert not missing.exists()


def _files(database: Path) -> dict[str, bytes]:
    return {path.name: path.read_bytes() for path in database.parent.iterdir()}


def test_closed_wal_uses_default_vfs_without_creating_sidecars(
    database: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    with closing(sqlite3.connect(database)) as writer:
        writer.execute("PRAGMA journal_mode = WAL")
    _insert(database, [("assistant", _NOW, _CALL)])
    before = _files(database)
    assert set(before) == {database.name}
    connect = sqlite3.connect

    def open_immutable(path: str, *, uri: bool) -> sqlite3.Connection:
        assert "immutable=1" in path and "vfs=" not in path
        return connect(path, uri=uri)

    monkeypatch.setattr(sqlite3, "connect", open_immutable)
    assert skill_calls(database, _NOW) == (Counter({"sample": 1}), Counter())
    assert _files(database) == before


def test_writable_active_wal_is_rejected_without_touching_sidecars(database: Path) -> None:
    with closing(sqlite3.connect(database)) as writer:
        writer.execute("PRAGMA journal_mode = WAL")
        writer.execute("INSERT INTO messages (role, timestamp, tool_calls) VALUES ('assistant', ?, ?)", (_NOW, _CALL))
        writer.commit()
        before = _files(database)
        assert before[database.name + "-wal"]

        with pytest.raises(sqlite3.OperationalError, match="read-only filesystem"):
            skill_calls(database, _NOW)
        assert _files(database) == before


@pytest.mark.platforms("posix")
def test_readonly_snapshot_counts_committed_wal_frames(database: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    # Leave a synthetic WAL and initialized SHM as a snapshot would. The last
    # read publishes a read mark for the committed frame before the process exits.
    subprocess.run(
        [sys.executable, "-B", "-c", """
import os
import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
connection.execute("PRAGMA journal_mode = WAL")
connection.execute("INSERT INTO messages (role, timestamp, tool_calls) VALUES ('assistant', ?, ?)",
                   (float(sys.argv[2]), sys.argv[3]))
connection.commit()
connection.execute("BEGIN")
connection.execute("SELECT COUNT(*) FROM messages").fetchone()
os._exit(0)
""", str(database), str(_NOW), _CALL],
        check=True,
    )
    before = _files(database)
    assert before[database.name + "-wal"]
    for path in database.parent.iterdir():
        path.chmod(0o444)
    # Emulate mount flags without requiring mount privileges. File permissions
    # also exercise SQLite's read-only SHM open when run without root.
    monkeypatch.setattr(os, "statvfs", lambda _path: SimpleNamespace(f_flag=os.ST_RDONLY))

    assert skill_calls(database, _NOW) == (Counter({"sample": 1}), Counter())
    assert _files(database) == before


@pytest.mark.platforms("posix")
def test_rollback_read_counts_only_committed_rows_during_write(database: Path) -> None:
    _insert(database, [("assistant", _NOW, _CALL)])
    with closing(sqlite3.connect(database)) as writer:
        writer.execute("INSERT INTO messages (role, timestamp, tool_calls) VALUES ('assistant', ?, ?)", (_NOW, _CALL))
        before = _files(database)
        assert before[database.name + "-journal"]

        assert skill_calls(database, _NOW) == (Counter({"sample": 1}), Counter())
        assert _files(database) == before


def test_immutable_read_rejects_source_changed_during_query(database: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    with closing(sqlite3.connect(database)) as writer:
        writer.execute("PRAGMA journal_mode = WAL")
    _insert(database, [("assistant", _NOW, _CALL)])
    connect = sqlite3.connect

    def change_mtime(_cursor: sqlite3.Cursor, row: tuple[str, int, int]) -> tuple[str, int, int]:
        info = database.stat()
        os.utime(database, ns=(info.st_atime_ns, info.st_mtime_ns + 1_000_000_000))
        return row

    def inspect_connection(path: str, *, uri: bool) -> sqlite3.Connection:
        connection = connect(path, uri=uri)
        connection.row_factory = change_mtime
        return connection

    monkeypatch.setattr(sqlite3, "connect", inspect_connection)
    with pytest.raises(sqlite3.OperationalError, match="source changed"):
        skill_calls(database, _NOW)


def test_rollback_journal_blocks_immutable_wal_read(database: Path) -> None:
    with closing(sqlite3.connect(database)) as writer:
        writer.execute("PRAGMA journal_mode = WAL")
    journal = database.with_name(database.name + "-journal")
    journal.touch()
    before = _files(database)

    with pytest.raises(sqlite3.OperationalError, match="rollback journal"):
        skill_calls(database, _NOW)
    assert _files(database) == before


@pytest.mark.platforms("posix")
def test_switch_to_wal_before_reader_open_cannot_create_sidecars(
    database: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    connect = sqlite3.connect

    def switch_then_connect(path: str, *, uri: bool) -> sqlite3.Connection:
        with closing(connect(database)) as writer:
            writer.execute("PRAGMA journal_mode = WAL")
        return connect(path, uri=uri)

    monkeypatch.setattr(sqlite3, "connect", switch_then_connect)
    with pytest.raises(sqlite3.OperationalError):
        skill_calls(database, _NOW)
    assert set(_files(database)) == {database.name}
