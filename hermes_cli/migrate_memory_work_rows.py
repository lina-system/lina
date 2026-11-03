"""Select and compare a copied work graph without changing either store."""

from __future__ import annotations

import hashlib
import json

KINDS = ("area", "project", "goal", "todo", "run", "run_todo", "wait", "decision", "link", "event")
REFERENCES = {
    "area": (), "project": (("area_id", "area"),),
    "goal": (("area_id", "area"), ("project_id", "project")),
    "todo": (("goal_id", "goal"), ("close_run_id", "run")),
    "run": (("goal_id", "goal"),),
    "run_todo": (("run_id", "run"), ("todo_id", "todo")),
    "wait": (("goal_id", "goal"), ("todo_id", "todo"), ("run_id", "run")),
    "decision": (("goal_id", "goal"), ("todo_id", "todo"), ("run_id", "run")),
    "link": (("goal_id", "goal"), ("todo_id", "todo"), ("run_id", "run")),
    "event": (("area_id", "area"), ("goal_id", "goal"), ("todo_id", "todo"),
              ("run_id", "run"), ("parent_id", "event")),
}


def rows(db) -> dict:
    return {kind: [dict(zip(db.columns(kind), row)) for row in db.execute(
        f"SELECT * FROM {kind} ORDER BY " + ("run_id, todo_id" if kind == "run_todo" else "id")).fetchall()]
            for kind in KINDS}


def key(kind, row) -> tuple:
    return (row["run_id"], row["todo_id"]) if kind == "run_todo" else (row["id"],)


def select_graph(all_rows, profile, agent) -> tuple:
    indexes = {kind: {row["id"]: row for row in all_rows[kind]} for kind in KINDS if kind != "run_todo"}
    chosen = {kind: {row["id"] for row in all_rows[kind] if row["agent_id"] == profile}
              for kind in ("area", "project", "goal", "todo", "run")}
    owned = {kind: ids.copy() for kind, ids in chosen.items()}
    for kind in ("run_todo", "wait", "decision", "link"):
        chosen[kind] = {key(kind, row) if kind == "run_todo" else row["id"] for row in all_rows[kind]
                        if row.get("agent_id") == profile or
                        any(row.get(parent + "_id") in ids for parent, ids in owned.items())}
    chosen["event"] = {row["id"] for row in all_rows["event"] if row["agent_id"] == profile}
    event_seeds = chosen["event"].copy()
    event_parents = set()
    history = {}
    for row in all_rows["event"]:
        for column, parent in REFERENCES["event"][:-1]:
            if row[column] is not None:
                history.setdefault((parent, row[column]), []).append(row["id"])
    indexes["run_todo"] = {key("run_todo", row): row for row in all_rows["run_todo"]}
    pending = [(kind, i) for kind, ids in chosen.items() for i in ids]
    while pending:
        kind, i = pending.pop()
        for column, parent in REFERENCES[kind]:
            parent_id = indexes[kind][i].get(column)
            if parent == "event" and parent_id is not None:
                event_parents.add(parent_id)
            if parent_id is not None and parent_id not in chosen[parent]:
                if parent_id not in indexes[parent]:
                    raise ValueError(f"missing work ancestor: {parent}/{parent_id}")
                chosen[parent].add(parent_id)
                pending.append((parent, parent_id))
        for event_id in history.get((kind, i), ()):
            if event_id not in chosen["event"]:
                chosen["event"].add(event_id)
                pending.append(("event", event_id))
    selected = {}
    for kind in KINDS:
        selected[kind] = [dict(row) for row in all_rows[kind]
                          if (key(kind, row) if kind == "run_todo" else row["id"]) in chosen[kind]]
        for row in selected[kind]:
            owning_selected = (row["id"] in owned[kind] if kind in owned else
                               any(row.get(parent + "_id") in ids for parent, ids in owned.items()))
            if row.get("agent_id") == profile:
                row["agent_id"] = agent
            if kind == "event":
                data = json.loads(row["data"])
                if "ledger" in data:
                    record = json.loads(data["ledger"])
                    if owning_selected and record.get("by") == profile:
                        record["by"] = agent
                        data["ledger"] = json.dumps(record, ensure_ascii=False)
                        row["data"] = json.dumps(data, ensure_ascii=False)
    ancestors = [{"id": i, "kind": kind, "profile": indexes[kind][i]["agent_id"]}
                 for kind in owned for i in sorted(chosen[kind] - owned[kind])]
    ancestors += [{"id": i, "kind": "event", "profile": indexes["event"][i]["agent_id"]}
                  for i in sorted(event_parents - event_seeds)]
    return selected, ancestors, owned


def canonical(db, kind, row) -> dict:
    """Use the destination's own types for cross-backend equality, not string formatting."""
    row = dict(row)
    if db.dialect == "postgres":
        types = db.execute("SELECT column_name, data_type FROM information_schema.columns "
                           "WHERE table_schema = 'work' AND table_name = ? ORDER BY ordinal_position", (kind,)).fetchall()
        casts = {"timestamp with time zone": "timestamptz", "interval": "interval", "boolean": "boolean"}
        columns = [(name, casts[typ]) for name, typ in types if typ in casts and row.get(name) is not None]
        if columns:
            values = db.execute("SELECT " + ", ".join(f"?::{typ}" for _, typ in columns),
                                [row[name] for name, _ in columns]).fetchone()
            row.update(zip((name for name, _ in columns), values))
    for column in ("data", "options"):
        if isinstance(row.get(column), str):
            row[column] = json.loads(row[column])
    if kind == "event" and "ledger" in row.get("data", {}):
        row["data"]["ledger"] = json.loads(row["data"]["ledger"])
    return row


def manifest(db, selected) -> list:
    return [{"kind": kind, "native_id": list(key(kind, row)),
             "sha256": hashlib.sha256(json.dumps(canonical(db, kind, row), sort_keys=True,
                                                  ensure_ascii=False).encode()).hexdigest()}
            for kind in KINDS for row in sorted(selected[kind], key=lambda row: key(kind, row))]


def preflight(db, selected) -> dict:
    """Reject every ID/content or unique-project-path conflict before invoking a writer."""
    existing = rows(db)
    matched = {}
    for kind in KINDS:
        index = {key(kind, row): row for row in existing[kind]}
        matched[kind] = set()
        for row in selected[kind]:
            identity = key(kind, row)
            old = index.get(identity)
            if old is not None:
                if canonical(db, kind, old) != canonical(db, kind, row):
                    raise ValueError(f"work ID collision: {kind}/{identity}")
                matched[kind].add(identity)
            if kind == "project" and any(p["para_path"] == row["para_path"] and p["id"] != row["id"]
                                         for p in existing[kind]):
                raise ValueError(f"work project path collision: {identity}")
    return matched
