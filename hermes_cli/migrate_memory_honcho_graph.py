"""Workspace graph extraction, identity normalization, and loss accounting."""

from __future__ import annotations

from collections import Counter
from copy import deepcopy
import hashlib
import json

from psycopg import sql
from psycopg.rows import dict_row
from psycopg.types.json import Jsonb
from hermes_cli.migrate_memory_honcho_ids import mapped_integers

TABLES = (
    "workspaces", "peers", "sessions", "session_peers", "collections",
    "message_meta", "documents", "document_sources", "conclusion_source_messages",
    "message_embeddings", "queue", "webhook_endpoints", "active_queue_sessions",
)
GENERATED = {"queue": "id", "message_embeddings": "id", "documents": "vec_id"}


def rows(conn, schema: str, table: str, where: str = "true", params: tuple = ()) -> list[dict]:
    with conn.cursor(row_factory=dict_row) as cur:
        cur.execute(sql.SQL("SELECT * FROM {}.{} WHERE " + where).format(
            sql.Identifier(schema), sql.Identifier(table)), params)
        return cur.fetchall()


def graph(conn, schema: str, workspace: str) -> dict[str, list[dict]]:
    result = {}
    for table in TABLES:
        if table == "active_queue_sessions":
            keys = [row["work_unit_key"] for row in result["queue"]]
            result[table] = rows(conn, schema, table, "work_unit_key = ANY(%s)", (keys,))
        elif table == "queue":
            result[table] = rows(conn, schema, table,
                "workspace_name = %s OR message_id = ANY(%s) OR session_id = ANY(%s)",
                (workspace, [r["message_id"] for r in result["message_meta"]],
                 [r["id"] for r in result["sessions"]]))
        else:
            key = "name" if table == "workspaces" else "workspace_name"
            result[table] = rows(conn, schema, table, key + " = %s", (workspace,))
    return result


def insert(conn, schema: str, table: str, row: dict, *, override_identity: bool = False) -> dict:
    values = [Jsonb(value) if isinstance(value, (dict, list)) else value for value in row.values()]
    with conn.cursor(row_factory=dict_row) as cur:
        cur.execute(sql.SQL("INSERT INTO {}.{} ({}) " + ("OVERRIDING SYSTEM VALUE " if override_identity else "")
                           + "VALUES ({}) RETURNING *").format(
            sql.Identifier(schema), sql.Identifier(table),
            sql.SQL(", ").join(map(sql.Identifier, row)),
            sql.SQL(", ").join(sql.Placeholder() for _ in row)), values)
        return cur.fetchone()


def summary_ids(metadata: dict, ids: dict[int, int | str]) -> None:
    """Only Honcho's two persisted summary boundaries are integer identities."""
    for label in ("honcho_chat_summary_short", "honcho_chat_summary_long"):
        boundary = metadata.get("summaries", {}).get(label)
        if boundary and "message_id" in boundary:
            old = boundary["message_id"]
            if old not in ids:
                raise ValueError(f"unresolved summary message reference: {old}")
            boundary["message_id"] = ids[old]


def work_key(key: str, source: str, destination: str) -> str:
    parts = key.split(":")
    index = 2 if parts[0] == "dream" else 1
    if len(parts) > index and parts[index] == source and parts[0] != "reconciler":
        parts[index] = destination
    return ":".join(parts)


def renamed(data: dict[str, list[dict]], source: str, destination: str,
            ids: dict[int, int | str], *, defer_vectors: bool = True) -> dict[str, list[dict]]:
    result = deepcopy(data)
    for table, records in result.items():
        for row in records:
            if table == "workspaces":
                row["name"] = destination
            if row.get("workspace_name") == source:
                row["workspace_name"] = destination
            if table == "sessions":
                summary_ids(row["internal_metadata"], ids)
            if table == "documents" and row["internal_metadata"].get("message_ids"):
                row["internal_metadata"]["message_ids"] = mapped_integers(row["internal_metadata"]["message_ids"], ids)
            if table == "queue":
                payload = row["payload"]
                if payload.get("workspace_name") == source:
                    payload["workspace_name"] = destination
                if payload.get("event_type") in {"queue.empty", "test.event"}:
                    if payload["data"].get("workspace_id") == source:
                        payload["data"]["workspace_id"] = destination
                if isinstance(payload.get("message_id"), int):
                    payload["message_id"] = ids[payload["message_id"]]
            if table in {"queue", "message_meta"} and row.get("message_id") is not None:
                row["message_id"] = ids[row["message_id"]]
            if "work_unit_key" in row:
                row["work_unit_key"] = work_key(row["work_unit_key"], source, destination)
            if defer_vectors and table in {"message_embeddings", "documents"}:
                row.update(sync_state="pending", sync_attempts=0, last_sync_at=None)
    return result


def manifest(data: dict[str, list[dict]], texts: dict[int, dict], workspace: str,
             native_ids: dict[str, dict[int, int]] | None = None) -> list[dict]:
    ids = {r["message_id"]: r["public_id"] for r in data["message_meta"]}
    normalized = renamed(data, workspace, workspace, ids, defer_vectors=False)
    result = []
    for table, records in normalized.items():
        for row in records:
            native = row.get("public_id", row.get("id"))
            if table in {"queue", "message_embeddings"} and native_ids:
                native = native_ids[table][row["id"]]
            if native is None:
                native = [row.get(key) for key in (
                    "workspace_name", "session_name", "peer_name", "derived_id", "source_id",
                    "conclusion_id", "message_id") if key in row]
            row.pop(GENERATED.get(table, ""), None)
            if table == "workspaces":
                row["internal_metadata"].pop("lina_dump_import", None)
            if table == "message_meta":
                original = next(r for r in data[table] if r["public_id"] == row["public_id"])
                text = texts[original["message_id"]]
                row["text"] = {key: text[key] for key in ("content", "content_sha256", "agent_id")}
            encoded = json.dumps(row, sort_keys=True, default=str, separators=(",", ":"))
            result.append({"table": table, "native_id": json.dumps(native, separators=(",", ":")),
                           "sha256": hashlib.sha256(encoded.encode()).hexdigest()})
    return sorted(result, key=lambda row: (row["table"], row["native_id"], row["sha256"]))


def source_links(data: dict[str, list[dict]]) -> dict:
    messages = {r["public_id"] for r in data["message_meta"]}
    documents = {r["id"] for r in data["documents"]}
    for row in data["document_sources"]:
        if row["derived_id"] not in documents or row["source_id"] not in documents | messages:
            raise ValueError(f"unresolved document_sources edge: {row['derived_id']} -> {row['source_id']}")
    counts = Counter("message" if r["source_id"] in messages else
                     "document" if r["source_id"] in documents else "unresolved"
                     for r in data["document_sources"])
    return {"count": len(data["document_sources"]), "references": dict(counts),
            "edges": sorted(data["document_sources"], key=lambda r: (r["derived_id"], r["source_id"]))}


def durable_graph(data: dict[str, list[dict]]) -> dict[str, list[dict]]:
    """Project durable fields; runtime jobs and worker-owned progress are not memory."""
    result = deepcopy(data)
    result["queue"] = []
    result["active_queue_sessions"] = []
    for table, records in result.items():
        for row in records:
            if table in {"documents", "message_embeddings"}:
                for field in ("sync_state", "sync_attempts", "last_sync_at"):
                    row.pop(field, None)
            if table == "sessions":
                summaries = row["internal_metadata"].get("summaries", {})
                for label in ("honcho_chat_summary_short", "honcho_chat_summary_long"):
                    summaries.pop(label, None)
                if not summaries:
                    row["internal_metadata"].pop("summaries", None)
            if table == "workspaces":
                row["internal_metadata"].pop("lina_dump_import", None)
    return result


def _metadata_projection(expected: dict, actual: dict) -> dict:
    """Keep imported keys, requiring their values, while allowing additive live keys."""
    projected = {}
    for key, value in expected.items():
        if key not in actual:
            raise ValueError("destination metadata differs from the copied dump")
        current = actual[key]
        if isinstance(value, dict) and isinstance(current, dict):
            projected[key] = _metadata_projection(value, current)
        else:
            projected[key] = current
    return projected


def imported_graph(expected: dict, destination: dict, generated: dict) -> tuple[dict, dict]:
    """Select only receipted durable identities, preserving actual copied field values."""
    baseline = deepcopy(expected)
    result = {table: [] for table in TABLES}
    keys = {"session_peers": ("workspace_name", "session_name", "peer_name"),
            "document_sources": ("derived_id", "source_id"),
            "conclusion_source_messages": ("conclusion_id", "message_id"),
            "message_meta": ("public_id",)}
    for table, records in baseline.items():
        identity = keys.get(table, ("id",))
        available = {tuple(row[key] for key in identity): row for row in destination[table]}
        for row in records:
            wanted = dict(row)
            if table in GENERATED:
                key = GENERATED[table]
                wanted[key] = generated[table][row[key]]
            current = available.get(tuple(wanted[key] for key in identity))
            if current is None or (table in GENERATED and current[GENERATED[table]] != wanted[GENERATED[table]]):
                raise ValueError(f"destination imported identities differ: {table}")
            projected = dict(current)
            if table == "documents":
                if current["times_derived"] < row["times_derived"]:
                    raise ValueError("destination reinforcement differs from the copied dump")
                # Reinforcement is monotonic runtime progress, checked above rather
                # than represented as equal in the durable-content manifests.
                row.pop("times_derived")
                projected.pop("times_derived")
            for field in ("metadata", "internal_metadata"):
                if field in row:
                    projected[field] = _metadata_projection(row[field], current[field])
            result[table].append(projected)
    return baseline, result
