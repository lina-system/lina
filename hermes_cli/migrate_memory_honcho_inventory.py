"""Machine-readable inventory of the importer's schema-defined identity fields."""

from hermes_cli.migrate_memory_honcho_ids import TEXT_IDS, document_references

# Paths are relative to a row; * visits collection members without interpreting prose.
REFERENCE_PATHS = {
    "workspace_names": ("*.workspace_name", "workspaces.name", "queue.payload.workspace_name"),
    "peer_names": ("peers.name", "*.peer_name", "*.observer", "*.observed",
                   "queue.payload.observer", "queue.payload.observed", "queue.payload.scope_peer",
                   "queue.payload.peer_name", "queue.payload.observers.*"),
    "session_names": ("*.session_name", "queue.payload.session_name"),
    "session_ids": ("queue.session_id",),
    "shared_message_ids": ("message_meta.message_id", "queue.message_id", "queue.payload.message_id"),
    "public_message_ids": ("message_embeddings.message_id", "conclusion_source_messages.message_id",
                           "queue.payload.message_public_id"),
    "document_ids": ("document_sources.derived_id", "conclusion_source_messages.conclusion_id"),
    "polymorphic_source_ids": ("document_sources.source_id",),
    "summary_message_ids": tuple(f"sessions.internal_metadata.summaries.{label}.message_id"
                                 for label in ("honcho_chat_summary_short", "honcho_chat_summary_long")),
    "summary_public_ids": tuple(f"sessions.internal_metadata.summaries.{label}.message_public_id"
                                for label in ("honcho_chat_summary_short", "honcho_chat_summary_long")),
    "explicit_text_uids": ("message_meta.metadata.uid", "message_meta.internal_metadata.uid"),
    "explicit_agent_ids": ("message_meta.metadata.agent_id", "message_meta.internal_metadata.agent_id"),
    "work_unit_keys": ("queue.work_unit_key", "active_queue_sessions.work_unit_key"),
    "generated_ids": ("documents.vec_id", "message_embeddings.id", "queue.id"),
    **{f"text_ids.{table}": (f"{table}.{column}",) for table, column in TEXT_IDS.items()},
}
SPECIAL_CLASSES = ("legacy_source_ids.column", "legacy_source_ids.source_ids", "legacy_source_ids.premise_ids",
                   "copied_from", "legacy_message_ids", "observation_deletion_ids", "peer_card_keys")


def _paths(value, parts: tuple, prefix: tuple):
    if not parts:
        if value is not None:
            yield prefix
        return
    field, *rest = parts
    if field == "*":
        if isinstance(value, list):
            for name, child in enumerate(value):
                yield from _paths(child, tuple(rest), (*prefix, name))
    elif isinstance(value, dict) and field in value:
        yield from _paths(value[field], tuple(rest), (*prefix, field))


def reference_locations(data: dict) -> dict[str, list[tuple]]:
    """Return each checked field occurrence; duplicate references count separately."""
    locations = {label: [] for label in (*REFERENCE_PATHS, *SPECIAL_CLASSES)}
    for label, specs in REFERENCE_PATHS.items():
        for spec in specs:
            table, *parts = spec.split(".")
            for name in data if table == "*" else (table,):
                for index, row in enumerate(data[name]):
                    locations[label].extend(_paths(row, tuple(parts), (name, index)))
    for index, row in enumerate(data["documents"]):
        for field, _ in document_references(row):
            label = "copied_from" if field == "copied_from" else f"legacy_source_ids.{field}"
            path = ("source_ids",) if field == "column" else ("internal_metadata", field)
            locations[label].append(("documents", index, *path))
        pending = [(row["internal_metadata"].get("message_ids", []), ("documents", index, "internal_metadata", "message_ids"))]
        while pending:
            value, path = pending.pop()
            if isinstance(value, list):
                pending.extend((item, (*path, i)) for i, item in enumerate(value))
            else:
                locations["legacy_message_ids"].append(path)
    for index, row in enumerate(data["queue"]):
        payload = row["payload"]
        if payload.get("event_type") in {"queue.empty", "test.event"}:
            locations["workspace_names"].extend(_paths(payload, ("data", "workspace_id"), ("queue", index, "payload")))
        if payload.get("event_type") == "queue.empty":
            for field in ("observer", "observed"):
                locations["peer_names"].extend(_paths(payload, ("data", field), ("queue", index, "payload")))
            locations["session_names"].extend(_paths(payload, ("data", "session_id"), ("queue", index, "payload")))
        if row["payload"].get("deletion_type") == "observation" and row["payload"].get("resource_id"):
            locations["observation_deletion_ids"].append(("queue", index, "payload", "resource_id"))
    for index, row in enumerate(data["peers"]):
        locations["peer_card_keys"].extend(("peers", index, "internal_metadata", key)
                                            for key in row["internal_metadata"] if key.endswith("_peer_card"))
    return locations


def reference_inventory(data: dict) -> dict[str, int]:
    """Counts describe the retained source graph, including zero-occurrence classes."""
    return {label: len(paths) for label, paths in reference_locations(data).items()}
