"""Persisted summary boundaries survive shared IDs with reversed chronology."""

import hashlib
import json
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from unittest.mock import AsyncMock

import pytest
import sqlalchemy as sa
from alembic import command
from sqlalchemy.engine import Connection
from sqlalchemy.ext.asyncio import AsyncSession, create_async_engine

from src import schemas
from src.config import settings
from src.utils import summarizer
from tests import test_message_split_migration as migration_tests

migration_connection = migration_tests.migration_connection


def _summary(message_id: int, kind: summarizer.SummaryType) -> summarizer.Summary:
    return summarizer.Summary(
        content=f"covered {message_id}",
        message_id=message_id,
        message_public_id=str(message_id) * 21,
        summary_type=kind.value,
        created_at="2026-01-01T00:00:00Z",
        token_count=7,
    )


@pytest.fixture
def migrated_summaries(migration_connection: Connection) -> Connection:
    """Seed the real prehead with IDs that migrate into reverse order."""
    conn = migration_connection
    config = migration_tests._config(conn)
    ns = conn.dialect.identifier_preparer.quote(settings.DB.SCHEMA)
    with conn.begin():
        command.upgrade(config, "l19_vec_embeddings")
    with conn.begin():
        conn.exec_driver_sql(
            f"INSERT INTO {ns}.workspaces (id, name) VALUES ('{'w' * 21}', 'workspace')"
        )
        conn.exec_driver_sql(
            f"INSERT INTO {ns}.peers (id, name, workspace_name) VALUES ('{'p' * 21}', 'peer', 'workspace')"
        )
        metadata = {
            "keep": {"nested": [1, 2]},
            "summaries": {
                summarizer.SummaryType.SHORT.value: _summary(
                    1, summarizer.SummaryType.SHORT
                ),
                summarizer.SummaryType.LONG.value: _summary(
                    2, summarizer.SummaryType.LONG
                ),
                "unrelated": {"message_id": 1, "keep": True},
            },
        }
        conn.execute(
            sa.text(
                f"INSERT INTO {ns}.sessions (id, name, workspace_name, internal_metadata) VALUES (:id, 'session', 'workspace', CAST(:metadata AS jsonb))"
            ),
            {"id": "s" * 21, "metadata": json.dumps(metadata)},
        )
        for old_id, shared_id in enumerate([40, 30, 20, 10], 1):
            content = f"message-{old_id}"
            conn.execute(
                sa.text("""INSERT INTO memory.messages
                (message_id, uid, source, agent_id, session_id, content, ts, content_sha256)
                OVERRIDING SYSTEM VALUE VALUES (:id, :uid, 'agent', 'workspace', 'session', :content, '2026-01-01Z', :digest)"""),
                {
                    "id": shared_id,
                    "uid": content,
                    "content": content,
                    "digest": hashlib.sha256(content.encode()).hexdigest(),
                },
            )
            conn.execute(
                sa.text(f"""INSERT INTO {ns}.messages
                (id, public_id, session_name, content, metadata, internal_metadata,
                 token_count, seq_in_session, created_at, peer_name, workspace_name)
                OVERRIDING SYSTEM VALUE VALUES (:id, :public, 'session', :content,
                CAST(:metadata AS jsonb), '{{}}', 7, :id, '2026-01-01Z', 'peer', 'workspace')"""),
                {
                    "id": old_id,
                    "public": str(old_id) * 21,
                    "content": content,
                    "metadata": json.dumps({"uid": content}),
                },
            )
    return conn


def test_migration_remaps_both_summary_boundaries(
    migrated_summaries: Connection,
) -> None:
    # Given: two persisted summary entries and unrelated metadata.
    conn = migrated_summaries
    ns = conn.dialect.identifier_preparer.quote(settings.DB.SCHEMA)
    # When: the actual revision runs inside a transaction.
    with conn.begin():
        command.upgrade(migration_tests._config(conn), "l13_message_split")
        metadata = conn.exec_driver_sql(
            f"SELECT internal_metadata FROM {ns}.sessions WHERE name = 'session'"
        ).scalar_one()
        # Then: both boundaries remap, preserving every other field.
        assert metadata == {
            "keep": {"nested": [1, 2]},
            "summaries": {
                summarizer.SummaryType.SHORT.value: {
                    **_summary(1, summarizer.SummaryType.SHORT),
                    "message_id": 40,
                },
                summarizer.SummaryType.LONG.value: {
                    **_summary(2, summarizer.SummaryType.LONG),
                    "message_id": 30,
                },
                "unrelated": {"message_id": 1, "keep": True},
            },
        }


@pytest.fixture
async def summary_db(migrated_summaries: Connection) -> AsyncIterator[AsyncSession]:
    conn = migrated_summaries
    with conn.begin():
        command.upgrade(migration_tests._config(conn), "l13_message_split")
    engine = create_async_engine(conn.engine.url)
    try:
        async with AsyncSession(engine, expire_on_commit=False) as db:
            yield db
    finally:
        await engine.dispose()


@pytest.mark.parametrize(
    "kind,expected",
    [
        (summarizer.SummaryType.SHORT, ["2" * 21, "3" * 21]),
        (summarizer.SummaryType.LONG, ["3" * 21]),
    ],
)
async def test_migrated_summary_returns_exact_tail(
    summary_db: AsyncSession, kind: summarizer.SummaryType, expected: list[str]
) -> None:
    # Given: reversed shared IDs and a cutoff at sequence four.
    # When: retrieve history and structured context using each summary boundary.
    history = await summarizer.get_summarized_history(
        summary_db, "workspace", "session", cutoff=10, summary_type=kind
    )
    summary, messages = await summarizer.get_session_context(
        summary_db, "workspace", "session", 1000, cutoff=10
    )
    # Then: only the exact unsummarized tail appears, never the boundary/cutoff.
    assert history.split("<recent_messages>\n")[1].split("</recent_messages>")[
        0
    ].strip() == (
        summarizer._format_messages(
            [
                message
                for message in await summarizer.crud.get_messages_by_seq_range(
                    summary_db, "workspace", "session", start_seq=1, end_seq=4
                )
                if message.public_id in expected
            ]
        )
    )
    assert summary is not None and summary.message_id == 40
    assert [message.public_id for message in messages] == ["2" * 21, "3" * 21]


async def test_summary_save_is_monotonic_by_sequence(summary_db: AsyncSession) -> None:
    # Given: the stored boundary is sequence one, shared ID 40.
    # When: sequence three (ID 20) finishes before sequence two (ID 30).
    await summarizer._save_summary(
        summary_db, _summary(20, summarizer.SummaryType.SHORT), "workspace", "session"
    )
    await summarizer._save_summary(
        summary_db, _summary(30, summarizer.SummaryType.SHORT), "workspace", "session"
    )
    # Then: the late older result cannot regress the persisted boundary.
    stored = await summarizer.get_summary(summary_db, "workspace", "session")
    assert stored is not None and stored["message_id"] == 20
    metadata = await summary_db.scalar(
        sa.select(summarizer.models.Session.internal_metadata)
    )
    assert metadata["keep"] == {"nested": [1, 2]}
    assert metadata["summaries"][summarizer.SummaryType.LONG.value]["message_id"] == 30


async def test_summary_generation_uses_sequence_not_id(
    summary_db: AsyncSession, monkeypatch: pytest.MonkeyPatch
) -> None:
    # Given: a prior summary with a numerically larger ID than the new message.
    @asynccontextmanager
    async def tracked_db(*args: str) -> AsyncIterator[AsyncSession]:
        yield summary_db

    monkeypatch.setattr(summarizer, "tracked_db", tracked_db)
    created = AsyncMock(
        return_value=(_summary(20, summarizer.SummaryType.SHORT), True, 0, 0)
    )
    monkeypatch.setattr(summarizer, "_create_summary", created)
    configuration = schemas.ResolvedConfiguration(
        reasoning=schemas.ResolvedReasoningConfiguration(enabled=True),
        peer_card=schemas.ResolvedPeerCardConfiguration(use=False, create=False),
        summary=schemas.ResolvedSummaryConfiguration(
            enabled=True, messages_per_short_summary=2, messages_per_long_summary=4
        ),
        dream=schemas.ResolvedDreamConfiguration(enabled=False),
    )
    # When: process a chronologically newer message.
    await summarizer._create_and_save_summary(
        "workspace",
        "session",
        message_id=20,
        message_seq_in_session=3,
        message_public_id="3" * 21,
        summary_type=summarizer.SummaryType.SHORT,
        configuration=configuration,
    )
    # Then: generation proceeds, but already-covered work is skipped.
    assert created.await_count == 1
    await summarizer._create_and_save_summary(
        "workspace",
        "session",
        message_id=40,
        message_seq_in_session=1,
        message_public_id="1" * 21,
        summary_type=summarizer.SummaryType.SHORT,
        configuration=configuration,
    )
    assert created.await_count == 1
