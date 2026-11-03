"""Real prehead-to-head coverage, outside the CI-ignored alembic directory."""

import hashlib
import json
import uuid
from collections.abc import Iterator
from pathlib import Path

import pytest
import sqlalchemy as sa
from alembic import command
from alembic.config import Config
from sqlalchemy.engine import Connection, make_url

from src.config import settings

ROOT = Path(__file__).resolve().parents[1]


@pytest.fixture
def migration_connection() -> Iterator[Connection]:
    """Each test owns a database and an unprivileged migration role."""
    url = make_url(settings.DB.CONNECTION_URI)
    name = f"split_{uuid.uuid4().hex}"
    role = f"honcho_{uuid.uuid4().hex}"
    admin = sa.create_engine(url.set(database="postgres"), isolation_level="AUTOCOMMIT")
    engine = sa.create_engine(url.set(database=name))
    with admin.connect() as conn:
        conn.exec_driver_sql(f'CREATE ROLE "{role}"')
        conn.exec_driver_sql(f'CREATE DATABASE "{name}"')
        conn.exec_driver_sql(f'GRANT CREATE ON DATABASE "{name}" TO "{role}"')
    try:
        with engine.begin() as conn:
            for extension in ("vector", "pg_trgm", "btree_gin"):
                conn.exec_driver_sql(f"CREATE EXTENSION IF NOT EXISTS {extension}")
            conn.exec_driver_sql(f'CREATE SCHEMA IF NOT EXISTS "{settings.DB.SCHEMA}"')
            conn.exec_driver_sql(
                f'ALTER SCHEMA "{settings.DB.SCHEMA}" OWNER TO "{role}"'
            )
            conn.exec_driver_sql("CREATE SCHEMA memory")
            conn.exec_driver_sql(
                (ROOT.parent / "store/migrations/0001_memory_messages.sql").read_text()
            )
            conn.exec_driver_sql(f'GRANT USAGE ON SCHEMA memory TO "{role}"')
            conn.exec_driver_sql(
                f'GRANT SELECT, INSERT, REFERENCES ON memory.messages TO "{role}"'
            )
            conn.exec_driver_sql(
                "ALTER TABLE memory.messages ENABLE ROW LEVEL SECURITY"
            )
            conn.exec_driver_sql("ALTER TABLE memory.messages FORCE ROW LEVEL SECURITY")
            conn.exec_driver_sql(
                f'CREATE POLICY honcho_rows ON memory.messages TO "{role}" USING (true) WITH CHECK (true)'
            )
        with engine.connect() as conn:
            conn.exec_driver_sql(f'SET ROLE "{role}"')
            conn.commit()
            yield conn
    finally:
        engine.dispose()
        with admin.connect() as conn:
            conn.exec_driver_sql(f'DROP DATABASE "{name}" WITH (FORCE)')
            conn.exec_driver_sql(f'DROP ROLE "{role}"')
        admin.dispose()


def _config(connection: Connection) -> Config:
    config = Config(str(ROOT / "alembic.ini"))
    config.set_main_option("script_location", str(ROOT / "migrations"))
    config.attributes["connection"] = connection
    return config


@pytest.mark.parametrize("mismatch", [False, True])
def test_message_split_from_prehead(
    migration_connection: Connection, mismatch: bool
) -> None:
    # Given: immutable shared rows, including IDs that swap with old IDs.
    conn = migration_connection
    config = _config(conn)
    schema = conn.dialect.identifier_preparer.quote(settings.DB.SCHEMA)
    with conn.begin():
        command.upgrade(config, "c4d8e2f6a1b3")
    with conn.begin():
        role = conn.exec_driver_sql("SELECT current_user").scalar_one()
        conn.exec_driver_sql("RESET ROLE")
        conn.exec_driver_sql(
            f"INSERT INTO {schema}.workspaces (id, name) VALUES ('wwwwwwwwwwwwwwwwwwwww', 'workspace')"
        )
        conn.exec_driver_sql(
            f"INSERT INTO {schema}.peers (id, name, workspace_name) VALUES ('ppppppppppppppppppppp', 'peer', 'workspace')"
        )
        conn.exec_driver_sql(
            f"INSERT INTO {schema}.sessions (id, name, workspace_name) VALUES ('sssssssssssssssssssss', 'session', 'workspace')"
        )
        for mid, uid, content, agent in [
            (2, "known", "uid text", "workspace"),
            (1, "digest", "digest text", "workspace"),
            (3, "other-agent", "fresh text", "other"),
        ]:
            conn.execute(
                sa.text("""INSERT INTO memory.messages
                (message_id, uid, source, agent_id, session_id, content, ts, content_sha256)
                OVERRIDING SYSTEM VALUE VALUES (:id, :uid, 'agent', :agent, 'session', :content, '2026-01-01Z', :digest)"""),
                {
                    "id": mid,
                    "uid": uid,
                    "agent": agent,
                    "content": content,
                    "digest": hashlib.sha256(content.encode()).hexdigest(),
                },
            )
        # Explicit IDs do not advance the identity sequence.
        conn.exec_driver_sql(
            "SELECT setval(pg_get_serial_sequence('memory.messages', 'message_id'), 3)"
        )
        for mid, content, metadata in [
            (1, "uid text", {"uid": "known"}),
            (2, "digest text", {}),
            (3, "fresh text", {"source": "messages"}),
            (4, "wrong" if mismatch else "uid text", {"uid": "known"}),
        ]:
            conn.execute(
                sa.text(f"""INSERT INTO {schema}.messages
                (id, public_id, session_name, content, metadata, internal_metadata,
                 token_count, seq_in_session, created_at, peer_name, workspace_name)
                OVERRIDING SYSTEM VALUE VALUES (:id, :public, 'session', :content,
                CAST(:metadata AS jsonb), '{{"keep": true}}', 7, :id, '2026-01-01Z', 'peer', 'workspace')"""),
                {
                    "id": mid,
                    "public": str(mid) * 21,
                    "content": content,
                    "metadata": json.dumps(metadata),
                },
            )
            conn.execute(
                sa.text(
                    f"INSERT INTO {schema}.queue (work_unit_key, task_type, payload, message_id) VALUES (:key, 'representation', '{{}}', :id)"
                ),
                {"key": str(mid), "id": mid},
            )
        conn.exec_driver_sql(
            f"INSERT INTO {schema}.collections (id, observer, observed, workspace_name) VALUES ('ccccccccccccccccccccc', 'peer', 'peer', 'workspace')"
        )
        conn.exec_driver_sql(
            f"INSERT INTO {schema}.documents (id, content, observer, observed, workspace_name) VALUES ('ddddddddddddddddddddd', 'conclusion', 'peer', 'peer', 'workspace'), ('eeeeeeeeeeeeeeeeeeeee', 'parent', 'peer', 'peer', 'workspace')"
        )
        conn.exec_driver_sql(
            f"INSERT INTO {schema}.document_sources (derived_id, source_id, workspace_name) VALUES ('ddddddddddddddddddddd', '111111111111111111111', 'workspace'), ('ddddddddddddddddddddd', 'eeeeeeeeeeeeeeeeeeeee', 'workspace')"
        )
        conn.exec_driver_sql(
            f"INSERT INTO {schema}.conclusion_source_messages (conclusion_id, message_id, workspace_name) VALUES ('ddddddddddddddddddddd', '111111111111111111111', 'workspace')"
        )
        conn.exec_driver_sql(
            f"INSERT INTO {schema}.message_embeddings (content, message_id, workspace_name, session_name, peer_name) VALUES ('embedding text', '111111111111111111111', 'workspace', 'session', 'peer')"
        )
        before = conn.exec_driver_sql(
            "SELECT * FROM memory.messages ORDER BY message_id"
        ).all()
        conn.exec_driver_sql(f'SET ROLE "{role}"')

    # When: run the real Alembic revision under SELECT/INSERT/REFERENCES only.
    if mismatch:
        with (
            pytest.raises(RuntimeError, match="different content or agent"),
            conn.begin(),
        ):
            command.upgrade(config, "l13_message_split")
        with conn.begin():
            assert (
                conn.exec_driver_sql(
                    f"SELECT version_num FROM {schema}.alembic_version"
                ).scalar_one()
                == "c4d8e2f6a1b3"
            )
            assert not sa.inspect(conn).has_table(
                "message_meta", schema=settings.DB.SCHEMA
            )
            assert (
                conn.exec_driver_sql(
                    "SELECT * FROM memory.messages ORDER BY message_id"
                ).all()
                == before
            )
        return
    with conn.begin():
        command.upgrade(config, "l13_message_split")
    with conn.begin():
        command.upgrade(config, "l13_message_split")
        from alembic.migration import MigrationContext
        from alembic.operations import Operations

        from migrations.versions import l13_message_split

        with Operations.context(MigrationContext.configure(conn)):
            l13_message_split.upgrade()

    # Then: IDs, text, public references and metadata all survive, once.
    with conn.begin():
        assert (
            conn.exec_driver_sql(
                "SELECT * FROM memory.messages WHERE message_id <= 3 ORDER BY message_id"
            ).all()
            == before
        )
        rows = conn.exec_driver_sql(
            f"SELECT public_id, message_id, token_count, internal_metadata FROM {schema}.message_meta ORDER BY seq_in_session"
        ).all()
        assert [r.message_id for r in rows[:2]] == [2, 1]
        assert len({r.message_id for r in rows}) == 4
        assert all(
            r.token_count == 7 and r.internal_metadata == {"keep": True} for r in rows
        )
        assert conn.exec_driver_sql(
            "SELECT uid, source, agent_id FROM memory.messages WHERE message_id > 3 ORDER BY message_id"
        ).all() == [
            ("honcho:workspace:" + "3" * 21, "messages", "workspace"),
            ("honcho:workspace:" + "4" * 21, "agent", "workspace"),
        ]
        assert conn.exec_driver_sql(
            f"SELECT message_id FROM {schema}.queue ORDER BY id"
        ).scalars().all() == [r.message_id for r in rows]
        assert "content" not in {
            c["name"]
            for c in sa.inspect(conn).get_columns(
                "message_meta", schema=settings.DB.SCHEMA
            )
        }
        assert sa.inspect(conn).get_pk_constraint(
            "message_meta", schema=settings.DB.SCHEMA
        )["constrained_columns"] == ["message_id"]
        assert conn.exec_driver_sql(
            f"SELECT message_id, content FROM {schema}.message_embeddings"
        ).one() == ("1" * 21, "embedding text")
        assert (
            conn.exec_driver_sql(
                f"SELECT message_id FROM {schema}.conclusion_source_messages"
            ).scalar_one()
            == "1" * 21
        )
        conn.exec_driver_sql("RESET ROLE")
        conn.exec_driver_sql("DELETE FROM memory.messages WHERE message_id = 2")
        assert (
            conn.exec_driver_sql(
                f"SELECT count(*) FROM {schema}.message_meta WHERE message_id = 2"
            ).scalar_one()
            == 0
        )
        assert (
            conn.exec_driver_sql(
                f"SELECT count(*) FROM {schema}.queue WHERE message_id = 2"
            ).scalar_one()
            == 0
        )
        assert (
            conn.exec_driver_sql(
                f"SELECT count(*) FROM {schema}.message_embeddings"
            ).scalar_one()
            == 0
        )
        assert (
            conn.exec_driver_sql(
                f"SELECT count(*) FROM {schema}.conclusion_source_messages"
            ).scalar_one()
            == 0
        )
        assert conn.exec_driver_sql(
            f"SELECT source_id FROM {schema}.document_sources"
        ).scalars().all() == ["e" * 21]
