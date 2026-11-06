"""Independent destination agents can import one shared Honcho workspace."""

import json
import subprocess

import psycopg

from hermes_cli.migrate_memory_honcho import migrate_honcho
from tests.hermes_cli._migrate_memory_honcho_fixture import (
    database, honcho_python, honcho_runtime, pg_command, seed_shared_peers, seed_workspace, upgrade,
)

__all__ = ["honcho_runtime"]


def test_shared_workspace_imports_two_agents_without_cross_references(_memory_store, tmp_path):
    # Given: both legacy profiles recall from the same physical workspace and share a session.
    admin = _memory_store.superuser_dsn()
    dump = tmp_path / "shared.dump"
    with database(admin) as source:
        upgrade(source, "public", "c4d8e2f6a1b3", honcho_python(), tmp_path)
        with psycopg.connect(source) as conn:
            seed_workspace(conn, "public", "alpha")
            seed_shared_peers(conn, "alpha")
            conn.execute("UPDATE public.messages SET internal_metadata=jsonb_build_object('uid', 'explicit-' || public_id)")
            conn.execute("UPDATE public.documents SET internal_metadata=jsonb_build_object("
                         "'message_ids',jsonb_build_array(jsonb_build_array((SELECT min(id) FROM public.messages))),"
                         "'copied_from',%s::text,'source_ids',jsonb_build_array(%s::text),"
                         "'custom',jsonb_build_object('id',%s::text)) WHERE id=%s",
                         ("ae" * 10 + "e", "ae" * 10 + "e", "ae" * 10 + "e", "ad" * 10 + "d"))
            conn.execute("UPDATE public.sessions SET internal_metadata=jsonb_set(internal_metadata,"
                         "'{summaries,honcho_chat_summary_short,message_public_id}',to_jsonb(%s::text)) "
                         "WHERE name='session-a'", ("a1" * 10 + "1",))
            conn.execute("UPDATE public.queue SET session_id=(SELECT id FROM public.sessions WHERE name='session-a'),"
                         "payload=jsonb_build_object('message_id',message_id,'message_public_id',%s::text)",
                         ("a1" * 10 + "1",))
            conn.execute("INSERT INTO public.queue(work_unit_key,task_type,payload,session_id,workspace_name) "
                         "SELECT %s,'deletion',jsonb_build_object('deletion_type','observation','resource_id',%s::text),"
                         "id,'alpha' FROM public.sessions WHERE name='session-a'",
                         ("deletion:alpha:observation:" + "ad" * 10 + "d", "ad" * 10 + "d"))
            conn.execute("INSERT INTO public.queue(work_unit_key,task_type,payload,session_id,workspace_name) "
                         "SELECT 'deletion:alpha:session:source-ai','deletion',"
                         "'{\"deletion_type\":\"session\",\"resource_id\":\"source-ai\"}',id,'alpha' "
                         "FROM public.sessions WHERE name='session-a'")
        with dump.open("wb") as archive:
            subprocess.run([*pg_command("pg_dump"), "--format=custom", "--no-owner", "--no-privileges", source],
                           stdout=archive, stderr=subprocess.PIPE, check=True, timeout=120)
    with database(admin, "lina") as target:
        upgrade(target, "ltm", "l13_message_split", honcho_python(), tmp_path)
        reports = {}
        selections = []
        for profile, peer, agent in (("alpha", "source-ai", "agent-a"), ("beta", "other-ai", "agent-b")):
            selection = tmp_path / f"{profile}.json"
            selection.write_text(json.dumps({"hosts": {f"hermes_{profile}": {
                "workspace": "alpha", "aiPeer": peer, "peerName": "user"}}}))
            options = dict(copied_dump=dump, profile=profile, agent=agent, work_dir=tmp_path,
                           selection_path=selection, destination_dsn=target,
                           restore_command=pg_command("pg_restore"))
            selections.append(options)
            # When: each scope is imported into the same store, then independently replayed.
            first = migrate_honcho(**options)
            repeated = migrate_honcho(**options)
            assert first["ok"] and repeated["ok"] and repeated["inserted"] == 0
            assert first["identity_maps"] == repeated["identity_maps"]
            assert all(row["source"] == row["matched"] + row["inserted"] for row in first["counts"].values())
            reports[agent] = first
        for options in selections:
            assert migrate_honcho(**options)["inserted"] == 0
        # Then: every global identity and FK belongs to exactly one destination workspace.
        with psycopg.connect(target) as conn:
            for table, column in (("workspaces", "id"), ("peers", "id"), ("sessions", "id"),
                                  ("collections", "id"), ("documents", "id"), ("message_meta", "public_id")):
                count, distinct = conn.execute(f"SELECT count(*), count(DISTINCT {column}) FROM ltm.{table}").fetchone()
                assert count == distinct
            assert conn.execute("SELECT count(*) FROM ltm.queue q JOIN ltm.sessions s ON s.id=q.session_id "
                                "WHERE q.workspace_name<>s.workspace_name").fetchone()[0] == 0
            assert conn.execute("SELECT count(*) FROM ltm.message_meta m JOIN memory.messages t USING(message_id) "
                                "WHERE m.workspace_name<>t.agent_id").fetchone()[0] == 0
            assert conn.execute("SELECT count(*) FROM ltm.message_meta m JOIN memory.messages t USING(message_id) "
                                "WHERE m.internal_metadata->>'uid' IS DISTINCT FROM t.uid").fetchone()[0] == 0
            assert conn.execute("SELECT count(*) FROM ltm.document_sources e JOIN ltm.documents d ON d.id=e.derived_id "
                                "LEFT JOIN ltm.documents p ON p.id=e.source_id "
                                "LEFT JOIN ltm.message_meta m ON m.public_id=e.source_id "
                                "WHERE d.workspace_name<>coalesce(p.workspace_name,m.workspace_name)").fetchone()[0] == 0
            for agent, report in reports.items():
                receipt = conn.execute("SELECT internal_metadata->'lina_dump_import' FROM ltm.workspaces WHERE name=%s",
                                       (agent,)).fetchone()[0]
                assert receipt["identity_maps"] == report["identity_maps"]
                assert all(value.startswith("explicit-") for value in receipt["source_uids"].values())
                assert set(receipt["source_message_ids"].values()) == set(report["identity_maps"]["message_meta"])
                for queue in conn.execute("SELECT message_id,payload FROM ltm.queue WHERE workspace_name=%s", (agent,)):
                    if queue[0] is not None:
                        assert queue[1]["message_id"] == queue[0]
                        assert queue[1]["message_public_id"] == report["identity_maps"]["message_meta"]["a1" * 10 + "1"]
                    elif queue[1]["deletion_type"] == "observation":
                        assert queue[1]["resource_id"] == report["identity_maps"]["documents"]["ad" * 10 + "d"]
                    else:
                        assert queue[1]["resource_id"] == "source-ai"
                doc = conn.execute("SELECT internal_metadata FROM ltm.documents WHERE id=%s",
                                   (report["identity_maps"]["documents"].get("ad" * 10 + "d", "absent"),)).fetchone()
                if doc:
                    assert doc[0]["copied_from"] == report["identity_maps"]["documents"]["ae" * 10 + "e"]
                    assert doc[0]["custom"]["id"] == "ae" * 10 + "e"
                    for nested in doc[0]["message_ids"]:
                        assert conn.execute("SELECT workspace_name FROM ltm.message_meta WHERE message_id=%s",
                                            (nested[0],)).fetchone() == (agent,)
                for table in ("sessions", "documents", "message_meta"):
                    assert conn.execute(f"SELECT count(*) FROM ltm.{table} WHERE workspace_name=%s",
                                        (agent,)).fetchone()[0] == report["source_counts"][table]
