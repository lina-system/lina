"""Receipt-bound reruns verify durable memory, not moving runtime state."""

import psycopg
import pytest

from hermes_cli.migrate_memory_honcho import migrate_honcho
from hermes_cli.migrate_memory_honcho_graph import graph
from tests.hermes_cli.test_migrate_memory_honcho import migration
from tests.hermes_cli._migrate_memory_honcho_fixture import honcho_runtime

__all__ = ["honcho_runtime", "migration"]


@pytest.mark.parametrize("progress", ["consumed", "updated", "new", "vectors", "metadata", "message", "reinforced"])
def test_rerun_accepts_live_progress_without_rewriting(migration, progress):
    # Given: an imported graph followed by legitimate runtime activity.
    options, _ = migration
    first = migrate_honcho(**options)
    changes = {
        "consumed": "DELETE FROM ltm.queue WHERE workspace_name='alpha-copy'",
        "updated": "UPDATE ltm.queue SET processed=true,error='retry' WHERE workspace_name='alpha-copy'",
        "new": "INSERT INTO ltm.queue(work_unit_key,task_type,payload,workspace_name) "
               "VALUES ('new-live-job','representation','{}','alpha-copy')",
        "vectors": "UPDATE ltm.documents SET sync_state='synced',sync_attempts=2,last_sync_at=now() "
                   "WHERE workspace_name='alpha-copy'; UPDATE ltm.message_embeddings "
                   "SET sync_state='synced',sync_attempts=2,last_sync_at=now() WHERE workspace_name='alpha-copy'",
        "metadata": "UPDATE ltm.sessions SET internal_metadata=internal_metadata || "
                    "'{\"live_counter\":1,\"summaries\":{}}'::jsonb WHERE workspace_name='alpha-copy'",
        "reinforced": "UPDATE ltm.documents SET times_derived=times_derived+1 WHERE workspace_name='alpha-copy'",
        "message": "INSERT INTO memory.messages(uid,source,agent_id,session_id,role,content,content_sha256) "
                   "VALUES ('live-message','agent','alpha-copy','session-a','user','new live text',"
                   "repeat('a',64)); INSERT INTO ltm.message_meta(message_id,public_id,session_name,"
                   "peer_name,workspace_name,token_count,seq_in_session) SELECT message_id,repeat('n',21),"
                   "'session-a','user','alpha-copy',3,4 FROM memory.messages WHERE uid='live-message'",
    }
    with psycopg.connect(options["destination_dsn"]) as conn:
        conn.execute(changes[progress])
        before = graph(conn, "ltm", "alpha-copy")
    # When: the identical copied archive is imported again.
    report = migrate_honcho(**options)
    # Then: existing memory is verified and runtime state is untouched.
    assert report["inserted"] == report["inserted_text"] == 0
    with psycopg.connect(options["destination_dsn"]) as conn:
        assert graph(conn, "ltm", "alpha-copy") == before
    assert report["runtime_queue"]["already_imported"] == first["source_counts"]["queue"]
    assert report["runtime_queue"]["basis"] == "dump_receipt"
    assert all(row["table"] != "queue" for row in report["destination_manifest"])


@pytest.mark.parametrize("damage", ["document", "embedding", "placement", "metadata", "identity", "reinforcement_loss"])
def test_rerun_still_refuses_durable_damage_after_queue_consumption(migration, damage):
    # Given: consumed runtime jobs and damaged imported durable memory.
    options, _ = migration
    migrate_honcho(**options)
    changes = {
        "document": "UPDATE ltm.documents SET content='corrupt' WHERE workspace_name='alpha-copy'",
        "embedding": "DELETE FROM ltm.message_embeddings WHERE workspace_name='alpha-copy'",
        "placement": "DELETE FROM ltm.message_meta WHERE workspace_name='alpha-copy'",
        "metadata": "UPDATE ltm.sessions SET internal_metadata=internal_metadata - 'keep' "
                    "WHERE workspace_name='alpha-copy'",
        "identity": "UPDATE ltm.documents SET vec_id=vec_id+10000 WHERE workspace_name='alpha-copy'",
        "reinforcement_loss": "UPDATE ltm.documents SET times_derived=0 WHERE workspace_name='alpha-copy'",
    }
    with psycopg.connect(options["destination_dsn"]) as conn:
        conn.execute("DELETE FROM ltm.queue WHERE workspace_name='alpha-copy'")
        conn.execute(changes[damage])
    # When / Then: receipt accounting never hides durable loss or corruption.
    with pytest.raises(ValueError, match="differ|missing"):
        migrate_honcho(**options)
