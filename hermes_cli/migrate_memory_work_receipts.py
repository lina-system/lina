"""Store-owned profile mappings and verified foreign-ancestor promotion receipts."""

import json

from hermes_cli.migrate_memory_work_rows import KINDS, canonical, key, remap_owner, rows
from hermes_cli.work import store

RECEIPT_KEY = "memory_migration_receipts"


def load(db, mappings: dict[str, str]) -> dict:
    if db.dialect == "postgres":
        db.execute("LOCK TABLE setting, " + ", ".join(KINDS) + " IN EXCLUSIVE MODE")
    saved = db.execute("SELECT value FROM setting WHERE key=?", (RECEIPT_KEY,)).fetchone()
    receipt = json.loads(saved[0]) if saved else {"profiles": {}, "ancestors": {}}
    for profile, agent in mappings.items():
        if profile in receipt["profiles"] and receipt["profiles"][profile] != agent:
            raise ValueError(f"source profile already mapped to another agent: {profile}")
        receipt["profiles"][profile] = agent
    return receipt


def save(db, receipt: dict) -> None:
    db.execute("INSERT INTO setting(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
               (RECEIPT_KEY, json.dumps(receipt, sort_keys=True)))


def promote(db, source: dict, receipt: dict) -> list[dict]:
    """Only a recorded unchanged import can change owner, within the caller transaction."""
    actual = {kind: {key(kind, row): row for row in records} for kind, records in rows(db).items()}
    originals = {kind: {key(kind, row): row for row in records} for kind, records in source.items()}
    promoted = []
    for token, record in list(receipt["ancestors"].items()):
        kind, identity, owner = record["kind"], tuple(record["key"]), record["owner"]
        if owner not in receipt["profiles"]:
            continue
        row = originals[kind].get(identity)
        current = actual[kind].get(identity)
        if row is None or current is None or canonical(db, kind, row) != record["source"] or canonical(db, kind, current) != record["imported"]:
            raise ValueError(f"work ancestor collision: {kind}/{identity}")
        mapped = remap_owner(kind, row, receipt["profiles"])
        store.actor(db, mapped["agent_id"])
        columns = [column for column in ("agent_id", "data") if column in mapped]
        db.execute(f"UPDATE {kind} SET " + ", ".join(f"{column}=?" for column in columns) + " WHERE id=?",
                   [mapped[column] for column in columns] + [row["id"]])
        promoted.append({"kind": kind, "id": row["id"], "profile": owner, "agent": mapped["agent_id"]})
        del receipt["ancestors"][token]
    return promoted


def record_ancestors(db, source: dict, selected: dict, matched: dict, receipt: dict) -> None:
    for kind, records in selected.items():
        originals = {key(kind, row): row for row in source[kind]}
        for row in records:
            identity = key(kind, row)
            original = originals[identity]
            owner = original.get("agent_id")
            if owner is not None and owner not in receipt["profiles"] and identity not in matched[kind]:
                token = json.dumps([kind, identity])
                receipt["ancestors"][token] = {"kind": kind, "key": list(identity), "owner": owner,
                                                "source": canonical(db, kind, original),
                                                "imported": canonical(db, kind, row)}
