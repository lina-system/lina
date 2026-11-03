"""pytest plugin: run LCM-X's own suite on the memory store (lina#16).

``pytest -p plugins.context_engine.lcm_x_store_pytest`` from inside ``plugins/context_engine/lcm-x`` (LINA root on
``PYTHONPATH``, as ``.github/workflows/lcm-x.yml`` runs it). Every database path a test opens goes to the test
Postgres of ``tests/_fixtures/memory_store.py`` (``LINA_TEST_PG_DSN``, a local ``initdb`` with pgvector, or
Docker), one agent per path, and every test starts with empty tables. Without a Postgres the run fails: this
plugin exists to run the suite there.
"""

from __future__ import annotations

import hashlib
import importlib.util
import os
import re
import sqlite3
import sys
from urllib.parse import unquote, urlsplit
from pathlib import Path

import pytest

_REPO = Path(__file__).resolve().parents[2]


def _memory_store_fixture():
    # LCM-X's own tests package is also called ``tests``, so LINA's fixture module loads by path.
    name = "lina_memory_store_fixture"
    if name not in sys.modules:
        spec = importlib.util.spec_from_file_location(name, _REPO / "tests" / "_fixtures" / "memory_store.py")
        module = importlib.util.module_from_spec(spec)
        sys.modules[name] = module
        spec.loader.exec_module(module)
    return sys.modules[name]


SQLITE_ONLY_LIST = _REPO / ".github" / "lcm-x-postgres-sqlite-only.txt"


def sqlite_only_entries(path: Path = SQLITE_ONLY_LIST) -> list[str]:
    """The node ids (or whole test files) of ``.github/lcm-x-postgres-sqlite-only.txt``: one per line, then ``#`` and
    the reason. They test SQLite itself and keep running on the SQLite job."""
    entries = []
    for line in path.read_text(encoding="utf-8-sig").splitlines():
        entry = line.split("#", 1)[0].strip()
        if entry:
            entries.append(entry)
    return entries


def pytest_collection_modifyitems(config, items) -> None:
    entries = set(sqlite_only_entries())
    selected, deselected = [], []
    for item in items:
        (deselected if item.nodeid in entries or item.nodeid.split("::", 1)[0] in entries else selected).append(item)
    if deselected:
        config.hook.pytest_deselected(items=deselected)
        items[:] = selected


def _agent_for(path: str) -> str:
    return "lcm-test-" + hashlib.sha256(path.encode("utf-8", "surrogatepass")).hexdigest()[:16]


@pytest.fixture(autouse=True)
def _lcm_x_on_store(monkeypatch):
    import hermes_state_store
    from plugins.context_engine import lcm_x_store
    store = _memory_store_fixture().PROCESS_STORE
    dsn = store.app_dsn()
    monkeypatch.setenv("LINA_STORE_DSN", dsn)
    monkeypatch.setattr(hermes_state_store, "resolve_dsn", lambda: dsn)
    lcm_x_store.route_every_database(_agent_for)
    original_connect = sqlite3.connect

    def connect(database, *args, **kwargs):
        # A test that opens its database file directly to look inside reads the store instead.
        path = str(database)
        if kwargs.get("uri") and path.startswith("file:"):
            path = unquote(urlsplit(path).path)
        backend = lcm_x_store.backend_for(path) if not args else None
        if backend is not None:
            return backend(timeout=kwargs.get("timeout", 5.0), isolation_level=kwargs.get("isolation_level", ""))
        if not args and not kwargs.get("uri") and not Path(path).exists() and Path(path).name != "state.db":
            return _FirstStatement(lambda: original_connect(database, **kwargs), lambda: lcm_x_store.backend_for(
                path, opening=True)(timeout=kwargs.get("timeout", 5.0), isolation_level=kwargs.get("isolation_level", "")))
        return original_connect(database, *args, **kwargs)
    monkeypatch.setattr(sqlite3, "connect", connect)
    monkeypatch.setattr(lcm_x_store, "_vector_text", _widened_vector_text(lcm_x_store._vector_text))
    yield
    lcm_x_store.route_every_database(None)
    hermes_state_store.TEXT_CACHE.clear()
    _drop_leftover_connections(store)
    store.reset()


_LCM_X_TABLE = re.compile(r"^\s*CREATE\s+(?:VIRTUAL\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?[\"`]?(?:messages|summary_nodes|lcm_\w+)\b",
                          re.IGNORECASE)


class _FirstStatement:
    """A new database file a test seeds itself before any LCM-X store opens it (its own minimal ``messages``
    table, say). The first statement decides: LCM-X's schema goes to the store, like the store that opens the
    path next; anything else is a plain SQLite file."""

    def __init__(self, open_sqlite, open_store):
        object.__setattr__(self, "_open", (open_sqlite, open_store))
        object.__setattr__(self, "_conn", None)
        object.__setattr__(self, "_pending", {})

    def _decide(self, sql=""):
        if self._conn is None:
            conn = self._open[1 if _LCM_X_TABLE.match(sql or "") else 0]()
            for name, value in self._pending.items():
                setattr(conn, name, value)
            object.__setattr__(self, "_conn", conn)
        return self._conn

    def execute(self, sql, *args, **kwargs):
        return self._decide(sql).execute(sql, *args, **kwargs)

    def executemany(self, sql, *args, **kwargs):
        return self._decide(sql).executemany(sql, *args, **kwargs)

    def executescript(self, script):
        return self._decide(script).executescript(script)

    def close(self):
        if self._conn is not None:
            self._conn.close()

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return self._decide().__exit__(*exc)

    def __getattr__(self, name):
        if name in self._pending:
            return self._pending[name]
        return getattr(self._decide(), name)

    def __setattr__(self, name, value):
        if self._conn is None:
            self._pending[name] = value
        else:
            setattr(self._conn, name, value)


def _widened_vector_text(vector_text):
    """The suite's fake embedders write a few floats; the store holds the one 1024-wide model (lina#19). On this
    run a narrower fake is widened with zeros, which keeps its cosine distances, and the views read it back cut
    to the profile's width. Production refuses every vector that is not 1024 wide."""
    def widened(value):
        if isinstance(value, (bytes, bytearray, memoryview)) and not len(value) % 4 and 0 < len(value) < 4096:
            value = bytes(value) + bytes(4096 - len(value))
        return vector_text(value)
    return widened


def _drop_leftover_connections(store) -> None:
    """A test that never closes a store leaves its connection idle in a transaction; on SQLite that held a
    lock on the test's own file only, here it would hold the emptying ``TRUNCATE`` forever."""
    import psycopg
    with psycopg.connect(store.superuser_dsn(), autocommit=True) as conn:
        conn.execute("SELECT pg_terminate_backend(pid) FROM pg_stat_activity "
                     "WHERE datname = current_database() AND usename = 'lina_app' AND pid <> pg_backend_pid()")


def pytest_unconfigure(config) -> None:
    if "lina_memory_store_fixture" in sys.modules:
        sys.modules["lina_memory_store_fixture"].PROCESS_STORE.close()
    os.environ.pop("LINA_STORE_DSN", None)
