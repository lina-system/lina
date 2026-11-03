"""Real archive restoration, Alembic migrations and populated-store merge."""

import json

import psycopg
import pytest

from hermes_cli.migrate_memory_honcho import migrate_honcho
from hermes_cli.migrate_memory_honcho_graph import graph
from hermes_cli.migrate_memory_honcho_stage import HonchoMigrationError
from tests.hermes_cli._migrate_memory_honcho_fixture import (
    create_dump, database, honcho_python, pg_command, seed_agent, seed_workspace, upgrade,
)
from tests.hermes_cli._migrate_memory_honcho_fixture import honcho_runtime

__all__ = ["honcho_runtime"]


@pytest.fixture
def migration(tmp_path, _memory_store):
    admin = _memory_store.superuser_dsn()
    with database(admin, "lina") as target:
        upgrade(target, "ltm", "c4d8e2f6a1b3", honcho_python(), tmp_path)
        with psycopg.connect(target) as conn:
            seed_workspace(conn, "ltm", "unrelated", "u")
            shared_id = seed_agent(conn)
        upgrade(target, "ltm", "l13_message_split", honcho_python(), tmp_path)
        dump = create_dump(admin, tmp_path / "source.dump", tmp_path)
        selection = tmp_path / "honcho-config.json"
        selection.write_text(json.dumps({"hosts": {"hermes_alpha": {
            "workspace": "alpha", "aiPeer": "source-ai", "peerName": "user", "observationMode": "unified"}}}))
        yield dict(copied_dump=dump, profile="alpha", agent="alpha-copy", work_dir=tmp_path,
                   selection_path=selection,
                   honcho_python=honcho_python(), destination_dsn=target,
                   restore_command=pg_command("pg_restore")), shared_id


def test_real_dump_merges_graph_reuses_agent_and_reruns_without_insert(migration):
    # Given: unrelated destination rows, and agent text also present in the dump.
    options, shared_id = migration
    with psycopg.connect(options["destination_dsn"]) as conn:
        unrelated = graph(conn, "ltm", "unrelated")
        original = conn.execute("SELECT * FROM memory.messages WHERE message_id=%s", (shared_id,)).fetchone()
    # When: the real dump is restored and the real l19/l13 chain runs in staging.
    report = migrate_honcho(**options)
    rerun = migrate_honcho(**options)
    # Then: mapped references preserve the graph, immutable text and other workspace.
    assert report["ok"] and report["source_manifest"] == report["destination_manifest"]
    assert report["matched_text"] == 1 and report["inserted_text"] == 2
    assert report["document_sources"]["references"] == {"message": 1, "document": 1}
    assert report["embeddings"]["document"]["source_vectors"] == 2
    assert report["embeddings"]["message"]["source_vectors"] == 1
    assert rerun["inserted"] == rerun["inserted_text"] == 0
    assert report["counts"]["active_queue_sessions"]["excluded"]["runtime_lease"] == 1
    assert report["skipped"] == []
    assert all(r["source"] == r["matched"] + r["inserted"] for r in report["counts"].values())
    assert all("native_id" in row for row in report["source_manifest"])
    with psycopg.connect(options["destination_dsn"]) as conn:
        assert graph(conn, "ltm", "unrelated") == unrelated
        assert conn.execute("SELECT * FROM memory.messages WHERE message_id=%s", (shared_id,)).fetchone() == original
        meta = conn.execute("SELECT message_id FROM ltm.message_meta WHERE workspace_name='alpha-copy' "
                            "ORDER BY seq_in_session").fetchall()
        assert meta[0][0] == shared_id and len(set(meta)) == 3
        assert conn.execute("SELECT message_id FROM ltm.queue WHERE workspace_name='alpha-copy' ORDER BY id").fetchall() == meta
        summaries = conn.execute("SELECT internal_metadata->'summaries' FROM ltm.sessions "
                                 "WHERE workspace_name='alpha-copy'").fetchone()[0]
        assert summaries["honcho_chat_summary_short"]["message_id"] == meta[0][0]
        assert summaries["honcho_chat_summary_long"]["message_id"] == meta[1][0]
        assert conn.execute("SELECT internal_metadata->'custom' FROM ltm.sessions WHERE workspace_name='alpha-copy'").fetchone()[0] == {"message_id": 777}
        assert conn.execute("SELECT count(*) FROM ltm.active_queue_sessions WHERE work_unit_key LIKE '%alpha-copy%'").fetchone()[0] == 0
        assert conn.execute("SELECT count(*) FROM vec.embeddings WHERE agent_id='alpha-copy'").fetchone()[0] == 0
        assert conn.execute("SELECT count(*) FROM memory.identity_keys WHERE source='honcho_session'").fetchone()[0] == 1
        assert conn.execute("SELECT count(DISTINCT identity_id) FROM memory.identity_keys "
                            "WHERE source IN ('honcho_session','gateway_chat')").fetchone()[0] == 1
        assert set(conn.execute("SELECT kind FROM memory.identity_keys WHERE source='honcho_peer'").fetchall()) == {("person",), ("agent",)}
        assert conn.execute("SELECT count(*) FROM ltm.workspaces WHERE name='not-selected'").fetchone()[0] == 0


def test_existing_workspace_collision_and_changed_rerun_are_refused(migration):
    # Given: an unrelated occupied destination workspace.
    options, _ = migration
    with pytest.raises(ValueError, match="not this exact import"):
        migrate_honcho(**{**options, "agent": "unrelated"})
    migrate_honcho(**options)
    with psycopg.connect(options["destination_dsn"]) as conn:
        conn.execute("UPDATE ltm.documents SET content='changed' WHERE workspace_name='alpha-copy'")
    # When / Then: an import never silently accepts or repairs a changed graph.
    with pytest.raises(ValueError, match="differs from the copied dump"):
        migrate_honcho(**options)


def test_dangling_document_source_refuses_atomic_merge(migration, tmp_path):
    # Given: a permitted legacy polymorphic edge whose target does not exist.
    options, _ = migration
    malformed = create_dump(options["destination_dsn"], tmp_path / "dangling.dump", tmp_path,
                            dangling_source=True)
    # When / Then: refuse instead of claiming an unresolved graph was migrated.
    with pytest.raises(ValueError, match="unresolved document_sources"):
        migrate_honcho(**{**options, "copied_dump": malformed})
    with psycopg.connect(options["destination_dsn"]) as conn:
        assert conn.execute("SELECT count(*) FROM ltm.workspaces WHERE name='alpha-copy'").fetchone()[0] == 0


def test_invalid_archive_reports_safe_failure(migration, tmp_path):
    # Given: an invalid copied archive containing private-looking source bytes.
    options, _ = migration
    archive = tmp_path / "invalid.dump"
    archive.write_bytes(b"private-source-payload password=synthetic-secret")
    # When: the real pg_restore rejects the archive.
    with pytest.raises(HonchoMigrationError) as failure:
        migrate_honcho(**{**options, "copied_dump": archive})
    # Then: the boundary receives structured status, not stderr or argv.
    assert failure.value.operation == "archive restore"
    assert failure.value.returncode != 0
    assert "synthetic-secret" not in str(failure.value)
    assert "private-source-payload" not in str(failure.value)
