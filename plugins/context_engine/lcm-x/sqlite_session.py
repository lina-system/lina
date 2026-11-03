"""Explicit SQLite open recipes; schema and connection lifetime stay with stores."""

from __future__ import annotations

from collections.abc import Callable, Iterator
from contextlib import AbstractContextManager, contextmanager
from dataclasses import dataclass
import os
import sqlite3
from urllib.parse import unquote, urlsplit


@dataclass(frozen=True)
class ConnectionPolicy:
    """An exact connect call and the ordered operations before its row factory.

    Keyword pairs deliberately distinguish an omitted SQLite default from an
    explicitly supplied value (especially isolation_level=None and uri=True).
    A None row_factory leaves SQLite's default tuple rows untouched.
    Callbacks retain the caller's live bootstrap bindings and ownership timing.
    No schema, retries, transaction or close policy is added here.
    """

    database: str
    connect_kwargs: tuple[tuple[str, object], ...]
    before_row_factory: tuple[str | Callable[[sqlite3.Connection], None], ...]
    row_factory: type[sqlite3.Row] | None
    on_open: Callable[[sqlite3.Connection], None] | None
    on_setup_error: Callable[[Exception], None] | None


def store_backend(database: object, *, opening: bool = False) -> Callable[..., sqlite3.Connection] | None:
    """LINA: a connection factory when the host routes *database* to its Postgres store, else None."""
    try:
        from plugins.context_engine.lcm_x_store import backend_for
    except ImportError:
        return None
    return backend_for(database, opening=opening)


def connect_database(opener: Callable[..., sqlite3.Connection], database: str | os.PathLike,
                     **kwargs: object) -> sqlite3.Connection:
    """LINA: ``opener(database, **kwargs)`` (the caller's ``sqlite3.connect``, so a test that replaces its module's
    ``sqlite3`` still sees the call), or the host's store connection when the host routes that database (a path, or
    a ``file:`` URI with ``uri=True``) to its Postgres store."""
    path = str(database)
    if kwargs.get("uri") and path.startswith("file:"):
        parts = urlsplit(path)
        path = unquote(parts.path)
        kwargs = {**kwargs, "readonly": "mode=ro" in parts.query}
    backend = store_backend(path)
    if backend is not None:
        return backend(**{k: v for k, v in kwargs.items() if k in ("timeout", "isolation_level", "readonly")})
    kwargs.pop("readonly", None)
    return opener(database, **kwargs)


def configured_store_backend() -> str:
    """LINA: ``lcm.store_backend`` from the profile's config.yaml (``sqlite`` when unset; lina#16)."""
    from .config import _load_hermes_config_yaml
    section = _load_hermes_config_yaml().get("lcm")
    value = section.get("store_backend") if isinstance(section, dict) else None
    return str(value).strip().lower() if value else "sqlite"


def route_to_host_store(database: object, backend: str) -> bool:
    """LINA: route *database* to the host's store when ``lcm.store_backend`` is ``auto`` or ``postgres`` and the home
    has no ``lcm.db`` yet (a home with one stays on SQLite until lina#24 moves it). Returns whether it was routed."""
    if str(backend).strip().lower() not in ("auto", "postgres"):
        return False
    try:
        from plugins.context_engine.lcm_x_store import route_home
    except ImportError:
        return False
    return route_home(database)


def is_store_connection(conn: object) -> bool:
    """LINA: whether *conn* is a store connection (its schema is the host's migrations, not this plugin's DDL)."""
    return getattr(conn, "is_lina_store", False) is True


def open_connection(policy: ConnectionPolicy) -> sqlite3.Connection:
    """Run exactly the supplied recipe, preserving caller-owned error cleanup."""
    backend = store_backend(policy.database, opening=True)
    if backend is not None:
        # LINA: the SQLite pragmas of before_row_factory do not apply to the store.
        conn = backend(**dict(policy.connect_kwargs))
        if policy.on_open is not None:
            policy.on_open(conn)
        if policy.row_factory is not None:
            conn.row_factory = policy.row_factory
        return conn
    conn = sqlite3.connect(policy.database, **dict(policy.connect_kwargs))
    # Some stores publish the handle (and create their lock) before setup.
    # Those operations were outside their setup exception handler as well.
    if policy.on_open is not None:
        policy.on_open(conn)
    try:
        for operation in policy.before_row_factory:
            if isinstance(operation, str):
                conn.execute(operation)
            else:
                operation(conn)
        if policy.row_factory is not None:
            conn.row_factory = policy.row_factory
    except Exception as exc:
        if policy.on_setup_error is not None:
            policy.on_setup_error(exc)
        raise
    return conn


class TransactionOwner:
    """Join transactions on one store connection under its existing lock.

    The default retains implicit first-write opening. Explicit owners request
    BEGIN IMMEDIATE at entry. Savepoints, retry policies and schema scripts
    retain their existing owners; this primitive does not absorb those paths.
    """

    def __init__(self, lock: AbstractContextManager[object]) -> None:
        self._lock = lock
        self._active_connection: sqlite3.Connection | None = None

    def commit(self, conn: sqlite3.Connection) -> None:
        """Flush standalone helper writes; an enclosing atomic block flushes itself."""
        if self._active_connection is None or self._active_connection is not conn:
            conn.commit()

    @contextmanager
    def atomic(self, conn: sqlite3.Connection, *, immediate: bool = False) -> Iterator[None]:
        with self._lock:
            if self._active_connection is conn:
                if immediate and not conn.in_transaction:
                    # An empty enclosing scope has not reserved the database yet; reserve it here so a nested
                    # read-before-write keeps BEGIN IMMEDIATE semantics. The enclosing owner still commits.
                    conn.execute("BEGIN IMMEDIATE")
                yield
                return
            if immediate:
                conn.execute("BEGIN IMMEDIATE")
            self._active_connection = conn
            try:
                yield
                conn.commit()
            except BaseException:
                conn.rollback()
                raise
            finally:
                self._active_connection = None
