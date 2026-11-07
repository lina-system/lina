"""The shipped migration report enumerates the references checked on this run."""

from hermes_cli.migrate_memory_honcho import migrate_honcho
from hermes_cli.migrate_memory_honcho_graph import TABLES, renamed
from hermes_cli.migrate_memory_honcho_ids import namespace
from hermes_cli.migrate_memory_honcho_inventory import reference_inventory, reference_locations
from hermes_cli.migrate_memory_honcho_scope import map_peer
from tests.hermes_cli.test_migrate_memory_honcho_sequences import sequence_archive
from tests.hermes_cli._migrate_memory_honcho_fixture import honcho_runtime

__all__ = ["honcho_runtime", "sequence_archive"]


def test_migration_report_exposes_counted_reference_inventory(sequence_archive):
    # Given / When: a real archive with relational and JSON references is imported.
    report = migrate_honcho(**sequence_archive)
    # Then: the CLI-consumed report includes checked reference classes and their counts.
    inventory = report["reference_inventory"]
    assert inventory and all(isinstance(count, int) and count >= 0 for count in inventory.values())
    assert inventory["text_ids.documents"] == report["source_counts"]["documents"]
    assert inventory["shared_message_ids"] >= report["source_counts"]["message_meta"]


def test_inventory_covers_every_identity_changed_by_remappers():
    # Given: every schema-defined reference shape, with opaque lookalikes beside it.
    public, doc, session = "m" * 21, "d" * 21, "s" * 21
    data = {table: [] for table in TABLES}
    data.update(
        workspaces=[{"id": "w" * 21, "name": "old", "internal_metadata": {}}],
        peers=[{"id": "p" * 21, "name": "ai", "internal_metadata": {"ai_peer_card": "prose"}}],
        sessions=[{"id": session, "name": "kept", "workspace_name": "old", "internal_metadata": {
            "summaries": {label: {"message_id": 1, "message_public_id": public}
                          for label in ("honcho_chat_summary_short", "honcho_chat_summary_long")}}}],
        collections=[{"id": "c" * 21, "observer": "ai", "observed": "ai"}],
        message_meta=[{"message_id": 1, "public_id": public, "workspace_name": "old", "peer_name": "ai"}],
        documents=[{"id": doc, "vec_id": 2, "observer": "ai", "observed": "ai", "source_ids": [public],
                    "internal_metadata": {"message_ids": [[1]], "source_ids": [doc], "premise_ids": [public],
                                          "copied_from": doc, "custom": public}}],
        document_sources=[{"derived_id": doc, "source_id": public}],
        conclusion_source_messages=[{"conclusion_id": doc, "message_id": public}],
        message_embeddings=[{"id": 3, "message_id": public}],
        queue=[{"id": 4, "message_id": 1, "session_id": session, "workspace_name": "old",
                "work_unit_key": f"deletion:old:observation:{doc}", "payload": {
                    "message_id": 1, "message_public_id": public, "workspace_name": "old",
                    "observer": "ai", "observed": "ai", "scope_peer": "ai", "peer_name": "ai",
                    "observers": ["ai"], "deletion_type": "observation", "resource_id": doc,
                    "event_type": "queue.empty", "data": {"workspace_id": "old", "observer": "ai", "observed": "ai"}}}],
    )
    paths = reference_locations(data)
    covered = {path for locations in paths.values() for path in locations}

    def leaves(value, path=()):
        if isinstance(value, dict):
            return {p: v for key, child in value.items() for p, v in leaves(child, (*path, key)).items()}
        if isinstance(value, list):
            return {p: v for key, child in enumerate(value) for p, v in leaves(child, (*path, key)).items()}
        return {path: value}

    # When: running the actual peer, text and integer remappers, not a duplicate implementation.
    transformed, _ = namespace(map_peer(data, "ai", "new"), "new")
    transformed = renamed(transformed, "old", "new", {1: 99}, defer_vectors=False)
    before, after = leaves(data), leaves(transformed)
    changed = {path for path, value in before.items() if after.get(path) != value}
    # Then: any remapped field absent from the report's inventory fails this relationship.
    assert changed
    assert all(any(path[:len(parent)] == parent for parent in covered) for path in changed)
    assert set(reference_inventory(data)) == set(paths)
    assert reference_inventory(data)["legacy_message_ids"] == 1
    assert reference_inventory(data)["summary_message_ids"] == 2


def test_inventory_ignores_opaque_events_and_malformed_peer_lists():
    # Given: arbitrary event data and a malformed legacy field preserved by the mapper.
    data = {table: [] for table in TABLES}
    data["queue"] = [{"payload": {"event_type": "custom", "observers": "prose",
                                  "data": {"workspace_id": "opaque", "observer": "opaque"}}}]
    # When / Then: inspection neither interprets opaque values nor crashes on them.
    counts = reference_inventory(data)
    assert counts["workspace_names"] == counts["peer_names"] == 0
