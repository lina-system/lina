"""Source schema, lossless projections and logical references for native LCM copies."""

import hashlib
import json

DURABLE = (
    "metadata", "lcm_embedding_profile", "messages", "summary_nodes", "summary_node_provenance",
    "lcm_lifecycle_state", "lcm_migration_state", "lcm_assertion_sources", "lcm_assertions",
    "lcm_assertion_relations", "lcm_imported_messages", "lcm_imported_summaries", "lcm_rollups",
    "lcm_rollup_sources", "lcm_rollup_invalidations", "lcm_rollup_state", "lcm_entities",
    "lcm_entity_aliases", "lcm_entity_keys", "lcm_memory_host_file_revisions", "lcm_memory_notes",
    "lcm_memory_events", "lcm_teams_tenants", "lcm_teams_principals", "lcm_teams_collections",
    "lcm_teams_memberships", "lcm_teams_revisions", "lcm_teams_audit", "lcm_teams_requests",
    "message_relations", "host_uid_bindings", "lcm_trajectory_corpora", "lcm_trajectory_sources",
    "lcm_trajectory_states", "lcm_trajectory_assets", "lcm_trajectory_ingest_receipts",
    "lcm_trajectory_transitions", "lcm_trajectory_embedding_profiles", "lcm_trajectory_state_embedding_profiles",
    "lcm_embedding_meta", "lcm_chunk_meta",
)
CACHES = {
    **dict.fromkeys(("lcm_query_corpus_state", "lcm_query_corpus_events", "lcm_query_views",
                    "lcm_query_view_versions", "lcm_query_view_sources"), "query_cache_rebuilt_from_imported_corpus"),
    **dict.fromkeys(("lcm_embedding_binary", "lcm_chunk_binary", "lcm_trajectory_embeddings",
                    "lcm_trajectory_state_embeddings"), "derived_vector_index_rebuilt_from_retained_text"),
    "lcm_embedding_backfill_inflight": "source_worker_lease_not_resumed",
}
VECTORS = ("lcm_embedding_vectors", "lcm_chunk_vectors")
VECTOR_META = ("lcm_embedding_meta", "lcm_chunk_meta")
RECEIPT_KEY = "lina_memory_import:v1"
QUARANTINE = "lcm_fork_quarantined_messages"
QUARANTINE_SUFFIX = "::fork-quarantine"
LEFT_OUT = "fork_quarantine_uncited"
# Rows that belong to one message and go wherever it goes; every other store_id reference cites it.
OWNED_BY_MESSAGE = ("messages", "host_uid_bindings", "lcm_chunk_meta")
CITING = (("message_relations", "store_id"), ("message_relations", "related_store_id"),
          ("lcm_assertion_sources", "source_store_id"), ("lcm_assertions", "source_store_id"),
          ("lcm_imported_messages", "target_store_id"))


def digest(row: dict) -> str:
    normalized = {k: {"bytes": bytes(v).hex()} if isinstance(v, (bytes, memoryview)) else v for k, v in row.items()}
    return hashlib.sha256(json.dumps(normalized, sort_keys=True, ensure_ascii=False,
                                     separators=(",", ":")).encode()).hexdigest()


def schema(source) -> dict:
    tables = {}
    for name, ddl in source.execute("SELECT name,sql FROM sqlite_master WHERE type='table' ORDER BY name"):
        if name.startswith("sqlite_"):
            continue
        if name in DURABLE or name in CACHES or name in VECTORS or name in VECTOR_META:
            tables[name] = list(source.execute(f'PRAGMA table_info("{name}")'))
        elif (any(name.startswith(prefix) for prefix in ("messages_fts", "nodes_fts", "lcm_trajectory_states_fts"))
              or name == QUARANTINE):
            tables[name] = []
        else:
            raise ValueError(f"unsupported LCM table: {name}")
    if not {"messages", "summary_nodes"} <= tables.keys():
        raise ValueError("LCM copy requires messages and summary_nodes")
    for table, required in (("messages", {"store_id", "session_id", "role", "content", "timestamp"}),
                            ("summary_nodes", {"node_id", "session_id", "summary", "source_ids", "source_type"})):
        if not required <= {r[1] for r in tables[table]}:
            raise ValueError(f"unsupported LCM columns: {table}")
    return tables


def verify_references(source, tables: dict) -> int:
    """Validate the summary DAG and explicit source edges before any destination write."""
    messages = {r[0] for r in source.execute("SELECT store_id FROM messages")}
    nodes = {r[0] for r in source.execute("SELECT node_id FROM summary_nodes")}
    edges, count = {}, 0
    for node, kind, encoded in source.execute("SELECT node_id,source_type,source_ids FROM summary_nodes"):
        refs = json.loads(encoded)
        if kind not in {"messages", "nodes"} or not isinstance(refs, list):
            raise ValueError(f"invalid LCM summary reference type: {node}")
        available = messages if kind == "messages" else nodes
        if any(type(ref) is not int or ref not in available for ref in refs):
            raise ValueError(f"unresolved LCM summary reference: {node}")
        edges[node] = refs if kind == "nodes" else []
        count += len(refs)
    done = set()
    for node in edges:
        pending, active = [(node, False)], set()
        while pending:
            current, exiting = pending.pop()
            if exiting:
                active.remove(current)
                done.add(current)
            elif current not in done:
                if current in active:
                    raise ValueError("cyclic LCM summary reference")
                active.add(current)
                pending.append((current, True))
                pending.extend((child, False) for child in edges[current])
    for table, column, available in (
        ("summary_node_provenance", "node_id", nodes), ("lcm_rollup_sources", "node_id", nodes),
        ("message_relations", "store_id", messages), ("message_relations", "related_store_id", messages),
        ("host_uid_bindings", "store_id", messages), ("lcm_assertion_sources", "source_store_id", messages),
        ("lcm_assertions", "source_store_id", messages), ("lcm_imported_messages", "target_store_id", messages),
        ("lcm_imported_summaries", "target_node_id", nodes), ("lcm_chunk_meta", "store_id", messages),
    ):
        if table in tables:
            # LCM-X records the uids it mints for its own rows as kind='engine' with store_id 0, which names no message.
            engine = " AND NOT (kind='engine' AND store_id=0)" if table == "host_uid_bindings" else ""
            for (value,) in source.execute(f'SELECT "{column}" FROM "{table}" WHERE "{column}" IS NOT NULL{engine}'):
                if value not in available:
                    raise ValueError(f"unresolved LCM reference: {table}.{column}/{value}")
                count += 1
    for table, column, parent, parent_column in (
        ("lcm_rollup_sources", "rollup_id", "lcm_rollups", "rollup_id"),
        ("lcm_assertion_relations", "from_assertion_id", "lcm_assertions", "assertion_id"),
        ("lcm_assertion_relations", "to_assertion_id", "lcm_assertions", "assertion_id"),
        ("lcm_entity_aliases", "entity_id", "lcm_entities", "entity_id"),
        ("lcm_entity_keys", "entity_id", "lcm_entities", "entity_id"),
        ("lcm_memory_notes", "entity_id", "lcm_entities", "entity_id"),
        ("lcm_memory_events", "target_event_id", "lcm_memory_events", "event_id"),
        ("lcm_trajectory_states", "source_id", "lcm_trajectory_sources", "source_id"),
        ("lcm_trajectory_assets", "state_id", "lcm_trajectory_states", "state_id"),
        ("lcm_trajectory_transitions", "pre_state_id", "lcm_trajectory_states", "state_id"),
        ("lcm_trajectory_transitions", "post_state_id", "lcm_trajectory_states", "state_id"),
    ):
        if table in tables:
            available = {r[0] for r in source.execute(f'SELECT "{parent_column}" FROM "{parent}"')} if parent in tables else set()
            for (value,) in source.execute(f'SELECT "{column}" FROM "{table}" WHERE "{column}" IS NOT NULL'):
                if value not in available:
                    raise ValueError(f"unresolved LCM reference: {table}.{column}/{value}")
                count += 1
    return count


def fork_quarantine(source, tables: dict) -> dict:
    """The unrestored fork-quarantined messages nothing cites, and the chunks only they own (docs/store.md)."""
    found = {"records": 0, "store_ids": set(), "chunk_ids": set(), "kept_cited": 0}
    if QUARANTINE not in tables:
        return found
    found["records"] = source.execute(f"SELECT count(*) FROM {QUARANTINE}").fetchone()[0]
    held = {r[0] for r in source.execute(f"SELECT store_id FROM {QUARANTINE} WHERE restored_at IS NULL")}
    held &= {store_id for store_id, session in source.execute(
        "SELECT store_id,session_id FROM messages WHERE session_id LIKE '%::fork-quarantine'")
        if session.endswith(QUARANTINE_SUFFIX)}
    cited = set()
    for kind, encoded in source.execute("SELECT source_type,source_ids FROM summary_nodes"):
        if kind == "messages":
            cited.update(json.loads(encoded))
    for table, column in CITING:
        if table in tables:
            cited.update(r[0] for r in source.execute(f'SELECT "{column}" FROM "{table}"'))
    found["store_ids"] = held - cited
    found["kept_cited"] = len(held & cited)
    if "lcm_chunk_meta" in tables:
        found["chunk_ids"] = {chunk for chunk, store_id in source.execute("SELECT chunk_id,store_id FROM lcm_chunk_meta")
                              if store_id in found["store_ids"]}
    return found


def engine_bindings(source, tables: dict) -> int:
    if "host_uid_bindings" not in tables:
        return 0
    return source.execute("SELECT count(*) FROM host_uid_bindings WHERE kind='engine' AND store_id=0").fetchone()[0]


def left_out(table: str, row: dict, quarantine: dict) -> bool:
    return table in OWNED_BY_MESSAGE and row.get("store_id") in quarantine["store_ids"]


def source_rows(source, table: str):
    cursor = source.execute(f'SELECT rowid AS __native_rowid__,* FROM "{table}" ORDER BY rowid')
    names = [column[0] for column in cursor.description]
    for values in cursor:
        row = dict(zip(names, values))
        native = row.pop("__native_rowid__")
        yield native, row
