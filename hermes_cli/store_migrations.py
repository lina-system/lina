"""LINA's numbered schema migrations for ``memory``, ``ctx`` and ``vec`` (lina#12).

Honcho migrates its own schema ``ltm``; everything else in the store is LINA's. Each
``services/store/migrations/NNNN_<name>.sql`` runs once, in its own transaction, and is recorded in
``memory.schema_migrations``; running them again applies only the files not yet recorded.
"""

from __future__ import annotations

import re
from collections.abc import Callable
from importlib.resources import files
from pathlib import Path

# Package data of services.store, so an installed wheel finds them too (lina#145).
MIGRATIONS_DIR = Path(files("services.store") / "migrations")
_NAME = re.compile(r"^(\d{4})_[a-z0-9_]+\.sql$")

LEDGER_SQL = """CREATE TABLE IF NOT EXISTS memory.schema_migrations (
    version text PRIMARY KEY,
    name text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
);"""


def migration_files(directory: Path = MIGRATIONS_DIR) -> list[Path]:
    files = sorted(p for p in directory.glob("*.sql") if _NAME.match(p.name))
    versions = [_NAME.match(p.name).group(1) for p in files]
    if len(set(versions)) != len(versions):
        raise ValueError(f"two migrations in {directory} share a number")
    return files


def _literal(text: str) -> str:
    return "'" + text.replace("'", "''") + "'"


def migration_script(path: Path) -> str:
    """One transaction: the file's statements and its ledger row, so a failed file records nothing."""
    version = _NAME.match(path.name).group(1)
    body = path.read_text(encoding="utf-8-sig")
    return (f"BEGIN;\n{body.rstrip()}\n"
            f"INSERT INTO memory.schema_migrations (version, name) VALUES ({_literal(version)}, {_literal(path.name)});\n"
            "COMMIT;\n")


def apply(run_sql: Callable[[str], str], *, out: Callable[[str], None] = print,
          directory: Path = MIGRATIONS_DIR) -> list[str]:
    """Apply the pending migrations through *run_sql* (runs a script as the superuser, returns its output).
    Returns the file names applied now."""
    run_sql(LEDGER_SQL)
    done = set(run_sql("SELECT version FROM memory.schema_migrations;").split())
    applied = []
    for path in migration_files(directory):
        if _NAME.match(path.name).group(1) in done:
            continue
        run_sql(migration_script(path))
        applied.append(path.name)
        out(f"Applied store migration {path.name}.")
    return applied
