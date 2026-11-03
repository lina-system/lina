"""Synthetic pg_dump inputs shared by adapter tests and combined CLI QA."""

from __future__ import annotations

from contextlib import contextmanager
import hashlib
import os
from pathlib import Path
import subprocess
import uuid

import psycopg
from psycopg import sql
from psycopg.types.json import Jsonb

from hermes_cli.migrate_memory_honcho_stage import database_dsn, upgrade
from tests._fixtures.memory_store import IMAGE

HONCHO_PYTHON = os.environ.get("LINA_TEST_HONCHO_PYTHON")


def pg_command(tool: str) -> list[str]:
    from hermes_platform.resolver import locate_command
    command = locate_command(tool).command
    if command:
        return list(command)
    return ["docker", "run", "--rm", "-i", "--network", "host", IMAGE, tool]


def honcho_python() -> Path:
    """Use the supplied interpreter or Python on PATH (runner strips custom env)."""
    from hermes_platform.resolver import locate_command
    return Path(HONCHO_PYTHON or locate_command("python").command[0])


@contextmanager
def database(admin_dsn: str, template: str = "template0"):
    name = "honcho_test_" + uuid.uuid4().hex
    with psycopg.connect(database_dsn(admin_dsn, "postgres"), autocommit=True) as admin:
        admin.execute(sql.SQL("CREATE DATABASE {} TEMPLATE {}").format(
            sql.Identifier(name), sql.Identifier(template)))
        try:
            yield database_dsn(admin_dsn, name)
        finally:
            admin.execute(sql.SQL("DROP DATABASE {} WITH (FORCE)").format(sql.Identifier(name)))


def seed_workspace(conn, schema: str, workspace: str, prefix: str = "a") -> None:
    """Two placements share agent text, with every important reference shape."""
    ns = sql.Identifier(schema)
    for table, columns, values in (
        ("workspaces", "id,name", (prefix * 21, workspace)),
        ("peers", "id,name,workspace_name", ((prefix + "p") * 10 + "p", "user", workspace)),
        ("sessions", "id,name,workspace_name", ((prefix + "s") * 10 + "s", "session-a", workspace)),
        ("collections", "id,observer,observed,workspace_name",
         ((prefix + "c") * 10 + "c", "user", "user", workspace)),
    ):
        conn.execute(sql.SQL("INSERT INTO {}.{} (" + columns + ") VALUES (") .format(
            ns, sql.Identifier(table)) + sql.SQL(",").join(sql.Placeholder() for _ in values) + sql.SQL(")"), values)
    conn.execute(sql.SQL("INSERT INTO {}.session_peers (workspace_name,session_name,peer_name) "
                         "VALUES (%s,'session-a','user')").format(ns), (workspace,))
    conn.execute(sql.SQL("INSERT INTO {}.active_queue_sessions (id,work_unit_key) VALUES (%s,%s)").format(ns),
                 ((prefix + "l") * 10 + "l", f"representation:{workspace}:session-a:user"))
    mids = []
    for number, content in enumerate(("alpha hello", "alpha hello", "fresh conclusion evidence"), 1):
        public = (prefix + str(number)) * 10 + str(number)
        mid = conn.execute(sql.SQL("INSERT INTO {}.messages (public_id,session_name,peer_name,workspace_name,"
            "content,metadata,internal_metadata,token_count,seq_in_session,created_at) VALUES "
            "(%s,'session-a','user',%s,%s,'{{}}','{{}}',7,%s,to_timestamp(1767323045)) RETURNING id").format(ns),
            (public, workspace, content, number)).fetchone()[0]
        mids.append(mid)
        conn.execute(sql.SQL("INSERT INTO {}.queue (work_unit_key,task_type,payload,message_id,workspace_name) "
            "VALUES (%s,'representation',%s,%s,%s)").format(ns),
            (f"representation:{workspace}:session-a:user", Jsonb({"workspace_name": workspace}), mid, workspace))
    summaries = {label: {"message_id": mids[index], "content": "synthetic summary"}
                 for index, label in enumerate(("honcho_chat_summary_short", "honcho_chat_summary_long"))}
    conn.execute(sql.SQL("UPDATE {}.sessions SET internal_metadata=%s WHERE workspace_name=%s").format(ns),
                 (Jsonb({"summaries": summaries, "keep": True, "custom": {"message_id": 777}}), workspace))
    document = (prefix + "d") * 10 + "d"
    parent = (prefix + "e") * 10 + "e"
    dim = conn.execute("SELECT atttypmod FROM pg_attribute WHERE attrelid=%s::regclass "
                       "AND attname='embedding'", (f"{schema}.documents",)).fetchone()[0]
    vector = "[" + ",".join(["1"] + ["0"] * (dim - 1)) + "]"
    for did in (document, parent):
        conn.execute(sql.SQL("INSERT INTO {}.documents (id,content,observer,observed,workspace_name,embedding) "
                             "VALUES (%s,'synthetic document','user','user',%s,%s::vector)").format(ns),
                     (did, workspace, vector))
    public = (prefix + "1") * 10 + "1"
    for source in (public, parent):
        conn.execute(sql.SQL("INSERT INTO {}.document_sources (derived_id,source_id,workspace_name) "
                             "VALUES (%s,%s,%s)").format(ns), (document, source, workspace))
    conn.execute(sql.SQL("INSERT INTO {}.conclusion_source_messages (conclusion_id,message_id,workspace_name) "
                         "VALUES (%s,%s,%s)").format(ns), (document, public, workspace))
    conn.execute(sql.SQL("INSERT INTO {}.message_embeddings (content,message_id,workspace_name,session_name,"
                         "peer_name,embedding) VALUES ('chunk',%s,%s,'session-a','user',%s::vector)").format(ns),
                 (public, workspace, vector))


def create_dump(admin_dsn: str, destination: Path, work_dir: Path, *, workspace: str = "alpha",
                python: Path | None = None, dangling_source: bool = False) -> Path:
    """Produce a real pre-l19 logical archive without a live Honcho service."""
    python = python or honcho_python()
    work_dir.mkdir(parents=True, exist_ok=True)
    with database(admin_dsn) as dsn:
        upgrade(dsn, "public", "c4d8e2f6a1b3", python, work_dir)
        with psycopg.connect(dsn) as conn:
            seed_workspace(conn, "public", workspace)
            seed_workspace(conn, "public", "not-selected", "z")
            if dangling_source:
                conn.execute("UPDATE public.document_sources SET source_id=%s WHERE workspace_name=%s "
                             "AND source_id=%s", ("x" * 21, workspace, "a1" * 10 + "1"))
        with destination.open("wb") as archive:
            subprocess.run([*pg_command("pg_dump"), "--format=custom", "--no-owner", "--no-privileges", dsn],
                           stdout=archive, stderr=subprocess.PIPE, check=True, timeout=120)
    return destination


def seed_agent(conn) -> int:
    from plugins.memory.lina.identity import IdentityRegistry
    registry = IdentityRegistry()
    registry._conn = conn
    conversation = registry._resolve("conversation", [("gateway_chat", "alpha-copy:chat-a")])
    return conn.execute("INSERT INTO memory.messages (uid,source,agent_id,session_id,role,content,ts,conversation_id,"
                        "content_sha256) VALUES ('agent:alpha-copy:known','agent','alpha-copy','session-a',"
                        "'user','alpha hello',to_timestamp(1767323045),%s,%s) RETURNING message_id",
                        (conversation, hashlib.sha256(b"alpha hello").hexdigest())).fetchone()[0]
