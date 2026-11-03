"""Import a copied pg_dump custom/tar archive, never a live Honcho server.

Requires an owner DSN with CREATE DATABASE privilege and pg_restore. An explicit
interpreter needs Honcho dependencies; otherwise the installed image is used.
All vectors are deferred, irrespective of dimensions or ambient model settings.
"""

from __future__ import annotations

from collections.abc import Sequence
import hashlib
from pathlib import Path
import threading

import psycopg
from psycopg import sql
from psycopg.rows import dict_row
from psycopg.types.json import Jsonb

from hermes_cli.migrate_memory_honcho_graph import (
    GENERATED, TABLES, durable_graph, graph, imported_graph, insert, manifest, renamed, rows, source_links,
)
from hermes_cli.migrate_memory_honcho_stage import ROOT, restored_database, upgrade
from hermes_cli.migrate_memory_honcho_scope import map_peer, scoped_graph, selection
from hermes_cli.migrate_memory_honcho_ids import namespace, staging_references


def _prepare(conn, schema: str, workspace: str, agent: str) -> tuple[str, dict]:
    """Keep the dump revision; initialize only the missing shared text table."""
    revision = conn.execute(sql.SQL("SELECT version_num FROM {}.alembic_version").format(
        sql.Identifier(schema))).fetchone()[0]
    if revision == "l13_message_split":
        raise ValueError("expected a pre-split Honcho dump, not l13_message_split")
    if conn.execute("SELECT to_regclass('memory.messages')").fetchone()[0] is None:
        conn.execute("CREATE SCHEMA IF NOT EXISTS memory")
        conn.execute("CREATE EXTENSION IF NOT EXISTS pg_trgm")
        conn.execute((ROOT / "services/store/migrations/0001_memory_messages.sql").read_text(encoding="utf-8-sig"))
    # The selected workspace receives the caller's agent identity before l13
    # matches text. Workspace FK values themselves are not updated.
    conn.execute(sql.SQL("UPDATE {}.messages SET internal_metadata = internal_metadata || "
                         "jsonb_build_object('agent_id', %s::text) WHERE workspace_name = %s").format(
        sql.Identifier(schema)), (agent, workspace))
    decisions = {}
    for table, kind, key in (("documents", "document", "vec_id"),
                             ("message_embeddings", "message", "id")):
        columns = {r[0] for r in conn.execute(
            "SELECT column_name FROM information_schema.columns WHERE table_schema=%s AND table_name=%s",
            (schema, table))}
        count = 0
        if "embedding" in columns:
            count = conn.execute(sql.SQL("SELECT count(*) FROM {}.{} WHERE workspace_name=%s "
                                         "AND embedding IS NOT NULL").format(
                sql.Identifier(schema), sql.Identifier(table)), (workspace,)).fetchone()[0]
        elif conn.execute("SELECT to_regclass('vec.embeddings')").fetchone()[0]:
            count = conn.execute(sql.SQL("SELECT count(*) FROM vec.embeddings e JOIN {}.{} t "
                                         "ON e.item_id=t.{} WHERE e.kind=%s AND t.workspace_name=%s").format(
                sql.Identifier(schema), sql.Identifier(table), sql.Identifier(key)),
                (kind, workspace)).fetchone()[0]
        decisions[kind] = {"source_vectors": count, "copied": 0, "deferred": count,
                           "reason": "Round1 deliberately defers all vectors; no embedding endpoint or model provenance used"}
    return revision, decisions


def _text(conn, row: dict, meta: dict, agent: str, claimed: set[int]) -> tuple[int, bool]:
    """Apply l13's UID then digest/session/time/agent matching at destination."""
    uid = f"honcho:{agent}:{meta['public_id']}"
    with conn.cursor(row_factory=dict_row) as cur:
        cur.execute("SELECT * FROM memory.messages WHERE uid=%s", (uid,))
        candidate = cur.fetchone()
        if candidate is not None and any(candidate[k] != row[k] for k in
                                          ("content", "content_sha256", "agent_id")):
            raise ValueError(f"shared UID collision: {uid}")
        if candidate is None:
            cur.execute("SELECT * FROM memory.messages WHERE content_sha256=%s AND session_id=%s "
                        "AND ts=%s AND agent_id=%s ORDER BY message_id",
                        (row["content_sha256"], row["session_id"], row["ts"], agent))
            candidate = next((r for r in cur.fetchall() if r["message_id"] not in claimed
                              and r["content"] == row["content"]), None)
        if candidate is not None and candidate["message_id"] not in claimed:
            claimed.add(candidate["message_id"])
            return candidate["message_id"], False
    if candidate is not None:
        uid = f"honcho:{agent}:{meta['public_id']}"
    payload = {k: v for k, v in row.items() if k not in {"message_id", "tsv", "conversation_id"}}
    payload.update(uid=uid, agent_id=agent)
    saved = insert(conn, "memory", "messages", payload)
    claimed.add(saved["message_id"])
    return saved["message_id"], True


def _registry(conn, data: dict, ids: dict, workspace: str, agent: str) -> None:
    from plugins.memory.lina.identity import IdentityRegistry
    from hermes_state_store import MessageStore

    registry = IdentityRegistry()
    registry._conn = conn
    # _resolve is the registry's transactional implementation without the live
    # turn fallback: imports must fail rather than return an unpersisted ID.
    registry._resolve("agent", [("profile", agent)])
    for peer in data["peers"]:
        bot = peer["metadata"].get("is_bot") or peer["name"] == agent
        keys = [("honcho_peer", f"{agent}:{peer['name']}")]
        if peer["name"] == agent:
            keys.insert(0, ("profile", agent))
        registry._resolve("agent" if bot else "person", keys)
    store = MessageStore(agent)
    store._write_conn = conn
    store._write_owner = threading.get_ident()
    for session in data["sessions"]:
        selected = [ids[r["message_id"]] for r in data["message_meta"] if r["session_name"] == session["name"]]
        shared = rows(conn, "memory", "messages", "message_id=ANY(%s)", (selected,))
        conversations = {r["conversation_id"] for r in shared if r["conversation_id"]}
        if len(conversations) > 1:
            raise ValueError("Honcho session spans conflicting existing conversations")
        keys = []
        if conversations:
            keys = conn.execute("SELECT source,key FROM memory.identity_keys WHERE kind='conversation' "
                                "AND identity_id=%s ORDER BY first_seen,source,key", (next(iter(conversations)),)).fetchall()
            if not keys:
                raise ValueError("existing conversation has no registry source key")
        else:
            keys = [("hermes_session", f"{agent}:{session['name']}")]
        keys.append(("honcho_session", f"{agent}:{session['name']}"))
        identity = registry._resolve("conversation", keys)
        for source, key in keys:
            actual = conn.execute("SELECT identity_id FROM memory.identity_keys WHERE kind='conversation' "
                                  "AND source=%s AND key=%s", (source, key)).fetchone()[0]
            if actual != identity:
                raise ValueError("existing Honcho alias belongs to another conversation")
        store.set_conversation([r["uid"] for r in shared], identity)


def _merge(conn, data: dict, texts: dict, workspace: str, agent: str,
           profile: str, receipt: dict) -> dict:
    """One transaction publishes the whole graph or refuses a collision."""
    revision = conn.execute("SELECT version_num FROM ltm.alembic_version").fetchall()
    if revision != [("l13_message_split",)]:
        raise ValueError("destination must already be at l13_message_split")
    conn.execute(sql.SQL("LOCK TABLE {} IN SHARE ROW EXCLUSIVE MODE").format(
        sql.SQL(", ").join(sql.Identifier("ltm", table) for table in TABLES)))
    conn.execute("LOCK TABLE memory.messages IN SHARE ROW EXCLUSIVE MODE")
    existing = graph(conn, "ltm", agent)
    source_links(data)
    rerun = bool(existing["workspaces"])
    ids = {}
    generated = {table: {} for table in GENERATED}
    inserted_text = 0
    if rerun:
        stored = existing["workspaces"][0]["internal_metadata"].get("lina_dump_import", {})
        if {key: value for key, value in stored.items() if key not in {"generated_ids", "message_ids"}} != receipt:
            raise ValueError("destination workspace exists and is not this exact import")
        generated = {table: {int(old): new for old, new in mapping.items()}
                     for table, mapping in stored["generated_ids"].items()}
        by_public = {r["public_id"]: r["message_id"] for r in existing["message_meta"]}
        if not {r["public_id"] for r in data["message_meta"]} <= set(by_public):
            raise ValueError("destination message placements differ from the imported dump")
        ids = {r["message_id"]: by_public[r["public_id"]] for r in data["message_meta"]}
        if {str(old): new for old, new in ids.items()} != stored["message_ids"]:
            raise ValueError("destination shared message identities differ from the import receipt")
    else:
        claimed = {r[0] for r in conn.execute("SELECT message_id FROM ltm.message_meta")}
        original_ids = {receipt["identity_maps"]["message_meta"][public]: int(old)
                        for old, public in receipt["source_message_ids"].items()}
        for meta in sorted(data["message_meta"], key=lambda r: (texts[r["message_id"]]["ts"], original_ids[r["public_id"]])):
            row = dict(texts[meta["message_id"]])
            row["agent_id"] = agent
            ids[meta["message_id"]], added = _text(conn, row, meta, agent, claimed)
            inserted_text += added
        _registry(conn, data, ids, workspace, agent)
        _text_metadata(conn, data, ids, agent)
        transformed = renamed(data, workspace, agent, ids)
        transformed["workspaces"][0]["internal_metadata"]["lina_dump_import"] = receipt
        for table in TABLES:
            records = transformed[table]
            if table in GENERATED:
                source_ids = {new: old for old, new in receipt["identity_maps"].get(table, {}).items()}
                records = sorted(records, key=lambda r: (r["created_at"], source_ids.get(r["id"], r["id"])))
            for record in records:
                old = record.get(GENERATED.get(table, ""))
                if table in GENERATED:
                    del record[GENERATED[table]]
                saved = insert(conn, "ltm", table, record)
                if table in GENERATED:
                    generated[table][old] = saved[GENERATED[table]]
        receipt["generated_ids"] = generated
        receipt["message_ids"] = ids
        conn.execute("UPDATE ltm.workspaces SET internal_metadata=jsonb_set(internal_metadata,"
                     "'{lina_dump_import}',%s) WHERE name=%s", (Jsonb(receipt), agent))
    destination = graph(conn, "ltm", agent)
    destination_texts = {r["message_id"]: r for r in rows(conn, "memory", "messages",
        "message_id = ANY(%s)", (list(ids.values()),))}
    if rerun:
        _text_metadata(conn, data, ids, agent)
    expected = renamed(data, workspace, agent, ids)
    expected_texts = {ids[k]: {**v, "agent_id": agent} for k, v in texts.items()}
    verified = destination
    if rerun:
        expected = durable_graph(expected)
        expected, verified = imported_graph(expected, durable_graph(destination), generated)
    source_manifest = manifest(expected, expected_texts, agent)
    destination_manifest = manifest(verified, destination_texts, agent,
        {table: {new: old for old, new in mapping.items()} for table, mapping in generated.items()})
    if source_manifest != destination_manifest:
        raise ValueError("destination workspace graph differs from the copied dump")
    counts = {table: {"source": len(records), "matched": len(records) if rerun else 0,
                      "inserted": 0 if rerun else len(records), "skipped": 0}
              for table, records in data.items()}
    return {"ok": True, "source_manifest": source_manifest, "destination_manifest": destination_manifest,
            "source_counts": {k: len(v) for k, v in data.items()}, "destination_counts": {k: len(v) for k, v in destination.items()},
            "counts": counts, "skipped": [],
            "inserted": sum(r["inserted"] for r in counts.values()), "inserted_text": inserted_text,
            "matched": sum(r["matched"] for r in counts.values()),
            "matched_text": len(data["message_meta"]) - inserted_text,
            "manifest_scope": "imported_durable_projection" if rerun else "initial_transfer",
            "runtime_queue": {"basis": "dump_receipt" if rerun else "initial_transfer",
                              "already_imported": len(data["queue"]) if rerun else 0,
                              "current_rows": len(destination["queue"]), "rewritten": 0},
            "document_sources": source_links(verified), "message_id_map": ids,
            "generated_id_maps": generated}


def _text_metadata(conn, data: dict, ids: dict, agent: str) -> None:
    """Keep l13's explicit identity metadata linked to the chosen destination text."""
    uids = dict(conn.execute("SELECT message_id,uid FROM memory.messages WHERE message_id=ANY(%s)",
                            (list(ids.values()),)).fetchall())
    for row in data["message_meta"]:
        for field in ("metadata", "internal_metadata"):
            if "uid" in row[field]:
                row[field]["uid"] = uids[ids[row["message_id"]]]
            if "agent_id" in row[field]:
                row[field]["agent_id"] = agent


def migrate_honcho(copied_dump: Path, profile: str, agent: str, work_dir: Path, *,
                   workspace: str | None = None, honcho_python: Path | None = None,
                   selection_path: Path | None = None,
                   destination_dsn: str | None = None,
                   restore_command: Sequence[str] | None = None) -> dict:
    """Restore, upgrade and merge one workspace; exact reruns insert zero rows."""
    from hermes_platform.resolver import locate_command
    from hermes_state_store import resolve_owner_dsn

    selected = selection(profile, selection_path, workspace)
    workspace = selected.workspace
    installed_store = destination_dsn is None
    destination_dsn = destination_dsn or resolve_owner_dsn()
    restore_command = restore_command or locate_command("pg_restore").command
    container_local = False
    if not restore_command:
        from hermes_cli.store import Store, store_dir
        restore_command = Store(store_dir()).compose_argv("exec", "-T", "db", "pg_restore")
        container_local = True
    work_dir = work_dir.resolve()
    work_dir.mkdir(parents=True, exist_ok=True)
    with copied_dump.open("rb") as stream:
        digest = hashlib.file_digest(stream, "sha256").hexdigest()
    receipt = {"sha256": digest, "workspace": workspace, "profile": profile, "agent": agent,
               "ai_peer": selected.ai_peer, "person_peer": selected.person_peer,
               "observation_mode": selected.observation_mode}
    with restored_database(copied_dump, destination_dsn, work_dir, restore_command,
                           container_local=container_local) as staging:
        with psycopg.connect(staging) as source:
            schemas = source.execute("SELECT table_schema FROM information_schema.tables "
                                     "WHERE table_name='alembic_version'").fetchall()
            if len(schemas) != 1:
                raise ValueError("dump must contain exactly one Honcho Alembic version table")
            schema = schemas[0][0]
            # Upgrade older archives to the actual l19 parent before inspecting
            # modern workspace columns, without replacing their revision stamp.
            original_revision = source.execute(sql.SQL("SELECT version_num FROM {}.alembic_version").format(
                sql.Identifier(schema))).fetchone()[0]
            if original_revision == "l13_message_split":
                raise ValueError("expected a pre-split Honcho dump, not l13_message_split")
        if original_revision != "l19_vec_embeddings":
            upgrade(staging, schema, "c4d8e2f6a1b3", honcho_python, work_dir, installed_store=installed_store)
        with psycopg.connect(staging) as source:
            _, decisions = _prepare(source, schema, workspace, agent)
            legacy_messages = dict(source.execute(sql.SQL("SELECT id,public_id FROM {}.messages").format(
                sql.Identifier(schema))).fetchall())
            source_uids = dict(source.execute(sql.SQL("SELECT public_id,(metadata || internal_metadata)->>'uid' "
                                                       "FROM {}.messages").format(sql.Identifier(schema))).fetchall())
        upgrade(staging, schema, "l13_message_split", honcho_python, work_dir, installed_store=installed_store)
        with psycopg.connect(staging) as source:
            data = graph(source, schema, workspace)
            if len(data["workspaces"]) != 1:
                raise ValueError(f"source workspace not found: {workspace}")
            staging_references(data, legacy_messages)
            data, excluded, dependencies = scoped_graph(source, schema, data, selected)
            data = map_peer(data, selected.ai_peer, agent)
            data, identity_maps = namespace(data, agent)
            receipt["identity_maps"] = identity_maps
            receipt["source_message_ids"] = {str(old): public for old, public in legacy_messages.items()
                                              if public in identity_maps["message_meta"]}
            receipt["source_uids"] = {public: source_uids[public] for public in identity_maps["message_meta"]}
            texts = {r["message_id"]: r for r in rows(source, "memory", "messages",
                "message_id = ANY(%s)", ([r["message_id"] for r in data["message_meta"]],))}
        with psycopg.connect(destination_dsn) as destination:
            report = _merge(destination, data, texts, workspace, agent, profile, receipt)
        for table, counts in report["counts"].items():
            counts["excluded"] = {reason: sum(row["table"] == table and row["reason"] == reason
                                              for row in excluded)
                                  for reason in ("other_agent", "other_workspace", "runtime_lease")}
        report.update(source_revision=original_revision, destination_revision="l13_message_split",
                      embeddings=decisions, source_workspace=workspace, destination_workspace=agent,
                      source_peer=selected.ai_peer, destination_peer=agent, person_peer=selected.person_peer,
                      observation_mode=selected.observation_mode, excluded=excluded, dependencies=dependencies,
                      identity_maps=identity_maps)
        return report
