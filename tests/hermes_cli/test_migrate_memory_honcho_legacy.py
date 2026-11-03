"""Pre-reconciler archives retain raw document provenance across scope closure."""

import json
import subprocess

import psycopg
import pytest
from psycopg.types.json import Jsonb

from hermes_cli.migrate_memory_honcho import migrate_honcho
from hermes_cli.migrate_memory_honcho_graph import TABLES, source_links
from tests.hermes_cli._migrate_memory_honcho_fixture import (
    database, honcho_python, honcho_runtime, pg_command, seed_shared_peers,
    seed_workspace, upgrade,
)

__all__ = ["honcho_runtime"]

FIELDS = ("column", "source_ids", "premise_ids", "copied_from")
CHILD = "ad" * 10 + "d"
PARENT = "d4" * 10 + "d"
MISSING = "x" * 21


@pytest.fixture
def legacy_archive(_memory_store, tmp_path):
    """Build a real DDL-only, undrained archive, not a post-reconciler fixture."""
    admin = _memory_store.superuser_dsn()
    with database(admin) as source, database(admin, "lina") as target:
        upgrade(source, "public", "a7c3e9f1b2d4", honcho_python(), tmp_path)
        with psycopg.connect(source) as conn:
            seed_workspace(conn, "public", "alpha")
            seed_shared_peers(conn, "alpha")
            conn.execute("DELETE FROM public.document_sources")
        upgrade(target, "ltm", "l13_message_split", honcho_python(), tmp_path)
        selection = tmp_path / "selection.json"
        selection.write_text(json.dumps({"hosts": {"hermes_alpha": {
            "workspace": "alpha", "aiPeer": "source-ai", "peerName": "user"}}}))
        yield source, dict(profile="alpha", agent="legacy-copy", work_dir=tmp_path,
                           selection_path=selection, destination_dsn=target,
                           restore_command=pg_command("pg_restore"))


def archive_reference(legacy_archive, field, source_id):
    source, options = legacy_archive
    metadata = {"custom": {"source_ids": [MISSING]}, "prose": "unchanged"}
    if field != "column":
        metadata[field] = source_id if field == "copied_from" else [source_id, "not an id", 17]
    with psycopg.connect(source) as conn:
        conn.execute("UPDATE public.documents SET source_ids=%s, internal_metadata=%s WHERE id=%s",
                     (Jsonb([source_id, "not an id", 17]) if field == "column" else None,
                      Jsonb(metadata), CHILD))
        # A cycle exercises fixed-point closure without importing private siblings.
        conn.execute("UPDATE public.documents SET internal_metadata=%s WHERE id=%s",
                     (Jsonb({"premise_ids": [CHILD]}), PARENT))
        assert conn.execute("SELECT count(*) FROM public.document_sources").fetchone() == (0,)
        assert conn.execute("SELECT version_num FROM public.alembic_version").fetchone() == ("a7c3e9f1b2d4",)
    dump = options["work_dir"] / "legacy.dump"
    with dump.open("wb") as stream:
        subprocess.run([*pg_command("pg_dump"), "--format=custom", "--no-owner", "--no-privileges", source],
                       stdout=stream, stderr=subprocess.PIPE, check=True, timeout=120)
    return {**options, "copied_dump": dump}


@pytest.mark.parametrize("field", FIELDS)
def test_legacy_archive_closes_excluded_ancestors_when_raw_reference_exists(legacy_archive, field):
    # Given: no normalized links, and a raw reference to an excluded collection.
    options = archive_reference(legacy_archive, field, PARENT)
    # When: the actual archive is restored, upgraded, scoped and imported.
    report = migrate_honcho(**options)
    # Then: ancestors and raw identities survive; malformed prose is untouched.
    identities = report["identity_maps"]["documents"]
    assert PARENT in identities
    assert {"table": "documents", "native_id": PARENT, "reason": "referenced_ancestor"} in report["dependencies"]
    assert report["source_revision"] == "a7c3e9f1b2d4"
    assert report["source_manifest"] == report["destination_manifest"]
    with psycopg.connect(options["destination_dsn"]) as conn:
        raw, metadata = conn.execute("SELECT source_ids,internal_metadata FROM ltm.documents WHERE id=%s",
                                     (identities[CHILD],)).fetchone()
        value = raw if field == "column" else metadata[field]
        assert value == (identities[PARENT] if field == "copied_from" else [identities[PARENT], "not an id", 17])
        assert metadata["custom"] == {"source_ids": [MISSING]}
        assert conn.execute("SELECT internal_metadata->'premise_ids' FROM ltm.documents WHERE id=%s",
                            (identities[PARENT],)).fetchone() == ([identities[CHILD]],)
    assert migrate_honcho(**options)["inserted"] == 0


@pytest.mark.parametrize("field", FIELDS)
def test_legacy_archive_refuses_atomically_when_raw_target_is_missing(legacy_archive, field):
    # Given: a valid machine identity with no source document or message.
    options = archive_reference(legacy_archive, field, MISSING)
    # When / Then: refusal leaves no destination graph, identity or message writes.
    with pytest.raises(ValueError, match="unresolved"):
        migrate_honcho(**options)
    with psycopg.connect(options["destination_dsn"]) as conn:
        assert conn.execute("SELECT count(*) FROM ltm.workspaces WHERE name='legacy-copy'").fetchone() == (0,)
        assert conn.execute("SELECT count(*) FROM memory.messages WHERE agent_id='legacy-copy'").fetchone() == (0,)
        assert conn.execute("SELECT count(*) FROM memory.identity_keys WHERE key LIKE 'legacy-copy%'").fetchone() == (0,)


@pytest.mark.parametrize("field", FIELDS)
def test_source_verification_refuses_when_raw_target_is_missing(field):
    # Given: normalized linkage is valid but a raw machine reference dangles.
    data = {table: [] for table in TABLES}
    row = {"id": CHILD, "internal_metadata": {}}
    if field == "column":
        row["source_ids"] = [MISSING]
    else:
        row["internal_metadata"][field] = MISSING if field == "copied_from" else [MISSING]
    data["documents"] = [row]
    data["document_sources"] = [{"derived_id": CHILD, "source_id": CHILD}]
    # When / Then: verification checks both persisted representations.
    with pytest.raises(ValueError, match="unresolved"):
        source_links(data)


def test_all_raw_arrays_close_ancestors_when_precedence_hides_other_locations(legacy_archive):
    # Given: all three arrays coexist; even lower-precedence raw IDs must resolve.
    source, options = legacy_archive
    parents = ["d4" * 10 + "d", "d5" * 10 + "d", "d6" * 10 + "d"]
    archive_reference(legacy_archive, "column", parents[0])
    with psycopg.connect(source) as conn:
        conn.execute("UPDATE public.documents SET internal_metadata=%s WHERE id=%s",
                     (Jsonb({"source_ids": [parents[1]], "premise_ids": [parents[2]]}), CHILD))
    with options["work_dir"].joinpath("legacy.dump").open("wb") as stream:
        subprocess.run([*pg_command("pg_dump"), "--format=custom", "--no-owner", "--no-privileges", source],
                       stdout=stream, stderr=subprocess.PIPE, check=True, timeout=120)
    # When: importing an undrained archive with mixed legacy representations.
    report = migrate_honcho(**{**options, "copied_dump": options["work_dir"] / "legacy.dump"})
    # Then: each raw field maps independently without flattening the precedence.
    identities = report["identity_maps"]["documents"]
    assert set(parents) <= identities.keys()
    with psycopg.connect(options["destination_dsn"]) as conn:
        raw, metadata = conn.execute("SELECT source_ids,internal_metadata FROM ltm.documents WHERE id=%s",
                                     (identities[CHILD],)).fetchone()
        assert raw == [identities[parents[0]], "not an id", 17]
        assert metadata == {"source_ids": [identities[parents[1]]], "premise_ids": [identities[parents[2]]]}


@pytest.mark.parametrize("field", FIELDS)
def test_namespace_preserves_malformed_prose_when_legacy_field_is_not_an_identity(field):
    from hermes_cli.migrate_memory_honcho_ids import namespace

    # Given: historical malformed JSON is not a schema-valid machine identity.
    data = {table: [] for table in TABLES}
    metadata = {"premises": [MISSING], "sources": [MISSING]}
    row = {"id": CHILD, "internal_metadata": metadata}
    if field == "column":
        row["source_ids"] = "not an array"
    else:
        metadata[field] = "not an identity"
    data["documents"] = [row]
    # When: namespacing only schema-defined identities.
    transformed, _ = namespace(data, "legacy-copy")
    # Then: malformed historical fields and arbitrary prose remain verbatim.
    assert transformed["documents"][0]["internal_metadata"] == metadata
    if field == "column":
        assert transformed["documents"][0]["source_ids"] == "not an array"
