"""Resolve a copied host and retain only its recall-readable graph."""

from __future__ import annotations

from dataclasses import dataclass
import json
from pathlib import Path

from hermes_cli.migrate_memory_honcho_graph import TABLES, rows
from hermes_cli.migrate_memory_honcho_ids import document_references, metadata_references, row_identity


@dataclass(frozen=True)
class Selection:
    workspace: str
    ai_peer: str
    person_peer: str | None
    observation_mode: str


def selection(profile: str, path: Path | None, workspace: str | None) -> Selection:
    """Read only the copied host; a CLI workspace is an assertion, not a scope override."""
    from plugins.memory.honcho.client import _host_block, profile_host_key

    if path is None:
        raise ValueError("copied Honcho host selection_path is required")
    host = profile_host_key(profile)
    block = _host_block(json.loads(path.read_text(encoding="utf-8-sig")), host)
    selected = block.get("workspace")
    if workspace and workspace != selected:
        raise ValueError("Honcho workspace does not match the copied host")
    mode = block.get("observationMode", "unified")
    if mode not in {"unified", "directional"}:
        raise ValueError("copied Honcho observationMode must be unified or directional")
    ai = block.get("aiPeer")
    person = block.get("peerName")
    if not isinstance(selected, str) or not isinstance(ai, str) or (person is not None and not isinstance(person, str)):
        raise ValueError("copied Honcho host identity fields must be strings")
    return Selection(selected, ai, person, mode)


def scoped_graph(conn, schema: str, data: dict, selected: Selection) -> tuple[dict, list[dict], list[dict]]:
    """Keep AI-participating sessions and the live adapter's observer/target pairs."""
    from plugins.memory.honcho.client import _OBSERVATION_PRESETS

    ai = selected.ai_peer
    if ai not in {r["name"] for r in data["peers"]}:
        raise ValueError("selected Honcho AI peer not found in source workspace")
    sessions = {r["session_name"] for r in data["session_peers"] if r["peer_name"] == ai}
    participants = [r for r in data["session_peers"] if r["session_name"] in sessions]
    peers = {r["peer_name"] for r in participants} | {ai}
    if selected.person_peer:
        peers.add(selected.person_peer)
    # Both shipped presets read through the AI (ai_observe_others=True).
    # Persisted session peer configuration wins, just as the live manager's
    # get_peer_configuration response wins over the local preset.
    pairs = {(ai, ai)}
    default_observes = _OBSERVATION_PRESETS[selected.observation_mode]["ai_observe_others"]
    for participant in participants:
        if participant["peer_name"] != ai:
            continue
        observes = participant["configuration"].get("observe_others", default_observes)
        if observes is None:
            observes = default_observes
        others = {p["peer_name"] for p in participants if p["session_name"] == participant["session_name"]}
        pairs.update((ai if observes else peer, peer) for peer in others if peer != ai)
    if not sessions and selected.person_peer:
        pairs.add((ai, selected.person_peer))
    result = {table: [] for table in TABLES}
    result["workspaces"] = data["workspaces"]
    result["sessions"] = [r for r in data["sessions"] if r["name"] in sessions]
    result["session_peers"] = participants
    result["collections"] = [r for r in data["collections"] if (r["observer"], r["observed"]) in pairs]
    result["documents"] = [r for r in data["documents"] if (r["observer"], r["observed"]) in pairs]
    result["message_meta"] = [r for r in data["message_meta"] if r["session_name"] in sessions]
    mids = {r["message_id"] for r in result["message_meta"]}
    publics = {r["public_id"] for r in result["message_meta"]}
    docs = {r["id"] for r in result["documents"]}
    peers.update(r["peer_name"] for r in result["message_meta"])
    result["peers"] = [r for r in data["peers"] if r["name"] in peers]
    result["document_sources"] = [r for r in data["document_sources"] if r["derived_id"] in docs]
    result["conclusion_source_messages"] = [r for r in data["conclusion_source_messages"] if r["conclusion_id"] in docs]
    result["message_embeddings"] = [r for r in data["message_embeddings"] if r["message_id"] in publics]
    session_ids = {r["id"] for r in result["sessions"]}
    result["queue"] = [r for r in data["queue"] if r["message_id"] in mids or r["session_id"] in session_ids]
    dependencies = _close_dependencies(data, result)
    excluded = []
    for table in TABLES:
        retained = {row_identity(table, row) for row in result[table]}
        for row in rows(conn, schema, table):
            if row_identity(table, row) in retained:
                continue
            owner = row.get("workspace_name", row.get("name") if table == "workspaces" else None)
            if table == "active_queue_sessions":
                owner = selected.workspace if row in data[table] else None
            reason = "other_agent" if owner == selected.workspace else "other_workspace"
            if table == "active_queue_sessions" and owner == selected.workspace:
                reason = "runtime_lease"
            excluded.append({"table": table, "native_id": row.get("public_id", row.get("id", row.get("name"))),
                             "reason": reason})
    return result, excluded, dependencies


def _close_dependencies(data: dict, result: dict) -> list[dict]:
    """Follow references outward, never expand a dependency's private sibling rows."""
    references = {
        "documents": (("sessions", ("name",), ("session_name",)),
                      ("collections", ("observer", "observed"), ("observer", "observed"))),
        "collections": (("peers", ("name",), ("observer",)), ("peers", ("name",), ("observed",))),
        "message_meta": (("sessions", ("name",), ("session_name",)), ("peers", ("name",), ("peer_name",))),
        "message_embeddings": (("message_meta", ("public_id",), ("message_id",)),
                               ("sessions", ("name",), ("session_name",)),
                               ("peers", ("name",), ("peer_name",))),
        "queue": (("sessions", ("id",), ("session_id",)),
                  ("message_meta", ("message_id",), ("message_id",))),
        "conclusion_source_messages": (("message_meta", ("public_id",), ("message_id",)),),
    }
    dependencies = []
    while True:
        required = metadata_references(result)
        for table, constraints in references.items():
            for row in result[table]:
                for parent, keys, fields in constraints:
                    values = tuple(row[field] for field in fields)
                    if None not in values:
                        required.append((parent, keys, values))
        for row in result["queue"]:
            if isinstance(row["payload"].get("message_id"), int):
                required.append(("message_meta", ("message_id",), (row["payload"]["message_id"],)))
        for row in result["document_sources"]:
            parent = "documents" if any(r["id"] == row["source_id"] for r in data["documents"]) else "message_meta"
            key = "id" if parent == "documents" else "public_id"
            required.append((parent, (key,), (row["source_id"],)))
        for row in result["documents"]:
            for field, value in document_references(row):
                parent = "documents" if field == "copied_from" or any(
                    r["id"] == value for r in data["documents"]) else "message_meta"
                key = "id" if parent == "documents" else "public_id"
                required.append((parent, (key,), (value,)))
        changed = False
        for parent, keys, values in required:
            match = next((row for row in data[parent] if tuple(row[key] for key in keys) == values), None)
            if match is None:
                raise ValueError(f"unresolved document_sources or dependency reference: {parent} {values}")
            if match not in result[parent]:
                result[parent].append(match)
                dependencies.append({"table": parent,
                                     "native_id": match.get("public_id", match.get("id", match.get("name"))),
                                     "reason": "referenced_ancestor"})
                changed = True
        documents = {row["id"] for row in result["documents"]}
        for table, key in (("document_sources", "derived_id"), ("conclusion_source_messages", "conclusion_id")):
            for row in data[table]:
                if row[key] in documents and row not in result[table]:
                    result[table].append(row)
                    dependencies.append({"table": table, "native_id": [row[key], row.get("source_id", row.get("message_id"))],
                                         "reason": "referenced_ancestor"})
                    changed = True
        if not changed:
            return dependencies


def map_peer(data: dict, source: str, destination: str) -> dict:
    """Rename only peer-bearing fields, never arbitrary user metadata or text."""
    from copy import deepcopy

    result = deepcopy(data)
    if source != destination and destination in {r["name"] for r in data["peers"]}:
        raise ValueError("destination AI peer collides with a source participant")
    for table, records in result.items():
        for row in records:
            fields = ("name",) if table == "peers" else ("peer_name", "observer", "observed")
            if table == "peers" and source != destination:
                metadata = row["internal_metadata"]
                label = f"{source}_peer_card"
                if label in metadata:
                    metadata[f"{destination}_peer_card"] = metadata.pop(label)
            for field in fields:
                if row.get(field) == source:
                    row[field] = destination
            if table == "queue":
                if row["payload"].get("event_type") == "queue.empty":
                    for field in ("observer", "observed"):
                        if row["payload"]["data"].get(field) == source:
                            row["payload"]["data"][field] = destination
                for field in ("observer", "observed", "scope_peer", "peer_name"):
                    if row["payload"].get(field) == source:
                        row["payload"][field] = destination
                observers = row["payload"].get("observers")
                if isinstance(observers, list):
                    row["payload"]["observers"] = [destination if p == source else p for p in observers]
                parts = row["work_unit_key"].split(":")
                positions = {"representation": range(3, len(parts)), "summary": (3, 4),
                             "dream": (3, 4), "scope_backfill": (2,), "scope_removal": (2,)}
                for index in positions.get(parts[0], ()):
                    if index < len(parts) and parts[index] == source:
                        parts[index] = destination
                row["work_unit_key"] = ":".join(parts)
    return result
