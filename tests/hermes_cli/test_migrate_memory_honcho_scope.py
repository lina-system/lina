"""A selected host imports its actual recall graph, not the shared workspace."""

import json

import psycopg
import pytest

from hermes_cli.migrate_memory_honcho import migrate_honcho
from tests.hermes_cli._migrate_memory_honcho_fixture import (
    create_dump, database, honcho_python, pg_command, seed_shared_peers, seed_workspace, upgrade,
)
from tests.hermes_cli._migrate_memory_honcho_fixture import honcho_runtime

__all__ = ["honcho_runtime"]


@pytest.mark.parametrize("mode", ["unified", "directional"])
def test_selected_ai_scope_maps_participants_but_not_other_recall(_memory_store, tmp_path, mode):
    python = honcho_python()
    admin = _memory_store.superuser_dsn()
    with database(admin, "lina") as target:
        upgrade(target, "ltm", "l13_message_split", python, tmp_path)
        dump = create_dump(admin, tmp_path / "shared.dump", tmp_path, shared_peers=True)
        selection = tmp_path / "honcho-config.json"
        selection.write_text(json.dumps({"hosts": {"hermes_alpha": {
            "workspace": "alpha", "aiPeer": "source-ai", "peerName": "user", "observationMode": mode}}}))
        options = dict(copied_dump=dump, profile="alpha", agent="new-ai", work_dir=tmp_path,
                       selection_path=selection, destination_dsn=target, restore_command=pg_command("pg_restore"))
        report = migrate_honcho(**options)
        rerun = migrate_honcho(**options)
        assert report["source_peer"] == "source-ai"
        assert report["source_revision"] == "3e7a1c9d5b20"
        assert report["destination_peer"] == "new-ai" and report["person_peer"] == "user"
        assert report["source_manifest"] == report["destination_manifest"]
        assert rerun["inserted"] == rerun["inserted_text"] == 0
        assert {row["reason"] for row in report["excluded"]} == {"other_agent", "other_workspace", "runtime_lease"}
        assert report["counts"]["documents"]["excluded"]["other_agent"] == 3
        assert report["counts"]["documents"]["excluded"]["other_workspace"] == 2
        assert all(count["source"] == count["matched"] + count["inserted"]
                   for count in report["counts"].values())
        with psycopg.connect(target) as conn:
            assert set(conn.execute("SELECT name FROM ltm.peers").fetchall()) == {
                ("new-ai",), ("other-ai",), ("user",)}
            assert set(conn.execute("SELECT name FROM ltm.sessions").fetchall()) == {("session-a",), ("a2a",)}
            assert set(conn.execute("SELECT observer,observed FROM ltm.collections").fetchall()) == {
                ("new-ai", "new-ai"), ("new-ai", "user"), ("new-ai", "other-ai")}
            assert conn.execute("SELECT count(*) FROM ltm.message_meta").fetchone()[0] == 4
            assert conn.execute("SELECT count(*) FROM ltm.documents").fetchone()[0] == 5
            assert conn.execute("SELECT count(*) FROM vec.embeddings").fetchone()[0] == 0
            ids = dict(conn.execute("SELECT key,identity_id FROM memory.identity_keys WHERE source='honcho_peer'").fetchall())
            assert ids["new-ai:new-ai"] != ids["new-ai:other-ai"]
        with pytest.raises(ValueError, match="workspace"):
            migrate_honcho(**options, workspace="not-selected")
        with pytest.raises(ValueError, match="collides"):
            migrate_honcho(**{**options, "agent": "other-ai"})


def test_server_observation_override_selects_self_view_not_ai_view(_memory_store, tmp_path, honcho_runtime):
    from hermes_cli.migrate_memory_honcho_graph import graph
    from hermes_cli.migrate_memory_honcho_scope import Selection, scoped_graph

    with database(_memory_store.superuser_dsn(), "lina") as target:
        upgrade(target, "public", "c4d8e2f6a1b3", honcho_python(), tmp_path)
        with psycopg.connect(target) as conn:
            seed_workspace(conn, "public", "alpha")
            seed_shared_peers(conn, "alpha")
            conn.execute("UPDATE public.session_peers SET configuration=%s WHERE peer_name='source-ai'",
                         (psycopg.types.json.Jsonb({"observe_others": False}),))
        upgrade(target, "public", "l13_message_split", None, tmp_path)
        with psycopg.connect(target) as conn:
            data, excluded = scoped_graph(conn, "public", graph(conn, "public", "alpha"),
                                          Selection("alpha", "source-ai", "user", "directional"))
        assert {(row["observer"], row["observed"]) for row in data["collections"]} == {
            ("source-ai", "source-ai"), ("user", "user"), ("other-ai", "other-ai")}
        assert {row["reason"] for row in excluded} == {"other_agent", "runtime_lease"}
