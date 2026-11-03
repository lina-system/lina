"""Disposable database restoration and real Honcho Alembic execution.

The subprocess is intentional: Honcho has its own dependency environment and
import-time schema settings. Neither its settings nor its imports enter Hermes.
"""

from __future__ import annotations

from collections.abc import Iterator, Sequence
from contextlib import contextmanager
import json
import os
from pathlib import Path
import subprocess
import sys
import uuid

import psycopg
from psycopg import sql
from psycopg.conninfo import conninfo_to_dict, make_conninfo

ROOT = Path(__file__).resolve().parents[1]


class HonchoMigrationError(RuntimeError):
    """A safe CLI-facing subprocess failure; never includes argv or stderr."""

    def __init__(self, operation: str, returncode: int | None = None) -> None:
        self.operation = operation
        self.returncode = returncode
        suffix = f" (exit {returncode})" if returncode is not None else " (timed out)"
        super().__init__(f"Honcho {operation} failed{suffix}")


def database_dsn(dsn: str, database: str) -> str:
    return make_conninfo(**{**conninfo_to_dict(dsn), "dbname": database})


def child_environment(work_dir: Path, schema: str) -> dict[str, str]:
    """Keep subprocess caches, settings and bytecode inside the work directory."""
    home = work_dir / "home"
    home.mkdir(parents=True, exist_ok=True)
    env = {key: value for key, value in os.environ.items()
           if not key.startswith(("DB_", "EMBEDDING_", "LLM_"))}
    env.update(HOME=str(home), TMPDIR=str(work_dir), TMP=str(work_dir), TEMP=str(work_dir),
               XDG_CACHE_HOME=str(work_dir / "cache"), XDG_CONFIG_HOME=str(home),
               PYTHONDONTWRITEBYTECODE="1", DB_SCHEMA=schema,
               PYTHONPATH=os.pathsep.join((str(ROOT), str(ROOT / "services/honcho"))),
               EMBEDDING_VECTOR_DIMENSIONS="1024")
    return env


def upgrade(dsn: str, schema: str, revision: str, python: Path, work_dir: Path) -> None:
    """Run the checked-in chain on an explicitly supplied connection, never stamp."""
    try:
        completed = subprocess.run(
            [str(python), "-m", "hermes_cli.migrate_memory_honcho_stage"],
            input=json.dumps({"dsn": dsn, "revision": revision}), text=True, encoding="utf-8", errors="replace",
            env=child_environment(work_dir, schema), cwd=work_dir,
            capture_output=True, check=False, timeout=600,
        )
    except subprocess.TimeoutExpired:
        raise HonchoMigrationError("Alembic upgrade") from None
    if completed.returncode:
        raise HonchoMigrationError("Alembic upgrade", completed.returncode)


@contextmanager
def restored_database(copied_dump: Path, dsn: str, work_dir: Path,
                      restore_command: Sequence[str], *, container_local: bool = False) -> Iterator[str]:
    """Restore a custom/tar pg_dump archive into a newly owned database only."""
    database = "honcho_import_" + uuid.uuid4().hex
    stage_dsn = database_dsn(dsn, database)
    restore_dsn = stage_dsn
    if container_local:
        restore_dsn = make_conninfo(**{**conninfo_to_dict(stage_dsn), "host": "127.0.0.1", "port": "5432"})
    with psycopg.connect(database_dsn(dsn, "postgres"), autocommit=True) as admin:
        admin.execute(sql.SQL("CREATE DATABASE {} TEMPLATE template0").format(sql.Identifier(database)))
        try:
            with copied_dump.open("rb") as archive:
                try:
                    completed = subprocess.run(
                        [*restore_command, "--no-owner", "--no-privileges", "--exit-on-error",
                         "--dbname", restore_dsn], stdin=archive, capture_output=True,
                        env=child_environment(work_dir, "public"), cwd=work_dir,
                        check=False, timeout=600,
                    )
                except subprocess.TimeoutExpired:
                    raise HonchoMigrationError("archive restore") from None
                if completed.returncode:
                    raise HonchoMigrationError("archive restore", completed.returncode)
            yield stage_dsn
        finally:
            admin.execute(sql.SQL("DROP DATABASE {} WITH (FORCE)").format(sql.Identifier(database)))


def _main() -> None:
    import sqlalchemy as sa
    from alembic import command
    from alembic.config import Config

    request = json.load(sys.stdin)
    engine = sa.create_engine("postgresql+psycopg://", creator=lambda: psycopg.connect(request["dsn"]))
    config = Config(str(ROOT / "services/honcho/alembic.ini"))
    config.set_main_option("script_location", str(ROOT / "services/honcho/migrations"))
    try:
        with engine.begin() as connection:
            config.attributes["connection"] = connection
            command.upgrade(config, request["revision"])
    finally:
        engine.dispose()


if __name__ == "__main__":
    _main()
