"""A disposable Postgres session: explicit test DSN, local cluster, then Docker."""

from __future__ import annotations

import os
import shutil
import subprocess
from collections.abc import Iterator
from pathlib import Path

import pytest

from hermes_cli.work.store_pg import PgDb, PgError, connect
from tests.waiting import wait_until


def command(argv: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(argv, capture_output=True, text=True, timeout=120, check=True)


def local_cluster(root: Path) -> tuple[list[str], list[str]]:
    data, sockets = root / "data", root / "socket"
    sockets.mkdir()
    command(["initdb", "-D", str(data), "-A", "trust", "-U", "lina"])
    stop = ["pg_ctl", "-D", str(data), "-m", "immediate", "-w", "stop"]
    try:
        command(["pg_ctl", "-D", str(data), "-l", str(root / "postgres.log"),
                 "-o", f"-k {sockets} -h ''", "-w", "start"])
        return ["psql", "-h", str(sockets), "-U", "lina", "-d", "postgres"], stop
    except (OSError, subprocess.SubprocessError):
        subprocess.run(stop, capture_output=True, timeout=30, check=False)
        raise


def docker_cluster() -> tuple[list[str], list[str]]:
    init = Path(__file__).resolve().parents[2] / "services/store/initdb"
    ctr = command(["docker", "run", "--rm", "-d", "-v", f"{init}:/docker-entrypoint-initdb.d:ro",
                   "-e", "POSTGRES_USER=lina", "-e", "POSTGRES_DB=lina",
                   "-e", "POSTGRES_PASSWORD=test", "-e", "LINA_APP_PASSWORD=test",
                   "-e", "HONCHO_DB_PASSWORD=test", "pgvector/pgvector:0.8.7-pg15-bookworm"]).stdout.strip()
    stop = ["docker", "rm", "-f", ctr]
    print(f"Postgres fixture Docker container: {ctr}")
    try:
        argv = ["docker", "exec", "-i", ctr, "psql", "-U", "lina", "-d", "lina"]
        # A TCP probe ignores the entrypoint's temporary socket-only server.
        wait_until(lambda: subprocess.run(
            ["docker", "exec", ctr, "pg_isready", "-h", "127.0.0.1", "-U", "lina", "-d", "lina"],
            capture_output=True, timeout=10, check=False).returncode == 0,
            timeout=90, what="Postgres completed initdb")
        return argv, stop
    except (OSError, subprocess.SubprocessError, AssertionError):
        command(stop)
        raise


@pytest.fixture(scope="session")
def pg_db(tmp_path_factory: pytest.TempPathFactory) -> Iterator[PgDb]:
    dsn = os.environ.get("LINA_TEST_PG_DSN")
    argv, stop, failures = [], [], []
    if dsn:
        argv = ["psql", dsn]
    else:
        if all(shutil.which(exe) for exe in ("initdb", "pg_ctl", "psql")):
            try:
                argv, stop = local_cluster(tmp_path_factory.mktemp("pg-cluster"))
            except (OSError, subprocess.SubprocessError) as exc:
                failures.append(str(exc))
        if not argv and shutil.which("docker"):
            try:
                argv, stop = docker_cluster()
            except (OSError, subprocess.SubprocessError, AssertionError) as exc:
                failures.append(str(exc))
    if not argv:
        reason = "Postgres unavailable: " + ("; ".join(failures) or "no test DSN, initdb/pg_ctl, or Docker")
        if os.environ.get("CI", "").lower() == "true":
            pytest.fail(reason)
        pytest.skip(reason)
    db = None
    try:
        db = connect(argv, "owner", {}, ensure_schema=False)
        if stop and stop[0] == "docker":
            assert db.execute("SELECT to_regprocedure('public.lina_apply_agent_rls(regclass)') IS NOT NULL").fetchone() == (True,)
        yield db
    except PgError as exc:
        pytest.fail(str(exc))
    finally:
        if db is not None:
            db.close()
        if stop:
            command(stop)
            print(f"Postgres fixture removed: {stop[-1]}")
