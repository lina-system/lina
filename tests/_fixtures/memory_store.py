"""A real Postgres with pgvector for the tests that read and write ``memory.messages`` (lina#12).

The cluster comes from, in order:

1. ``LINA_TEST_PG_DSN``: a superuser DSN of a throwaway cluster (``scripts/run_tests_parallel.py``
   starts one for the whole run and passes it to every test process);
2. ``initdb``/``pg_ctl`` on this machine, when their Postgres has the ``vector`` extension;
3. ``docker run pgvector/pgvector:0.8.7-pg15-bookworm``.

The cluster is bootstrapped once with ``services/store/initdb`` and LINA's migrations into the database
``lina``, which is the template: each test process gets its own copy (``CREATE DATABASE ... TEMPLATE``)
and every test starts with its tables empty. Store-backed tests skip with one reason when no cluster can
be had, except under ``CI=true``, where that is a failure.
"""

from __future__ import annotations

import os
import re
import shutil
import subprocess
import tempfile
import threading
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, Optional

import pytest

REPO = Path(__file__).resolve().parents[2]
INITDB_SCRIPT = REPO / "services" / "store" / "initdb" / "10-lina-store.sh"
IMAGE = "pgvector/pgvector:0.8.7-pg15-bookworm"
TEMPLATE_DB = "lina"
APP_PASSWORD = "lina-test-app"
HONCHO_PASSWORD = "lina-test-honcho"
_FAST = ("-c", "fsync=off", "-c", "full_page_writes=off", "-c", "synchronous_commit=off",
         "-c", "max_connections=1000")
UNAVAILABLE_ENV = "LINA_TEST_PG_UNAVAILABLE"
CLUSTER_ENV = "LINA_TEST_PG_DSN"


class ClusterUnavailable(RuntimeError):
    pass


@dataclass
class Cluster:
    dsn: str
    how: str
    stop: Callable[[], None] = field(default=lambda: None)


def _connect(dsn: str, **kw):
    import psycopg
    return psycopg.connect(dsn, autocommit=True, connect_timeout=5, **kw)


def _with_db(dsn: str, dbname: str) -> str:
    from psycopg.conninfo import conninfo_to_dict, make_conninfo
    return make_conninfo(**{**conninfo_to_dict(dsn), "dbname": dbname})


def _as_app(dsn: str, dbname: str) -> str:
    from psycopg.conninfo import conninfo_to_dict, make_conninfo
    return make_conninfo(**{**conninfo_to_dict(dsn), "dbname": dbname, "user": "lina_app",
                            "password": APP_PASSWORD})


def _wait_ready(dsn: str, timeout: float = 60.0) -> None:
    import psycopg
    from tests.waiting import wait_until

    def ready() -> bool:
        try:
            with _connect(_with_db(dsn, "postgres")):
                return True
        except psycopg.OperationalError:
            return False
    wait_until(ready, timeout=timeout, interval=0.2, what="the test Postgres to accept connections")


def _initdb_sql() -> str:
    """The SQL of services/store/initdb, with its psql variables bound and its roles created only once."""
    script = INITDB_SCRIPT.read_text(encoding="utf-8")
    sql = script.split("<<'SQL'\n", 1)[1].rsplit("\nSQL", 1)[0]
    for role, var, password in (("lina_app", "app_password", APP_PASSWORD),
                                ("honcho", "honcho_password", HONCHO_PASSWORD)):
        sql = re.sub(
            rf"CREATE ROLE {role} LOGIN PASSWORD :'{var}';",
            f"DO $r$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '{role}') THEN "
            f"CREATE ROLE {role} LOGIN; END IF; END $r$; ALTER ROLE {role} PASSWORD '{password}';",
            sql)
    return sql


def bootstrap(dsn: str) -> None:
    """Create the template database ``lina`` (initdb script + migrations) unless it is there already."""
    from hermes_cli.store_migrations import apply
    with _connect(_with_db(dsn, "postgres")) as admin:
        admin.execute("SELECT pg_advisory_lock(4210012)")
        try:
            if admin.execute("SELECT 1 FROM pg_database WHERE datname = %s", (TEMPLATE_DB,)).fetchone():
                return
            admin.execute(f"CREATE DATABASE {TEMPLATE_DB}")
            try:
                with _connect(_with_db(dsn, TEMPLATE_DB)) as conn:
                    conn.execute(_initdb_sql())

                    def run(sql: str) -> str:
                        cur = conn.execute(sql)
                        rows = cur.fetchall() if cur.description else []
                        return "\n".join("|".join(str(v) for v in row) for row in rows)
                    apply(run, out=lambda _line: None)
            except BaseException:
                admin.execute(f"DROP DATABASE IF EXISTS {TEMPLATE_DB} WITH (FORCE)")
                raise
        finally:
            admin.execute("SELECT pg_advisory_unlock(4210012)")


def _which(name: str) -> Optional[str]:
    from hermes_platform.resolver import locate_command
    command = locate_command(name).command
    return command[0] if command else None


def _free_port() -> int:
    import socket
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def _run_or_explain(argv: list[str], root: Path) -> None:
    """Run an initdb/pg_ctl step; on failure, raise with its output (and the server log) instead of a bare exit code."""
    done = subprocess.run(argv, capture_output=True, text=True, timeout=120, check=False)
    if done.returncode != 0:
        log = root / "log"
        detail = (done.stdout + done.stderr + (log.read_text(errors="replace") if log.exists() else "")).strip()
        shutil.rmtree(root, ignore_errors=True)
        raise ClusterUnavailable(f"{Path(argv[0]).name} failed ({done.returncode}): {detail[-600:]}")


def _start_local() -> Optional[Cluster]:
    initdb, pg_ctl, pg_config = _which("initdb"), _which("pg_ctl"), _which("pg_config")
    if not (initdb and pg_ctl and pg_config):
        return None
    sharedir = subprocess.run([pg_config, "--sharedir"], capture_output=True, text=True, timeout=30, check=False).stdout.strip()
    if not (Path(sharedir) / "extension" / "vector.control").exists():
        return None
    root = Path(tempfile.mkdtemp(prefix="lina-test-pg-"))
    data = root / "data"
    _run_or_explain([initdb, "-D", str(data), "-U", "lina", "--auth=trust", "-E", "UTF8", "--no-instructions"], root)
    port = _free_port()
    options = " ".join([f"-p {port}", "-c listen_addresses=127.0.0.1", f"-k {root}", *(" ".join(_FAST[i:i + 2])
                                                                                      for i in range(0, len(_FAST), 2))])
    _run_or_explain([pg_ctl, "-D", str(data), "-o", options, "-l", str(root / "log"), "-w", "start"], root)

    def stop() -> None:
        subprocess.run([pg_ctl, "-D", str(data), "-m", "immediate", "stop"], capture_output=True, timeout=60, check=False)
        shutil.rmtree(root, ignore_errors=True)
    return Cluster(f"host=127.0.0.1 port={port} user=lina dbname=postgres", f"initdb ({initdb})", stop)


def _start_docker() -> Optional[Cluster]:
    docker = _which("docker")
    if not docker:
        return None
    if subprocess.run([docker, "info"], capture_output=True, timeout=60, check=False).returncode != 0:
        return None
    password = uuid.uuid4().hex
    name = f"lina-test-pg-{uuid.uuid4().hex[:12]}"
    port = _free_port()
    # Host networking: hundreds of test processes connecting at once overran docker's userland port proxy
    # ("server closed the connection unexpectedly" with nothing in the server log).
    started = subprocess.run(
        [docker, "run", "-d", "--rm", "--name", name, "--network", "host", "--tmpfs", "/var/lib/postgresql/data",
         "-e", "POSTGRES_USER=lina", "-e", f"POSTGRES_PASSWORD={password}", "-e", "POSTGRES_DB=postgres", IMAGE,
         "postgres", "-c", f"port={port}", "-c", "listen_addresses=127.0.0.1", *_FAST],
        capture_output=True, text=True, timeout=600, check=False)
    if started.returncode != 0:
        raise ClusterUnavailable(f"docker run {IMAGE} failed: {started.stderr.strip()[-300:]}")

    def stop() -> None:
        subprocess.run([docker, "rm", "-f", name], capture_output=True, timeout=60, check=False)
    try:
        dsn = f"host=127.0.0.1 port={port} user=lina password={password} dbname=postgres"
        _wait_ready(dsn)
    except BaseException:
        stop()
        raise
    return Cluster(dsn, f"docker ({name})", stop)


def start_cluster() -> Cluster:
    """A bootstrapped cluster, from the first source that has one (see the module docstring)."""
    given = os.environ.get(CLUSTER_ENV, "").strip()
    if given:
        cluster = Cluster(given, CLUSTER_ENV)
    else:
        cluster = _start_local() or _start_docker()
        if cluster is None:
            raise ClusterUnavailable("no Postgres for the memory store tests: set LINA_TEST_PG_DSN, install "
                                     "Postgres with pgvector (initdb on PATH), or start Docker")
    try:
        bootstrap(cluster.dsn)
    except BaseException:
        cluster.stop()
        raise
    return cluster


class ProcessStore:
    """This test process's database: created on first use, emptied after each test, dropped at exit."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._cluster: Optional[Cluster] = None
        self._dbname: Optional[str] = None
        self.unavailable: Optional[str] = os.environ.get(UNAVAILABLE_ENV) or None
        self._tables: Optional[list[str]] = None
        self.refused_this_test = False
        self.kept_for_module: Optional[str] = None

    def app_dsn(self) -> str:
        from hermes_state_store import StoreUnavailableError
        with self._lock:
            if self._dbname is None:
                if self.unavailable:
                    self.refused_this_test = True
                    raise StoreUnavailableError(self.unavailable)
                try:
                    self._cluster = start_cluster()
                    dbname = f"lina_t_{os.getpid()}_{uuid.uuid4().hex[:8]}"
                    with _connect(_with_db(self._cluster.dsn, "postgres")) as admin:
                        admin.execute(f"CREATE DATABASE {dbname} TEMPLATE {TEMPLATE_DB}")
                    self._dbname = dbname
                except Exception as exc:
                    self.unavailable = str(exc) if isinstance(exc, ClusterUnavailable) else (
                        f"the test Postgres could not be set up: {type(exc).__name__}: {exc}")
                    if self._cluster is not None:
                        self._cluster.stop()
                        self._cluster = None
                    self.refused_this_test = True
                    raise StoreUnavailableError(self.unavailable) from exc
                os.environ["LINA_STORE_DSN"] = _as_app(self._cluster.dsn, self._dbname)
            return _as_app(self._cluster.dsn, self._dbname)

    @property
    def started(self) -> bool:
        return self._dbname is not None

    def superuser_dsn(self) -> str:
        self.app_dsn()
        return _with_db(self._cluster.dsn, self._dbname)

    def reset(self) -> None:
        if self._dbname is None:
            return
        with _connect(self.superuser_dsn()) as conn:
            if self._tables is None:
                self._tables = [row[0] for row in conn.execute(
                    "SELECT format('%I.%I', schemaname, tablename) FROM pg_tables "
                    "WHERE schemaname IN ('memory', 'ctx', 'vec') AND tablename <> 'schema_migrations'")]
            if self._tables:
                conn.execute(f"TRUNCATE {', '.join(self._tables)} RESTART IDENTITY")

    def close(self) -> None:
        if self._dbname is None or self._cluster is None:
            return
        dbname, self._dbname = self._dbname, None
        try:
            with _connect(_with_db(self._cluster.dsn, "postgres")) as admin:
                admin.execute(f"DROP DATABASE IF EXISTS {dbname} WITH (FORCE)")
        finally:
            if self._cluster.how != CLUSTER_ENV:
                self._cluster.stop()


PROCESS_STORE = ProcessStore()


def _carry_store_dsn_into_children() -> None:
    """Many tests start a gateway, worker or probe with an environment built from an allowlist (PATH, LANG,
    HERMES_HOME...). A real install's child finds the store through ``store/store.env`` under its LINA root; a
    test's child has no such file, so the test store's DSN rides along in every child environment instead."""
    original = subprocess.Popen.__init__

    def __init__(self, *args, **kwargs):
        env = kwargs.get("env")
        dsn = os.environ.get("LINA_STORE_DSN")
        if env is not None and dsn and "LINA_STORE_DSN" not in env:
            kwargs["env"] = {**env, "LINA_STORE_DSN": dsn}
        original(self, *args, **kwargs)

    subprocess.Popen.__init__ = __init__


_carry_store_dsn_into_children()


def in_ci() -> bool:
    return os.environ.get("CI", "").strip().lower() in {"1", "true", "yes"}


PER_MODULE_MARK = "memory_store_per_module"


@pytest.fixture(autouse=True)
def _memory_store(_hermetic_environment, monkeypatch, request):
    """Point the message store at this process's test database, empty after the test (after the module,
    for a module marked ``memory_store_per_module`` whose module-scoped fixtures keep state across tests)."""
    import hermes_state_store
    module = request.node.module.__name__ if request.node.module else ""
    if PROCESS_STORE.kept_for_module and PROCESS_STORE.kept_for_module != module:
        PROCESS_STORE.kept_for_module = None
        hermes_state_store.TEXT_CACHE.clear()
        PROCESS_STORE.reset()
    PROCESS_STORE.refused_this_test = False
    monkeypatch.setattr(hermes_state_store, "resolve_dsn", PROCESS_STORE.app_dsn)
    monkeypatch.setattr(hermes_state_store, "resolve_owner_dsn", PROCESS_STORE.superuser_dsn)
    if not PROCESS_STORE.started and os.environ.get(CLUSTER_ENV) and not PROCESS_STORE.unavailable:
        # A run's cluster is already up: make the database now, so a child process the test starts before
        # its own first store use (a gateway daemon, a worker) inherits LINA_STORE_DSN.
        try:
            PROCESS_STORE.app_dsn()
        except hermes_state_store.StoreUnavailableError:
            pass  # recorded in PROCESS_STORE.unavailable; the test's own store use reports it
    if PROCESS_STORE.started:
        monkeypatch.setenv("LINA_STORE_DSN", PROCESS_STORE.app_dsn())
    else:
        monkeypatch.delenv("LINA_STORE_DSN", raising=False)
    yield PROCESS_STORE
    if request.node.get_closest_marker(PER_MODULE_MARK) is not None:
        PROCESS_STORE.kept_for_module = module
        return
    hermes_state_store.TEXT_CACHE.clear()
    PROCESS_STORE.reset()


@pytest.fixture
def memory_store_sql():
    """Run SQL against this test's ``memory`` schema as the superuser (row-level security does not apply)."""
    def run(sql: str, params=()):
        with _connect(PROCESS_STORE.superuser_dsn()) as conn:
            cur = conn.execute(sql, params)
            return cur.fetchall() if cur.description else []
    return run


def skip_if_store_unavailable(item, report) -> None:
    """Turn a failure caused by a missing test Postgres into a skip with one reason (never under CI)."""
    if report.when == "teardown" or not report.failed or in_ci() or not PROCESS_STORE.refused_this_test:
        return
    report.outcome = "skipped"
    report.longrepr = (str(item.path), item.location[1] or 0,
                       f"Skipped: memory store tests need Postgres: {PROCESS_STORE.unavailable}")


if __name__ == "__main__":
    import sys

    # Standalone suites (LCM-X) do not run through run_tests_parallel.py. Give their
    # command the same cluster and keep its lifetime scoped to that command.
    if len(sys.argv) < 2:
        raise SystemExit("usage: memory_store.py COMMAND [ARG ...]")
    cluster = start_cluster()
    try:
        completed = subprocess.run(
            sys.argv[1:], env={**os.environ, CLUSTER_ENV: cluster.dsn}, check=False,
        )
    finally:
        cluster.stop()
    raise SystemExit(completed.returncode)
