"""Copy native LCM rows through the existing PgConnection SQLite-SQL route."""

from contextlib import closing
import hashlib
import json
from pathlib import Path

from hermes_cli.migrate_memory_agent import _readonly
from hermes_cli.migrate_memory_agent_stream import scratch, finish_report
from hermes_cli.migrate_memory_lcm_rows import (
    CACHES, DURABLE, RECEIPT_KEY, VECTOR_META, VECTORS, digest, schema, source_rows, verify_references,
)
from hermes_cli.migrate_memory_lcm_text import conversations, index_host, message_uid
from hermes_cli.migrate_memory_lcm_vectors import transfer_vectors
from hermes_state_store import MessageStore
from plugins.context_engine import lcm_x_store
from plugins.memory.lina.identity import IdentityRegistry


def _insert(conn, table, row):
    columns = list(row)
    result = conn.execute(f'INSERT INTO {table} (' + ",".join(columns) + ") VALUES ("
                          + ",".join("?" for _ in columns) + ")"
                          + ("" if table == "messages" else " RETURNING rowid"), list(row.values()))
    if table == "messages":
        return row["store_id"]
    found = result.fetchone()
    return found[0] if found else result.lastrowid


def _compare(conn, table, rows, mapping, ledger):
    """Read only one bounded destination batch; live rows outside receipts are irrelevant."""
    wanted = [mapping[str(native)] for native, _ in rows]
    selected = conn.execute(f'SELECT rowid AS __key__,* FROM "{table}" WHERE rowid IN ('
                            + ",".join("?" for _ in wanted) + ")", wanted)
    names = [column[0] for column in selected.description]
    actual = {values[0]: dict(zip(names, values)) for values in selected.fetchall()}
    for native, expected in rows:
        current = actual.get(mapping[str(native)])
        if current is None:
            raise ValueError(f"missing imported LCM row: {table}/{native}")
        observed = {name: current[name] for name in expected}
        # PostgreSQL jsonb renders tool calls with different insignificant whitespace.
        for value in (expected, observed):
            if table == "messages" and value.get("tool_calls"):
                value["tool_calls"] = json.loads(value["tool_calls"])
        left, right = digest(expected), digest(observed)
        if left != right:
            raise ValueError(f"changed imported LCM row: {table}/{native}")
        identity = f"{table}:{native}"
        ledger.execute("INSERT INTO source_manifest VALUES (?,?)", (identity, left))
        ledger.execute("INSERT INTO destination_manifest VALUES (?,?)", (identity, right))


def _transfer(conn, source, table, receipt, ledger, text, replay):
    from hermes_cli.migrate_memory_agent_stream import batches

    mapping = receipt["ids"].setdefault(table, {})
    matched_text = inserted_text = count = 0
    for batch in batches(source_rows(source, table)):
        for native, row in batch:
            if row.get("conversation_id"):
                row["conversation_id"] = receipt["conversations"][row["conversation_id"]]
            if table == "metadata" and row["key"] == RECEIPT_KEY:
                raise ValueError("source LCM contains a destination import receipt")
            if not replay:
                if table == "messages":
                    uid, matched = message_uid(row, text, ledger, receipt["sha256"])
                    conn.note_message_uid(uid)
                    matched_text += int(matched)
                    inserted_text += int(not matched)
                mapping[str(native)] = _insert(conn, table, row)
                if table == "messages":
                    with text.write():
                        text.set_conversation([text.base_uid(uid)], row.get("conversation_id") or
                                              receipt["conversations"][row["session_id"]])
            elif str(native) not in mapping:
                raise ValueError(f"LCM receipt misses source row: {table}/{native}")
        _compare(conn, table, batch, mapping, ledger)
        count += len(batch)
    return count, matched_text, inserted_text


def migrate_lcm(copied: Path | None, state: Path, destination: Path, agent: str, work: Path) -> dict:
    if copied is None:
        return {"status": "absent", "counted": True, "ok": True, "source_rows": 0, "inserted": 0, "matched": 0}
    with copied.open("rb") as stream:
        fingerprint = hashlib.file_digest(stream, "sha256").hexdigest()
    target = destination / "lcm.db"
    lcm_x_store.route(target, agent)
    try:
        with closing(_readonly(copied)) as source, closing(scratch()) as ledger, \
                closing(MessageStore(agent)) as text, closing(IdentityRegistry()) as registry, \
                closing(lcm_x_store.backend_for(target)()) as conn:
            tables = schema(source)
            references = verify_references(source, tables)
            identities = conversations(source, registry, agent)
            index_host(state, ledger)
            for side in ("source", "destination"):
                ledger.execute(f"CREATE TABLE {side}_manifest(native_id TEXT,sha256 TEXT)")
            conn.execute("BEGIN IMMEDIATE")
            saved = conn.execute("SELECT value FROM metadata WHERE key=?", (RECEIPT_KEY,)).fetchone()
            replay = saved is not None
            receipt = json.loads(saved[0]) if replay else {"sha256": fingerprint, "agent": agent, "ids": {}}
            if receipt["sha256"] != fingerprint or receipt["agent"] != agent:
                raise ValueError("LCM destination belongs to another copy")
            if replay and receipt["conversations"] != identities:
                raise ValueError("LCM conversation identity differs from receipt")
            receipt["conversations"] = identities
            if not replay and conn.execute("SELECT 1 FROM messages UNION ALL SELECT 1 FROM summary_nodes LIMIT 1").fetchone():
                raise ValueError("LCM destination contains unreceipted rows")
            counts, matched_text, inserted_text = {}, 0, 0
            for table in DURABLE:
                if table not in tables:
                    continue
                if table == "lcm_rollup_invalidations" and not replay:
                    conn.execute("DELETE FROM lcm_rollup_invalidations")
                count, matched, added = _transfer(conn, source, table, receipt, ledger, text, replay)
                counts[table] = {"source": count, "matched": count if replay else 0,
                                 "inserted": 0 if replay else count, "skipped": 0}
                matched_text += matched
                inserted_text += added
            vector_counts, skipped = transfer_vectors(conn, source, tables, receipt, ledger, replay)
            counts.update(vector_counts)
            for table in tables.keys() - set(DURABLE) - set(VECTORS):
                count = source.execute(f'SELECT count(*) FROM "{table}"').fetchone()[0]
                reason = CACHES.get(table, "vector_deferred_to_reembedding" if table in (*VECTORS, *VECTOR_META)
                                    else "full_text_index_rebuilt_by_store")
                counts[table] = {"source": count, "matched": 0, "inserted": 0, "skipped": count, "reason": reason}
                skipped.append({"kind": table, "rows": count, "reason": reason})
            if not replay:
                if source.execute("SELECT 1 FROM sqlite_master WHERE name='sqlite_sequence'").fetchone():
                    for table, value in source.execute("SELECT name,seq FROM sqlite_sequence"):
                        if table in DURABLE:
                            conn.execute("INSERT INTO lcm_id_sequences(table_name,last_id) VALUES (?,?) "
                                         "ON CONFLICT(table_name) DO UPDATE SET last_id=max(last_id,excluded.last_id)",
                                         ("lcm_messages" if table == "messages" else table, value))
                receipt["matched_text"], receipt["inserted_text"] = matched_text, inserted_text
                conn.execute("INSERT INTO metadata(key,value) VALUES (?,?)", (RECEIPT_KEY, json.dumps(receipt)))
            inserted = sum(row["inserted"] for row in counts.values())
            report = finish_report(ledger, work / "lcm", "lcm", inserted).as_dict()
            if not report["ok"]:
                raise ValueError("LCM manifest mismatch")
            conn.commit()
            report.update(status="imported", counted=True, counts=counts, skipped_tables=skipped,
                          whole_source_rows=sum(row["source"] for row in counts.values()),
                          matched_text=source.execute("SELECT count(*) FROM messages").fetchone()[0] if replay else matched_text,
                          inserted_text=inserted_text, references={"resolved": references, "unresolved": 0},
                          conversation_ids=identities, identity_maps=receipt["ids"],
                          embeddings={"status": "deferred", "command": "lina store reembed --source memory"})
            return report
    finally:
        lcm_x_store.unroute(target)
