"""Shared text must follow the real message API transaction and identity."""

import hashlib

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import delete, func, select
from sqlalchemy.exc import IntegrityError
from sqlalchemy.ext.asyncio import AsyncSession

from src import models
from src.message_store import memory_messages


async def test_api_message_reads_shared_text(
    client: TestClient,
    db_session: AsyncSession,
    sample_data: tuple[models.Workspace, models.Peer],
):
    workspace, peer = sample_data
    session = models.Session(workspace_name=workspace.name, name="split-session")
    db_session.add(session)
    await db_session.commit()

    response = client.post(
        f"/v3/workspaces/{workspace.name}/sessions/{session.name}/messages",
        json={"messages": [{"content": "Synthetic shared text", "peer_id": peer.name}]},
    )

    assert response.status_code == 201
    public_id = response.json()[0]["id"]
    db_session.expunge_all()
    message = await db_session.scalar(
        select(models.Message).where(models.Message.public_id == public_id)
    )
    assert message is not None
    assert message.content == "Synthetic shared text"
    result = await db_session.execute(
        select(memory_messages).where(memory_messages.c.message_id == message.id)
    )
    row = result.mappings().one()
    assert row["uid"] == f"honcho:{workspace.name}:{public_id}"
    assert row["agent_id"] == workspace.name
    assert row["source"] == "agent"
    assert row["content_sha256"] == hashlib.sha256(b"Synthetic shared text").hexdigest()
    assert "content" not in models.Message.__table__.c


async def test_metadata_failure_rolls_back_shared_text(
    db_session: AsyncSession,
    sample_data: tuple[models.Workspace, models.Peer],
):
    workspace, peer = sample_data
    message = models.Message(
        workspace_name=workspace.name,
        peer_name=peer.name,
        session_name="missing-session",
        content="Rolled back synthetic text",
        seq_in_session=1,
    )
    db_session.add(message)

    with pytest.raises(IntegrityError):
        await db_session.flush()
    await db_session.rollback()

    count = await db_session.scalar(
        select(func.count())
        .select_from(memory_messages)
        .where(memory_messages.c.content == "Rolled back synthetic text")
    )
    assert count == 0


async def test_existing_uid_reuses_text_and_rejects_rewrite(
    db_session: AsyncSession,
    sample_data: tuple[models.Workspace, models.Peer],
):
    workspace, peer = sample_data
    session = models.Session(workspace_name=workspace.name, name="identity-session")
    db_session.add(session)
    await db_session.flush()
    shared_id = await db_session.scalar(
        memory_messages.insert()
        .values(
            uid="agent:synthetic:original",
            source="agent",
            agent_id=workspace.name,
            content="Original synthetic text",
            content_sha256=hashlib.sha256(b"Original synthetic text").hexdigest(),
        )
        .returning(memory_messages.c.message_id)
    )
    message = models.Message(
        workspace_name=workspace.name,
        peer_name=peer.name,
        session_name=session.name,
        content="Original synthetic text",
        seq_in_session=1,
        internal_metadata={"uid": "agent:synthetic:original"},
    )
    db_session.add(message)
    await db_session.commit()
    assert message.id == shared_id

    message.content = "A forbidden rewrite"
    with pytest.raises(ValueError, match="immutable"):
        await db_session.flush()
    await db_session.rollback()

    content = await db_session.scalar(
        select(memory_messages.c.content).where(
            memory_messages.c.message_id == shared_id
        )
    )
    assert content == "Original synthetic text"


async def test_shared_delete_cascades_and_detaches_document_sources(
    db_session: AsyncSession,
    sample_data: tuple[models.Workspace, models.Peer],
):
    workspace, peer = sample_data
    session = models.Session(workspace_name=workspace.name, name="cascade-session")
    collection = models.Collection(
        workspace_name=workspace.name, observer=peer.name, observed=peer.name
    )
    db_session.add_all([session, collection])
    await db_session.flush()
    message = models.Message(
        workspace_name=workspace.name,
        peer_name=peer.name,
        session_name=session.name,
        content="Synthetic cascade source",
        seq_in_session=1,
    )
    document = models.Document(
        workspace_name=workspace.name,
        observer=peer.name,
        observed=peer.name,
        session_name=session.name,
        content="Synthetic derived conclusion",
        level="explicit",
    )
    db_session.add_all([message, document])
    await db_session.flush()
    message_id, public_id, document_id = message.id, message.public_id, document.id
    db_session.add_all(
        [
            models.DocumentSource(
                derived_id=document_id,
                source_id=public_id,
                workspace_name=workspace.name,
            ),
            models.ConclusionSourceMessage(
                conclusion_id=document_id,
                message_id=public_id,
                workspace_name=workspace.name,
            ),
            models.QueueItem(
                session_id=session.id,
                workspace_name=workspace.name,
                message_id=message_id,
                work_unit_key="synthetic-cascade",
                task_type="representation",
                payload={},
            ),
        ]
    )
    await db_session.commit()

    await db_session.execute(
        delete(memory_messages).where(memory_messages.c.message_id == message_id)
    )
    await db_session.commit()
    db_session.expunge_all()

    assert await db_session.get(models.Message, message_id) is None
    assert await db_session.get(models.Document, document_id) is not None
    for model in (
        models.DocumentSource,
        models.ConclusionSourceMessage,
        models.QueueItem,
    ):
        assert await db_session.scalar(select(func.count()).select_from(model)) == 0
