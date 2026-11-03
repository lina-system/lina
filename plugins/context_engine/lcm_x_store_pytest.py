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
import re
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
    schema = _TestSchema(store)
    monkeypatch.setattr(lcm_x_store.PgConnection, "_ddl",
                        lambda connection, cur, template, params: schema.handle(connection, cur, template, params))
    yield
    schema.drop()
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


_TYPES = {"INTEGER": "bigint", "INT": "bigint", "REAL": "double precision", "BLOB": "bytea", "TEXT": "text",
          "NUMERIC": "numeric"}
_CREATE_TABLE = re.compile(r"\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?[\"`]?(\w+)[\"`]?\s*\((.*)\)\s*;?\s*$",
                           re.IGNORECASE | re.DOTALL)
_CREATE_TRIGGER = re.compile(
    r"\s*CREATE\s+TRIGGER\s+(?:IF\s+NOT\s+EXISTS\s+)?[\"`]?(\w+)[\"`]?\s+(BEFORE|AFTER)\s+"
    r"(INSERT|DELETE|UPDATE(?:\s+OF\s+[\w\s,]+?)?)\s+ON\s+[\"`]?(\w+)[\"`]?\s+(?:FOR\s+EACH\s+ROW\s+)?"
    r"(?:WHEN\s+(.*?)\s+)?BEGIN\s+(.*?)\s*END\s*;?\s*$", re.IGNORECASE | re.DOTALL)
_DROP_TABLE = re.compile(r"\s*DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?[\"`]?(\w+)[\"`]?\s*;?\s*$", re.IGNORECASE)


class _TestSchema:
    """A test's own tables and triggers (a trace table and a trigger that fills it, say): LCM-X's schema is the
    store's migrations, but what a test adds for itself is created in the test database's ``public`` schema (its
    SQLite triggers as PL/pgSQL) and dropped when the test ends."""

    def __init__(self, store) -> None:
        self._store, self._tables, self._triggers = store, [], []

    def _run(self, sql: str) -> None:
        import psycopg
        with psycopg.connect(self._store.superuser_dsn(), autocommit=True) as conn:
            conn.execute("SET lock_timeout = '10s'")
            conn.execute(sql)

    def handle(self, connection, cur, template, params) -> None:
        from plugins.context_engine import lcm_x_store
        sql = template.parts[0]
        catalog = connection._catalog
        table = _CREATE_TABLE.match(sql)
        if table and not catalog.columns(table.group(1)) and table.group(1).lower() != "sqlite_sequence":
            self._create_table(table.group(1), table.group(2))
        trigger = _CREATE_TRIGGER.match(sql)
        if trigger and any(re.search(rf"\b{name}\b", trigger.group(6), re.IGNORECASE) for name in self._tables):
            self._create_trigger(trigger, catalog)
        dropped = _DROP_TABLE.match(sql)
        if dropped and dropped.group(1) in self._tables:
            self._run(f'DROP TABLE IF EXISTS public."{dropped.group(1)}" CASCADE')
            self._tables.remove(dropped.group(1))
        cur._set_result(None, [], -1, None)

    def _create_table(self, name: str, body: str) -> None:
        from plugins.context_engine import lcm_x_store
        columns = []
        for column in lcm_x_store._split_top_level(body):
            words = column.split()
            if len(words) == 1 or words[1].upper() in ("PRIMARY", "REFERENCES", "NOT", "UNIQUE", "DEFAULT", "CHECK"):
                words.insert(1, "text")
            else:
                words[1] = _TYPES.get(words[1].upper(), words[1])
            columns.append(" ".join(words))
        self._run(f'CREATE TABLE IF NOT EXISTS public."{name}" ({", ".join(columns)}, '
                  f'rowid bigint GENERATED ALWAYS AS IDENTITY); GRANT ALL ON public."{name}" TO lina_app')
        self._tables.append(name)

    def _create_trigger(self, trigger, catalog) -> None:
        from plugins.context_engine import lcm_x_store
        name, when, event, table, condition, body = trigger.groups()
        target = "ctx.lcm_messages" if table.lower() == "messages" else f"ctx.{table}"
        statements = []
        for statement in lcm_x_store.split_statements(body):
            text, _args = lcm_x_store.render(lcm_x_store.translate(statement, catalog), ())
            statements.append(text.rstrip(";") + ";")
        returned = "NULL" if when.upper() == "AFTER" else ("OLD" if event.upper() == "DELETE" else "NEW")
        function = f'public."lcm_test_{name}"'
        self._run(f"CREATE OR REPLACE FUNCTION {function}() RETURNS trigger LANGUAGE plpgsql AS $lcm$ BEGIN "
                  f"IF {condition or 'TRUE'} THEN {' '.join(statements)} END IF; RETURN {returned}; END $lcm$; "
                  f'CREATE TRIGGER "{name}" {when} {event} ON {target} FOR EACH ROW EXECUTE FUNCTION {function}()')
        self._triggers.append((name, target, function))

    def drop(self) -> None:
        for name, target, function in reversed(self._triggers):
            self._run(f'DROP TRIGGER IF EXISTS "{name}" ON {target}; DROP FUNCTION IF EXISTS {function}()')
        for name in reversed(self._tables):
            self._run(f'DROP TABLE IF EXISTS public."{name}" CASCADE')
        self._tables, self._triggers = [], []


def _widened_vector_text(vector_text):
    """The suite's fake embedders write a few floats; the store holds the one 1024-wide model (lina#19). On this
    run a narrower fake is widened with zeros, which keeps its cosine distances, and the views read it back cut
    to the profile's width. Production refuses every vector that is not 1024 wide."""
    zeros = {}

    def widened(value):
        if isinstance(value, (bytes, bytearray, memoryview)) and not len(value) % 4 and 0 < len(value) < 4096:
            pad = (4096 - len(value)) // 4
            if pad not in zeros:
                zeros[pad] = ",0" * pad
            return vector_text(value)[:-1] + zeros[pad] + "]"
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
