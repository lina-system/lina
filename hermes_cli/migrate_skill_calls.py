"""Aggregate recent skill calls from a standard SQLite session database."""

import json
import os
import sqlite3
import subprocess
import sys
from collections import Counter
from contextlib import closing
from pathlib import Path
from typing import Final

_WINDOW_SECONDS: Final = 30 * 24 * 60 * 60

# json_each returns unquoted SQL text for scalar strings. Guard each JSON
# boundary in CASE so malformed data stays safe even if SQLite reorders filters.
_SKILL_CALLS_SQL: Final = """
WITH calls AS (
    SELECT CASE WHEN entry.type = 'object' THEN entry.value END AS call
    FROM messages AS message
    CROSS JOIN json_each(
        CASE WHEN json_valid(message.tool_calls) THEN
            CASE WHEN json_type(message.tool_calls) = 'array' THEN message.tool_calls END
        END
    ) AS entry
    WHERE message.role = 'assistant' AND message.timestamp BETWEEN ? AND ?
),
arguments AS (
    SELECT CASE json_type(call, '$.function.arguments')
        WHEN 'object' THEN json_extract(call, '$.function.arguments')
        WHEN 'text' THEN
            CASE WHEN json_valid(json_extract(call, '$.function.arguments'))
                 THEN json_extract(call, '$.function.arguments') END
        END AS args
    FROM calls
    WHERE json_extract(call, '$.function.name') = 'skill_view'
),
skill_names AS (
    SELECT json_extract(args, '$.name') AS name,
           json_type(args, '$.file_path') IS NOT NULL AS has_file_path
    FROM arguments
    WHERE json_type(args, '$.name') = 'text'
)
SELECT name, has_file_path, COUNT(*)
FROM skill_names
WHERE name <> ''
GROUP BY name, has_file_path
"""


def _count(
    uri: str, now: float, *, exclusive: bool = False
) -> tuple[Counter[str], Counter[str]]:
    total: Counter[str] = Counter()
    with_file_path: Counter[str] = Counter()
    with closing(sqlite3.connect(uri, uri=True)) as connection:
        connection.execute("PRAGMA query_only = ON")
        connection.execute("PRAGMA temp_store = MEMORY")
        if exclusive:
            # Before the first database read, require WAL to take an exclusive
            # main-file lock instead of opening shared memory. A read-only
            # descriptor cannot take that write lock. Rollback reads still
            # take SQLite's normal shared lock.
            connection.execute("PRAGMA locking_mode = EXCLUSIVE")
        for name, has_file_path, count in connection.execute(
            _SKILL_CALLS_SQL, (now - _WINDOW_SECONDS, now)
        ):
            total[name] += count
            if has_file_path:
                with_file_path[name] = count
    return total, with_file_path


def _stamp(path: Path) -> tuple[int, int, int, int, int] | None:
    """Fingerprint a file or directory without opening a database descriptor."""
    try:
        info = path.lstat()
    except FileNotFoundError:
        return None
    return info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns


def _readonly_mount(path: Path) -> bool:
    return hasattr(os, "statvfs") and bool(os.statvfs(path).f_flag & os.ST_RDONLY)


def _count_readonly_wal(uri: str, now: float) -> tuple[Counter[str], Counter[str]]:
    # A fresh process keeps SQLite from reusing a writable SHM mapping
    # already opened in this process through a different mount of the inode.
    script = """
import json
import sys

sys.path.insert(0, sys.argv[1])
from hermes_cli.migrate_skill_calls import _count

print(json.dumps(_count(sys.argv[2], float(sys.argv[3]))))
"""
    try:
        child = subprocess.run(
            [sys.executable, "-IB", "-c", script, str(Path(__file__).resolve().parent.parent),
             uri + "&readonly_shm=1", str(now)],
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=False, timeout=300,
        )
    except subprocess.TimeoutExpired as exc:
        raise sqlite3.OperationalError("timed out counting calls in read-only WAL") from exc
    if child.returncode:
        raise sqlite3.OperationalError("could not read committed WAL frames from the read-only filesystem")
    total, with_file_path = json.loads(child.stdout)
    return Counter(total), Counter(with_file_path)


def skill_calls(database: Path, now: float) -> tuple[Counter[str], Counter[str]]:
    """Return total and file-path-present call counts by skill over the last 30 days."""
    database = database.resolve()
    wal, shm, journal = (database.with_name(database.name + suffix)
                         for suffix in ("-wal", "-shm", "-journal"))
    paths = (database, database.parent, wal, shm, journal)
    before = tuple(map(_stamp, paths))
    if before[0] is None:
        raise sqlite3.OperationalError("SQLite source database does not exist")
    uri = database.as_uri() + "?mode=ro"

    if before[2] is not None or before[3] is not None:
        # SQLite can use an existing WAL index read-only if a suitable read
        # mark is already available. Require filesystem-enforced protection
        # for both sidecars and their directory before allowing shared memory.
        if (before[2] is not None and before[3] is not None
                and all(map(_readonly_mount, (database.parent, database, wal, shm)))):
            return _count_readonly_wal(uri + "&vfs=unix", now)
        raise sqlite3.OperationalError("WAL sidecars require a read-only filesystem")

    if before[4] is not None:
        # Use SQLite's locking and hot-journal detection while a writer may
        # have uncommitted pages. Refuse a switch to WAL without opening SHM.
        try:
            return _count(uri + "&vfs=unix", now, exclusive=True)
        except sqlite3.OperationalError as exc:
            raise sqlite3.OperationalError(f"could not read database with rollback journal: {exc}") from exc

    # A clean database needs no sidecars. Check the directory as well as the
    # files to detect a journal that appeared and disappeared during the read.
    result = _count(uri + "&immutable=1", now)
    if before != tuple(map(_stamp, paths)):
        raise sqlite3.OperationalError("SQLite source changed during immutable read")
    return result
