"""Inventory a copied work ledger without claiming an unsafe identity import."""

from __future__ import annotations

import hashlib
from collections import Counter
from contextlib import closing
from pathlib import Path

from hermes_cli.work import env, store
from hermes_cli.work.cmd_store import RUN_COLUMNS, Source, latest


def migrate_work(copied_db: Path, destination_home: Path, profile: str, agent: str) -> dict:
    """Report the importer gap; never open a live ledger or write either home.

    Ownership follows the latest item's ``by`` (the projection's ``agent_id``),
    not its writer/actor: those identify collaborators, not the owning profile.
    Ancestry is inventoried separately, never copied with another profile's body.
    """
    # Source needs actor display metadata even for read-only SQLite access.
    # Supply it explicitly rather than letting current() discover a live home.
    previous = env._CURRENT[:]
    env.bind(env.Env(destination_home, {}, "en", "owner", "owner", {}))
    try:
        with closing(Source(copied_db)) as source:
            lines = source.lines()
            runs, pairs = source.runs()
            counts = source.counts()
    finally:
        env._CURRENT[:] = previous
    records, order = latest(row for _, _, row in lines if row is not None)
    selected = {i for i, row in records.items() if row.get("by") == profile}
    ancestors = set()
    missing = set()
    pending = list(selected)
    visited = set(selected)
    while pending:
        parent = records[pending.pop()].get("parent")
        if not parent or parent in visited:
            continue
        visited.add(parent)
        if parent not in records:
            missing.add(parent)
            continue
        ancestors.add(parent)
        pending.append(parent)
    selected_lines = [(n, text, row) for n, text, row in lines
                      if row is not None and row.get("id") in selected and row.get("by") == profile]
    run_ids = {run[0] for run in runs if run[RUN_COLUMNS.index("agent_id")] == profile}
    kinds = Counter(store.table_of(records[i]) or "unknown" for i in selected)
    kinds.update(run=len(run_ids), run_todo=sum(1 for pair in pairs
                                             if pair[0] in run_ids and pair[1] in selected),
                 event=len(selected_lines))
    manifest = [{"native_id": str(n), "item_id": row["id"],
                 "kind": store.table_of(row) or "unknown",
                 "sha256": hashlib.sha256(text.encode("utf-8")).hexdigest()}
                for n, text, row in selected_lines]
    # _import_line -> project_item -> upsert conflicts on id alone, not
    # (agent_id, id). _imported also keys receipts by source line alone.
    # Do not invent a second writer or silently overwrite an original import.
    return {
        "source": "work", "copied_path": str(copied_db),
        "destination_home": str(destination_home), "profile": profile, "agent": agent,
        "status": "gap", "ok": False, "inserted": 0, "matched": 0,
        "gap": {
            "code": "work_import_identity_namespace",
            "reason": "The existing importer has global item/run IDs and source-line receipts, "
                      "with no identity-remap namespace. Changing agent_id leaves item IDs unchanged; "
                      "_import_line/project_item/upsert overwrites an original import on id. "
                      "_copy_runs keeps the original run on id conflict. Profile subsets also cannot "
                      "use _check_counts, which compares whole-source and whole-destination counts.",
        },
        "source_counts": counts, "selected_counts": dict(kinds),
        "source_rows": len(lines), "selected_rows": len(selected_lines),
        "excluded_rows": len(lines) - len(selected_lines),
        "invalid_rows": sum(row is None for _, _, row in lines),
        "selected_ids": [i for i in order if i in selected],
        "required_ancestors": [{"id": i, "kind": store.table_of(records[i]),
                                "profile": records[i].get("by")}
                               for i in order if i in ancestors],
        "missing_ancestors": sorted(missing),
        "excluded_items": [{"id": i, "kind": store.table_of(records[i]),
                            "reason": "other_profile", "required_ancestor": i in ancestors}
                           for i in order if i not in selected],
        "excluded_run_ids": [run[0] for run in runs if run[0] not in run_ids],
        "source_manifest": manifest, "destination_manifest": [],
        "migrated_counts": {kind: 0 for kind in kinds},
    }
