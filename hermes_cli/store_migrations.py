"""Numbered store migrations, shared by psql connections and Store.psql."""

from collections.abc import Callable
from pathlib import Path
from typing import Final

MIGRATIONS: Final = Path(__file__).resolve().parents[1] / "services/store/migrations"
RunSql = Callable[[str], str]


def pending(run_sql: RunSql) -> list[tuple[str, Path]]:
    """Create the migration ledger and return unapplied files in version order."""
    run_sql("CREATE SCHEMA IF NOT EXISTS memory; CREATE TABLE IF NOT EXISTS memory.schema_migrations ("
            "version text PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now());")
    applied = set(run_sql("SELECT version FROM memory.schema_migrations ORDER BY version;").splitlines())
    return [(p.name.split("_", 1)[0], p) for p in sorted(MIGRATIONS.glob("[0-9][0-9][0-9][0-9]_*.sql"))
            if p.name.split("_", 1)[0] not in applied]


def apply(run_sql: RunSql) -> list[str]:
    """Apply each pending file and its ledger entry in one transaction."""
    applied = []
    for version, path in pending(run_sql):
        name = path.name.replace("'", "''")
        run_sql(f"BEGIN;\n{path.read_text(encoding='utf-8-sig')}\n"
                f"INSERT INTO memory.schema_migrations (version, name) VALUES ('{version}', '{name}');\nCOMMIT;")
        applied.append(version)
    return applied
