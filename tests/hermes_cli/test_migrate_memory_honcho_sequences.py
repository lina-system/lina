"""Imported identities must remain reachable by the live store readers."""

from contextlib import closing
import json
import subprocess

import psycopg
import pytest

from hermes_cli.migrate_memory_honcho import migrate_honcho
from hermes_cli.store_reembed import reembed
from hermes_state_store import MessageStore, SearchTerm
from tests.hermes_cli.test_store_reembed import _Client
from tests.hermes_cli._migrate_memory_honcho_fixture import (
    database, honcho_python, honcho_runtime, pg_command, seed_shared_peers, seed_workspace, upgrade,
)

__all__ = ["honcho_runtime"]


@pytest.fixture
def sequence_archive(_memory_store, tmp_path):
    admin = _memory_store.superuser_dsn()
    with database(admin) as source, database(admin, "lina") as target:
        upgrade(source, "public", "c4d8e2f6a1b3", honcho_python(), tmp_path)
        with psycopg.connect(source) as conn:
            seed_workspace(conn, "public", "alpha")
            seed_shared_peers(conn, "alpha")
            # Native insertion order deliberately disagrees with historical time.
            conn.execute("UPDATE public.messages SET metadata='{\"role\":\"user\"}', created_at=to_timestamp(1700000000 + "
                         "CASE public_id WHEN %s THEN 30 WHEN %s THEN 10 ELSE 20 END)",
                         ("a1" * 10 + "1", "a2" * 10 + "2"))
            for table in ("documents", "message_embeddings", "queue"):
                conn.execute(f"UPDATE public.{table} SET created_at=to_timestamp(1700000000) + "
                             f"(row_numbered.ordinal % 3) * interval '1 second' FROM "
                             f"(SELECT id,row_number() OVER (ORDER BY id DESC) AS ordinal FROM public.{table}) "
                             f"row_numbered WHERE {table}.id=row_numbered.id")
        dump = tmp_path / "sequence.dump"
        with dump.open("wb") as stream:
            subprocess.run([*pg_command("pg_dump"), "--format=custom", "--no-owner", "--no-privileges", source],
                           stdout=stream, stderr=subprocess.PIPE, check=True, timeout=120)
        upgrade(target, "ltm", "l13_message_split", honcho_python(), tmp_path)
        selection = tmp_path / "selection.json"
        selection.write_text(json.dumps({"hosts": {"hermes_alpha": {
            "workspace": "alpha", "aiPeer": "source-ai", "peerName": "user"}}}))
        yield dict(copied_dump=dump, profile="alpha", agent="sequence-copy", work_dir=tmp_path,
                   selection_path=selection, destination_dsn=target,
                   restore_command=pg_command("pg_restore"))


def test_reembedding_reaches_imported_rows_and_receipt_ids_survive_replay(sequence_archive):
    # Given: a real historical archive with shared-agent references.
    options = sequence_archive
    first = migrate_honcho(**options)
    # When: the live positive-cursor reembedding pipeline reads the migrated store.
    reembed(lambda: psycopg.connect(options["destination_dsn"], autocommit=True), _Client(),
            ["agent", "messages", "memory"], batch_size=2, from_start=True)
    # Then: every imported eligible row has a vector, without a negative-ID exception.
    with psycopg.connect(options["destination_dsn"]) as conn:
        assert conn.execute("SELECT count(*) FROM ltm.documents d LEFT JOIN vec.embeddings e "
                            "ON e.kind='document' AND e.item_id=d.vec_id "
                            "WHERE d.workspace_name='sequence-copy' AND e.item_id IS NULL").fetchone() == (0,)
        assert conn.execute("SELECT count(*) FROM ltm.message_meta m LEFT JOIN vec.chunks c "
                            "ON c.message_ids=ARRAY[m.message_id] WHERE m.workspace_name='sequence-copy' "
                            "AND c.chunk_id IS NULL").fetchone() == (0,)
        for table, column in (("memory.messages", "message_id"), ("ltm.documents", "vec_id"),
                              ("ltm.message_embeddings", "id"), ("ltm.queue", "id")):
            assert conn.execute(f"SELECT count(*) FROM {table} WHERE {column} <= 0").fetchone() == (0,)
            if table != "memory.messages":
                times = [row[0] for row in conn.execute(f"SELECT created_at FROM {table} ORDER BY {column}")]
                assert times == sorted(times)
    repeated = migrate_honcho(**options)
    assert repeated["inserted"] == repeated["inserted_text"] == 0
    assert repeated["message_id_map"] == first["message_id_map"]
    assert repeated["generated_id_maps"] == first["generated_id_maps"]
    # Once Honcho releases its placements, the ordinary upward collector can visit them.
    with psycopg.connect(options["destination_dsn"]) as conn:
        uids = {row[0] for row in conn.execute("SELECT uid FROM memory.messages")}
        conn.execute("DELETE FROM ltm.message_meta")
        conn.execute("UPDATE memory.messages SET observed_at=to_timestamp(1600000000)")
    visited = set()

    def held(page):
        visited.update(page)
        return set(page)

    with closing(MessageStore("sequence-copy", dsn=options["destination_dsn"])) as store:
        assert store.collect_unreferenced(held) == 0
    assert visited == uids


def test_source_time_orders_allocations_and_search_in_fresh_store(sequence_archive):
    # Given: a fresh destination and historical rows inserted out of time order.
    options = sequence_archive
    # When: migrating out-of-order historical source rows.
    report = migrate_honcho(**options)
    # Then: allocated IDs ascend with source time; live search retains actual recency.
    with psycopg.connect(options["destination_dsn"]) as conn:
        times = [row[0] for row in conn.execute("SELECT ts FROM memory.messages ORDER BY message_id")]
        assert times == sorted(times)
        ordered_publics = [row[0] for row in conn.execute("SELECT public_id FROM ltm.message_meta ORDER BY message_id")]
        assert ordered_publics == [report["identity_maps"]["message_meta"][source] for source in
                                   ("a2" * 10 + "2", "a3" * 10 + "3", "bm" * 10 + "m", "a1" * 10 + "1")]
        newest = conn.execute("SELECT uid FROM memory.messages WHERE content LIKE '%alpha%' "
                              "ORDER BY ts DESC,message_id DESC LIMIT 1").fetchone()[0]
    with closing(MessageStore("sequence-copy", dsn=options["destination_dsn"])) as store:
        assert store.search_groups([[SearchTerm("alpha")]])[0][1] == newest
