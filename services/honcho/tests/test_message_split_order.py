"""Message identities may reverse during migration; chronology must not."""

import hashlib
from contextlib import asynccontextmanager

import pytest
import sqlalchemy as sa
from alembic import command
from sqlalchemy.ext.asyncio import AsyncSession, create_async_engine

from src import models
from src.config import settings
from src.crud.message import get_messages, get_messages_id_range
from src.crud.session import clone_session
from src.deriver import queue_manager
from src.deriver.enqueue import construct_work_unit_key
from tests import test_message_split_migration as migration_tests
from tests.deriver import conftest as deriver_fixtures

migration_connection = migration_tests.migration_connection
create_queue_payload = deriver_fixtures.create_queue_payload


@pytest.fixture
def migrated_order(request):
    conn = request.getfixturevalue("migration_connection")
    config = migration_tests._config(conn)
    schema = conn.dialect.identifier_preparer.quote(settings.DB.SCHEMA)
    with conn.begin():
        command.upgrade(config, "l19_vec_embeddings")
    with conn.begin():
        conn.exec_driver_sql("RESET ROLE")
        conn.exec_driver_sql(
            f"INSERT INTO {schema}.workspaces (id,name) VALUES ('wwwwwwwwwwwwwwwwwwwww','order-workspace')"
        )
        conn.exec_driver_sql(
            f"INSERT INTO {schema}.peers (id,name,workspace_name) VALUES ('ppppppppppppppppppppp','peer','order-workspace')"
        )
        conn.exec_driver_sql(
            f"INSERT INTO {schema}.sessions (id,name,workspace_name) VALUES ('sssssssssssssssssssss','order-session','order-workspace')"
        )
        for seq, shared_id, content in (
            (1, 30, "first"),
            (2, 20, "second"),
            (3, 10, "newest"),
        ):
            conn.execute(
                sa.text(
                    "INSERT INTO memory.messages (message_id,uid,source,agent_id,session_id,content,content_sha256,ts) OVERRIDING SYSTEM VALUE VALUES (:id,:uid,'agent','order-workspace','order-session',:content,:digest,'2026-01-01Z')"
                ),
                {
                    "id": shared_id,
                    "uid": f"known-{seq}",
                    "content": content,
                    "digest": hashlib.sha256(content.encode()).hexdigest(),
                },
            )
            conn.execute(
                sa.text(
                    f"INSERT INTO {schema}.messages (id,public_id,workspace_name,session_name,peer_name,content,seq_in_session,token_count,created_at) OVERRIDING SYSTEM VALUE VALUES (:seq,:public,'order-workspace','order-session','peer',:content,:seq,5,'2026-01-01Z')"
                ),
                {"seq": seq, "public": str(seq) * 21, "content": content},
            )
        conn.exec_driver_sql(
            "SELECT setval(pg_get_serial_sequence('memory.messages','message_id'),30)"
        )
        command.upgrade(config, "l13_message_split")
    return conn.engine.url


@pytest.mark.parametrize("scenario", ["history", "tokens", "count", "range", "clone"])
async def test_migrated_ids_do_not_define_message_order(migrated_order, scenario):
    engine = create_async_engine(migrated_order)
    try:
        async with AsyncSession(engine, expire_on_commit=False) as db:
            match scenario:
                case "history":
                    rows = await db.scalars(
                        await get_messages("order-workspace", "order-session")
                    )
                    assert [m.content for m in rows] == ["first", "second", "newest"]
                case "tokens":
                    rows = await db.scalars(
                        await get_messages(
                            "order-workspace", "order-session", token_limit=5
                        )
                    )
                    assert [m.content for m in rows] == ["newest"]
                case "count":
                    rows = await db.scalars(
                        await get_messages(
                            "order-workspace", "order-session", message_count_limit=2
                        )
                    )
                    assert [m.content for m in rows] == ["second", "newest"]
                case "range":
                    rows = await get_messages_id_range(
                        db, "order-workspace", "order-session", start_id=30, end_id=10
                    )
                    assert [m.content for m in rows] == ["first", "second"]
                case "clone":
                    clone = await clone_session(
                        db,
                        "order-workspace",
                        "order-session",
                        cutoff_message_id="2" * 21,
                    )
                    rows = await db.scalars(
                        sa.select(models.Message)
                        .where(models.Message.session_name == clone.name)
                        .order_by(models.Message.seq_in_session)
                    )
                    assert [m.content for m in rows] == ["first", "second"]
    finally:
        await engine.dispose()


async def test_migrated_queue_batch_uses_sequence_boundaries(
    migrated_order, monkeypatch, request
):
    make_payload = request.getfixturevalue("create_queue_payload")
    engine = create_async_engine(migrated_order)
    try:
        async with AsyncSession(engine, expire_on_commit=False) as db:
            messages = list(
                await db.scalars(
                    sa.select(models.Message).order_by(models.Message.seq_in_session)
                )
            )
            items = []
            for message in messages:
                payload = make_payload(
                    message=message,
                    task_type="representation",
                    observed="peer",
                    observer="peer",
                )
                key = construct_work_unit_key("order-workspace", payload)
                item = models.QueueItem(
                    task_type="representation",
                    work_unit_key=key,
                    payload=payload,
                    workspace_name="order-workspace",
                    message_id=message.id,
                )
                db.add(item)
                items.append(item)
            active = models.ActiveQueueSession(work_unit_key=key)
            db.add(active)
            await db.commit()

            @asynccontextmanager
            async def tracked_db(_name):
                yield db

            monkeypatch.setattr(queue_manager, "tracked_db", tracked_db)
            resolve = queue_manager._resolve_batch_configuration

            def keep_first(batch):
                kept, config = resolve(batch)
                return kept[:1], config

            monkeypatch.setattr(
                queue_manager, "_resolve_batch_configuration", keep_first
            )
            batch = await queue_manager.QueueManager().get_queue_item_batch(
                "representation", key, active.id
            )
            assert [m.content for m in batch.messages_context] == ["first"]
            assert [item.message_id for item in batch.items_to_process] == [30]
    finally:
        await engine.dispose()
