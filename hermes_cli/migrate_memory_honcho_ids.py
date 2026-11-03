"""Destination-owned Honcho identities and their schema-defined references."""

from copy import deepcopy
import base64
import hashlib
import json
import re

TEXT_IDS = {"workspaces": "id", "peers": "id", "sessions": "id", "collections": "id",
            "documents": "id", "message_meta": "public_id", "webhook_endpoints": "id",
            "active_queue_sessions": "id"}
SOURCE_ID = re.compile(r"^[A-Za-z0-9_-]{21}$")


def document_references(row: dict) -> list[tuple[str, str]]:
    """Retain all persisted machine references, not only the ORM's winning array.

    The reconciler ignores malformed values; preserving them verbatim keeps
    historical prose from becoming newly valid provenance after namespacing.
    """
    result = []
    metadata = row["internal_metadata"]
    for field, candidate in (("column", row.get("source_ids")),
                             ("source_ids", metadata.get("source_ids")),
                             ("premise_ids", metadata.get("premise_ids")),
                             ("copied_from", [metadata.get("copied_from")])):
        if isinstance(candidate, list):
            result.extend((field, value) for value in candidate
                          if isinstance(value, str) and SOURCE_ID.fullmatch(value))
    return result


def destination_id(agent: str, source: str) -> str:
    digest = hashlib.sha256(json.dumps([agent, source], separators=(",", ":")).encode()).digest()
    return base64.urlsafe_b64encode(digest).decode()[:21]


def integer_id(agent: str, kind: str, source: int | str) -> int:
    digest = hashlib.sha256(json.dumps([agent, kind, source], separators=(",", ":")).encode()).digest()
    # Negative IDs leave the positive live identity sequences undisturbed.
    return -1 - (int.from_bytes(digest[:8], "big") % (2**63 - 1))


def mapped_integers(value, ids: dict):
    if isinstance(value, list):
        return [mapped_integers(item, ids) for item in value]
    return ids[value]


def integer_leaves(value) -> list[int]:
    if isinstance(value, list):
        return [leaf for item in value for leaf in integer_leaves(item)]
    return [value]


def staging_references(data: dict, legacy_messages: dict[int, str]) -> None:
    """l13 changes relational IDs but leaves legacy JSON integer provenance untouched."""
    shared = {row["public_id"]: row["message_id"] for row in data["message_meta"]}
    legacy = {old: shared[public] for old, public in legacy_messages.items() if public in shared}
    for row in data["queue"]:
        if isinstance(row["payload"].get("message_id"), int):
            row["payload"]["message_id"] = legacy[row["payload"]["message_id"]]
    for row in data["documents"]:
        metadata = row["internal_metadata"]
        if metadata.get("message_ids"):
            metadata["message_ids"] = mapped_integers(metadata["message_ids"], legacy)


def row_identity(table: str, row: dict) -> tuple:
    if table == "session_peers":
        return (row["workspace_name"], row["session_name"], row["peer_name"])
    if table == "document_sources":
        return (row["derived_id"], row["source_id"])
    if table == "conclusion_source_messages":
        return (row["conclusion_id"], row["message_id"])
    return (row.get("public_id", row.get("id")),)


def metadata_references(data: dict) -> list[tuple]:
    required = []
    for row in data["sessions"]:
        for label in ("honcho_chat_summary_short", "honcho_chat_summary_long"):
            boundary = row["internal_metadata"].get("summaries", {}).get(label, {})
            for field, column in (("message_id", "message_id"), ("message_public_id", "public_id")):
                if boundary.get(field) is not None:
                    required.append(("message_meta", (column,), (boundary[field],)))
    for row in data["documents"]:
        required.extend(("message_meta", ("message_id",), (value,))
                        for value in integer_leaves(row["internal_metadata"].get("message_ids", [])))
    return required


def _queue_ids(row: dict, maps: dict, agent: str) -> None:
    if row["session_id"] is not None:
        row["session_id"] = maps["sessions"][row["session_id"]]
    payload = row["payload"]
    if payload.get("message_public_id"):
        payload["message_public_id"] = maps["message_meta"][payload["message_public_id"]]
    if payload.get("deletion_type") == "observation" and payload.get("resource_id"):
        old = payload["resource_id"]
        payload["resource_id"] = maps["documents"].get(old, destination_id(agent, old))
        parts = row["work_unit_key"].split(":")
        if parts[0] == "deletion" and len(parts) == 4:
            parts[3] = payload["resource_id"]
            row["work_unit_key"] = ":".join(parts)


def namespace(data: dict, agent: str) -> tuple[dict, dict]:
    """Remap global IDs, not scoped names or arbitrary strings in user metadata."""
    result = deepcopy(data)
    maps = {table: {row[column]: destination_id(agent, row[column]) for row in data[table]}
            for table, column in TEXT_IDS.items()}
    evidence = {**maps["documents"], **maps["message_meta"]}
    for table, records in result.items():
        for row in records:
            if table in TEXT_IDS:
                column = TEXT_IDS[table]
                row[column] = maps[table][row[column]]
            if table == "queue":
                _queue_ids(row, maps, agent)
            if table == "sessions":
                for label in ("honcho_chat_summary_short", "honcho_chat_summary_long"):
                    summary = row["internal_metadata"].get("summaries", {}).get(label, {})
                    if summary.get("message_public_id"):
                        summary["message_public_id"] = maps["message_meta"][summary["message_public_id"]]
            if table == "documents":
                metadata = row["internal_metadata"]
                references = document_references(row)
                for field, value in references:
                    if field == "copied_from":
                        metadata[field] = maps["documents"][value]
                for field, owner in (("source_ids", row), ("source_ids", metadata), ("premise_ids", metadata)):
                    candidate = owner.get(field)
                    if isinstance(candidate, list):
                        owner[field] = [evidence[value] if isinstance(value, str) and SOURCE_ID.fullmatch(value)
                                        else value for value in candidate]
            if table == "document_sources":
                row["derived_id"] = maps["documents"][row["derived_id"]]
                row["source_id"] = evidence[row["source_id"]]
            if table == "conclusion_source_messages":
                row["conclusion_id"] = maps["documents"][row["conclusion_id"]]
                row["message_id"] = maps["message_meta"][row["message_id"]]
            if table == "message_embeddings":
                row["message_id"] = maps["message_meta"][row["message_id"]]
    return result, maps
