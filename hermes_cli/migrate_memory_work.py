"""Import a copied profile work graph through the configured work store."""

from __future__ import annotations

from contextlib import closing
from pathlib import Path

from hermes_cli.migrate_memory_work_rows import KINDS, key, manifest, preflight, rows, select_graph
from hermes_cli.work import env, ledger, store
from hermes_cli.work.cmd_store import Source, import_selected_rows


def _destination_db():
    """Open the destination backend without ledger.open_db's preflight seed writes."""
    current = env.current()
    if ledger.backend() == "postgres":
        from hermes_cli.work import store_pg
        return store_pg.connect(ledger._psql_argv(), current.owner, current.names, ensure_schema=False)
    from hermes_cli.work import store_sqlite
    return store_sqlite.connect(current.root / "work.db", current.owner, current.names, create=True)


def migrate_work(copied_db: Path, destination_home: Path, profile: str, agent: str) -> dict:
    """Import a selected graph atomically, refusing different-content ID collisions."""
    previous = env._CURRENT[:]
    env.bind(env.load(destination_home))
    try:
        with closing(Source(copied_db)) as source:
            if source.db is None:
                raise ValueError("work migration requires a copied SQLite store")
            all_rows = rows(source.db)
            source_counts = source.counts()
        selected, ancestors, owned = select_graph(all_rows, profile, agent)
        with closing(_destination_db()) as db:
            with store.transaction(db):
                if db.dialect == "postgres":
                    db.execute("LOCK TABLE " + ", ".join(KINDS) + " IN EXCLUSIVE MODE")
                matched = preflight(db, selected)
                expected = manifest(db, selected)
                import_selected_rows(db, {kind: [row for row in selected[kind]
                                                if key(kind, row) not in matched[kind]] for kind in KINDS})
                actual_rows = rows(db)
                actual = {kind: [row for row in actual_rows[kind]
                                 if key(kind, row) in {key(kind, item) for item in selected[kind]}]
                          for kind in KINDS}
                observed = manifest(db, actual)
                if expected != observed:
                    raise ValueError("work import verification mismatch")
        counts = {kind: len(selected[kind]) for kind in KINDS}
        matched_counts = {kind: len(matched[kind]) for kind in KINDS}
        inserted_counts = {kind: counts[kind] - matched_counts[kind] for kind in KINDS}
        return {"source": "work", "copied_path": str(copied_db), "destination_home": str(destination_home),
                "profile": profile, "agent": agent, "status": "imported", "ok": True,
                "source_counts": counts, "whole_source_counts": source_counts, "selected_counts": counts, "migrated_counts": counts,
                "matched_counts": matched_counts, "inserted_counts": inserted_counts,
                "matched": sum(matched_counts.values()), "inserted": sum(inserted_counts.values()),
                "selected_ids": sorted(set().union(*(owned[k] for k in ("area", "goal", "todo")))),
                "required_ancestors": ancestors, "missing_ancestors": [],
                "source_rows": sum(counts.values()), "selected_rows": sum(counts.values()),
                "selected_event_rows": counts["event"],
                "whole_source_rows": sum(len(records) for records in all_rows.values()),
                "excluded_rows": sum(len(all_rows[kind]) - counts[kind] for kind in KINDS),
                "excluded_event_rows": len(all_rows["event"]) - counts["event"], "invalid_rows": 0,
                "excluded_run_ids": [row["id"] for row in all_rows["run"]
                                     if row["id"] not in {r["id"] for r in selected["run"]}],
                "excluded_items": [{"id": row["id"], "kind": kind, "reason": "other_profile"}
                                   for kind in ("area", "project", "goal", "todo") for row in all_rows[kind]
                                   if row["id"] not in {r["id"] for r in selected[kind]}],
                "source_manifest": expected, "destination_manifest": observed}
    finally:
        env._CURRENT[:] = previous
