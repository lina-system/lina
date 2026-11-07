"""Selected recall retains only the ancestors required by its references."""

import json
import subprocess

import psycopg
import pytest

from hermes_cli.migrate_memory_honcho import migrate_honcho
from tests.hermes_cli._migrate_memory_honcho_fixture import (
    database, honcho_python, pg_command, seed_shared_peers, seed_workspace, upgrade,
)
from tests.hermes_cli._migrate_memory_honcho_fixture import honcho_runtime

__all__ = ["honcho_runtime"]


@pytest.mark.parametrize("dependency", ["document_session", "queue_session", "queue_message", "sources", "embedding"])
def test_selected_graph_retains_dependencies_without_private_siblings(_memory_store, tmp_path, dependency):
    # Given: selected rows point to nonparticipating sessions or private evidence.
    admin = _memory_store.superuser_dsn()
    dump = tmp_path / "dependencies.dump"
    with database(admin) as source:
        upgrade(source, "public", "c4d8e2f6a1b3", honcho_python(), tmp_path)
        with psycopg.connect(source) as conn:
            seed_workspace(conn, "public", "alpha")
            seed_shared_peers(conn, "alpha")
            changes = {
                "document_session": "UPDATE public.documents SET session_name='other-session' "
                                    "WHERE observer='source-ai'",
                "queue_session": "UPDATE public.queue SET session_id=(SELECT id FROM public.sessions "
                                 "WHERE name='other-session')",
                "queue_message": "UPDATE public.queue SET session_id=(SELECT id FROM public.sessions "
                                 "WHERE name='session-a'),message_id=(SELECT id FROM public.messages "
                                 "WHERE session_name='other-session')",
                "sources": "INSERT INTO public.document_sources(derived_id,source_id,workspace_name) "
                           "VALUES ('" + "ad" * 10 + "d','" + "d4" * 10 + "d','alpha'); "
                           "INSERT INTO public.conclusion_source_messages(conclusion_id,message_id,workspace_name) "
                           "SELECT '" + "ad" * 10 + "d',public_id,'alpha' FROM public.messages "
                           "WHERE session_name='other-session'",
                "embedding": "UPDATE public.message_embeddings SET session_name='other-session'",
            }
            conn.execute(changes[dependency])
        with dump.open("wb") as archive:
            subprocess.run([*pg_command("pg_dump"), "--format=custom", "--no-owner", "--no-privileges", source],
                           stdout=archive, stderr=subprocess.PIPE, check=True, timeout=120)
    selection = tmp_path / "host.json"
    selection.write_text(json.dumps({"hosts": {"hermes_alpha": {
        "workspace": "alpha", "aiPeer": "source-ai", "peerName": "user"}}}))
    with database(admin, "lina") as target:
        upgrade(target, "ltm", "l13_message_split", honcho_python(), tmp_path)
        # When: importing the real archive with the selected host's recall scope.
        options = dict(copied_dump=dump, profile="alpha", agent="new-ai", work_dir=tmp_path,
                       selection_path=selection, destination_dsn=target, restore_command=pg_command("pg_restore"))
        report = migrate_honcho(**options)
        # Then: original ancestor identities survive without their sibling recall.
        with psycopg.connect(target) as conn:
            assert conn.execute("SELECT count(*) FROM ltm.sessions WHERE name='other-session'").fetchone()[0] == 1
            assert conn.execute("SELECT count(*) FROM ltm.session_peers WHERE session_name='other-session'").fetchone()[0] == 0
            assert conn.execute("SELECT count(*) FROM ltm.documents WHERE observer='other-ai' "
                                "AND observed='other-ai'").fetchone()[0] == 0
            assert conn.execute("SELECT count(*) FROM ltm.message_meta WHERE session_name='other-session'").fetchone()[0] == int(dependency in {"queue_message", "sources"})
        assert any(row["table"] == "sessions" and row["native_id"] == "cs" * 10 + "s"
                   for row in report["dependencies"])
        assert migrate_honcho(**options)["inserted"] == 0
