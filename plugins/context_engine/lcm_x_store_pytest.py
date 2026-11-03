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
import json
import os
import sqlite3
import sys
import threading
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
    monkeypatch.setenv(lcm_x_store.TEST_ROUTES_ENV, "{}")  # what a child process of the test routes
    original_connect = sqlite3.connect

    def connect(database, *args, **kwargs):
        # A test that opens its database file directly to look inside reads the store instead.
        path = str(database)
        if kwargs.get("uri") and path.startswith("file:"):
            path = unquote(urlsplit(path).path)
        backend = lcm_x_store.backend_for(path) if not args else None
        if backend is not None:
            store = backend(timeout=kwargs.get("timeout", 5.0), isolation_level=kwargs.get("isolation_level", ""))
            factory = kwargs.get("factory")
            return _bridged(factory, store) if factory not in (None, sqlite3.Connection) else store
        return original_connect(database, *args, **kwargs)
    monkeypatch.setattr(sqlite3, "connect", connect)
    monkeypatch.setattr(lcm_x_store, "backend_for", _carry_seeded_file(lcm_x_store, original_connect, lambda: connect))
    monkeypatch.setattr(lcm_x_store, "_vector_text", _widened_vector_text(lcm_x_store._vector_text))
    yield
    lcm_x_store.route_every_database(None)
    hermes_state_store.TEXT_CACHE.clear()
    _drop_leftover_connections(store)
    store.reset()


def _carry_seeded_file(lcm_x_store, original_connect, own_connect):
    """A test that seeds a new database file itself (its own minimal ``messages`` or ``metadata`` table) before an
    LCM-X store opens the path: when the store opens it, the seeded rows are carried into the store, as if the test
    had written them there. Other connects to the path follow the store from then on."""
    backend_for = lcm_x_store.backend_for

    def carried(database, *, opening=False):
        seen = opening and lcm_x_store._route_key(database) in lcm_x_store._route_every_seen
        factory = backend_for(database, opening=opening)
        if opening and not seen and factory is not None:
            routes = json.loads(os.environ.get(lcm_x_store.TEST_ROUTES_ENV) or "{}")
            key = lcm_x_store._route_key(database)
            routes[key] = _agent_for(key)
            os.environ[lcm_x_store.TEST_ROUTES_ENV] = json.dumps(routes)
        if not opening or seen or factory is None or not Path(str(database)).is_file():
            return factory
        seeded = []
        source = original_connect(str(database))
        try:
            for table in _SEEDED_TABLES:
                if source.execute("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?", (table,)).fetchone():
                    cursor = source.execute(f"SELECT * FROM {table} ORDER BY rowid")
                    seeded.append((table, [d[0] for d in cursor.description], cursor.fetchall()))
        finally:
            source.close()
        if any(rows for _, _, rows in seeded):
            target = factory()
            try:
                for table, names, rows in seeded:
                    if rows:
                        target.executemany(f"INSERT INTO {table} ({', '.join(names)}) "
                                           f"VALUES ({', '.join('?' * len(names))})", rows)
                target.commit()
            finally:
                target.close()
        return factory

    def opened(database, *, opening=False):
        factory = carried(database, opening=opening)
        if not opening or factory is None:
            return factory

        def connect(**kwargs):
            # A test that replaced sqlite3.connect (a factory= subclass, say) sees an LCM-X store's open too, as on
            # SQLite, where the store opens through sqlite3.connect.
            if sqlite3.connect is not own_connect():
                kwargs.pop("readonly", None)
                return sqlite3.connect(str(database), **kwargs)
            return factory(**kwargs)
        return connect
    return opened


_SEEDED_TABLES = ("metadata", "messages")


class _StoreConnection(sqlite3.Connection):
    """The base a test's ``sqlite3.Connection`` subclass (``factory=``) sits on when its database is on the store: the
    subclass's own methods run, and what they hand to ``super()`` goes to the store connection."""

    _opening = threading.local()

    def __init__(self, *args, **kwargs):
        super().__init__(":memory:")
        object.__setattr__(self, "_store", type(self)._opening.store)

    def execute(self, sql, parameters=(), /):
        return self._store.execute(sql, parameters)

    def executemany(self, sql, parameters, /):
        return self._store.executemany(sql, parameters)

    def executescript(self, script, /):
        return self._store.executescript(script)

    def cursor(self, factory=None):
        return self._store.cursor()

    def commit(self):
        self._store.commit()

    def rollback(self):
        self._store.rollback()

    def close(self):
        self._store.close()
        super().close()

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        return self._store.__exit__(exc_type, exc, tb)

    def set_trace_callback(self, callback):
        self._store.set_trace_callback(callback)

    def create_function(self, name, *args, **kwargs):
        self._store.create_function(name, *args, **kwargs)

    def interrupt(self):
        self._store.interrupt()

    def backup(self, *args, **kwargs):
        self._store.backup(*args, **kwargs)

    in_transaction = property(lambda self: self._store.in_transaction)
    total_changes = property(lambda self: self._store.total_changes)
    isolation_level = property(lambda self: self._store.isolation_level,
                               lambda self, value: setattr(self._store, "isolation_level", value))
    row_factory = property(lambda self: self._store.row_factory,
                           lambda self, value: setattr(self._store, "row_factory", value))
    text_factory = property(lambda self: self._store.text_factory,
                            lambda self, value: setattr(self._store, "text_factory", value))

    def __getattr__(self, name):
        return getattr(object.__getattribute__(self, "_store"), name)


_BRIDGES: dict[type, type] = {}


def _bridged(factory: type, store):
    if factory not in _BRIDGES:
        _BRIDGES[factory] = type(f"Store{factory.__name__}", (factory, _StoreConnection), {})
    _StoreConnection._opening.store = store
    try:
        return _BRIDGES[factory]()
    finally:
        _StoreConnection._opening.store = None


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
