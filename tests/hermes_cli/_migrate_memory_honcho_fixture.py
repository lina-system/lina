"""Synthetic pg_dump inputs shared by adapter tests and combined CLI QA."""

from __future__ import annotations

from contextlib import contextmanager
import hashlib
import os
from pathlib import Path
import subprocess
import uuid
from functools import lru_cache

import psycopg
import pytest
from psycopg import sql
from psycopg.types.json import Jsonb

from hermes_cli.migrate_memory_honcho_stage import database_dsn, upgrade as _upgrade
from tests._fixtures.memory_store import IMAGE

HONCHO_TEST_IMAGE = "lina-honcho-test:" + uuid.uuid4().hex


@pytest.fixture(scope="session", autouse=True)
def honcho_runtime():
    """Own one image for this test process and remove it after all databases close."""
    from hermes_cli import store

    previous = store.HONCHO_IMAGE
    try:
        honcho_python()
        yield
    finally:
        store.HONCHO_IMAGE = previous
        from hermes_platform.resolver import locate_command
        docker = locate_command("docker").command
        if docker:
            subprocess.run([*docker, "image", "rm", HONCHO_TEST_IMAGE], capture_output=True, check=False)


def upgrade(dsn, schema, revision, python, work_dir, **kwargs):
    """The fixture owns an explicit external cluster, not an installed store."""
    return _upgrade(dsn, schema, revision, python, work_dir, installed_store=False, **kwargs)

def pg_command(tool: str) -> list[str]:
    from hermes_platform.resolver import locate_command
    command = locate_command(tool).command
    if command:
        return list(command)
    return ["docker", "run", "--rm", "-i", "--network", "host", IMAGE, tool]


@lru_cache(maxsize=1)
def honcho_python() -> None:
    """Provision the installed image in the canonical runner's clean environment."""
    from hermes_platform.resolver import locate_command
    from hermes_cli import store
    from hermes_cli.store import HONCHO_DIR

    docker = locate_command("docker").command
    reason = "Honcho migration tests need a running Docker and the Honcho image"
    if docker and subprocess.run([*docker, "info"], capture_output=True, check=False).returncode == 0:
        # Cache mounts are an optimization, not an image input. The CI/local
        # legacy builder can build the identical runtime without BuildKit.
        recipe = (HONCHO_DIR / "Dockerfile").read_text().replace(
            "RUN --mount=type=cache,target=/root/.cache/uv \\\n", "RUN \\\n")
        built = subprocess.run([*docker, "build", "-t", HONCHO_TEST_IMAGE, "-f", "-", str(HONCHO_DIR)],
                               input=recipe, text=True, capture_output=True, check=False, timeout=1200)
        if built.returncode == 0:
            store.HONCHO_IMAGE = HONCHO_TEST_IMAGE
            return
        reason += ": " + (built.stdout + built.stderr)[-1500:]
    if os.environ.get("CI", "").lower() == "true":
        pytest.fail(reason)
    pytest.skip(reason)


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
    if conn.execute("SELECT to_regclass(%s)", (f"{schema}.conclusion_source_messages",)).fetchone()[0]:
        conn.execute(sql.SQL("INSERT INTO {}.conclusion_source_messages (conclusion_id,message_id,workspace_name) "
                             "VALUES (%s,%s,%s)").format(ns), (document, public, workspace))
    conn.execute(sql.SQL("INSERT INTO {}.message_embeddings (content,message_id,workspace_name,session_name,"
                         "peer_name,embedding) VALUES ('chunk',%s,%s,'session-a','user',%s::vector)").format(ns),
                 (public, workspace, vector))


def seed_shared_peers(conn, workspace: str) -> None:
    """Two agents share a person and an AI-to-AI session, not each other's recall."""
    for peer, key in (("source-ai", "i"), ("other-ai", "j")):
        conn.execute("INSERT INTO public.peers(id,name,workspace_name,metadata) VALUES (%s,%s,%s,%s)",
                     (key * 21, peer, workspace, Jsonb({"is_bot": True})))
    for name, key, participants in (("session-a", "a", ["source-ai"]),
                                   ("a2a", "b", ["source-ai", "other-ai"]),
                                   ("other-session", "c", ["other-ai", "user"])):
        if name != "session-a":
            conn.execute("INSERT INTO public.sessions(id,name,workspace_name) VALUES (%s,%s,%s)",
                         ((key + "s") * 10 + "s", name, workspace))
            conn.execute("INSERT INTO public.messages(public_id,session_name,peer_name,workspace_name,"
                         "content,token_count,seq_in_session) VALUES (%s,%s,'other-ai',%s,%s,3,1)",
                         ((key + "m") * 10 + "m", name, workspace, name + " evidence"))
        for peer in participants:
            conn.execute("INSERT INTO public.session_peers(workspace_name,session_name,peer_name) VALUES (%s,%s,%s)",
                         (workspace, name, peer))
    for index, (observer, observed) in enumerate((("source-ai", "user"), ("source-ai", "source-ai"),
                                               ("source-ai", "other-ai"), ("other-ai", "user"),
                                               ("other-ai", "other-ai"), ("user", "source-ai")), 1):
        conn.execute("INSERT INTO public.collections(id,observer,observed,workspace_name) VALUES (%s,%s,%s,%s)",
                     (f"c{index}" * 10 + "c", observer, observed, workspace))
        conn.execute("INSERT INTO public.documents(id,content,observer,observed,workspace_name) VALUES (%s,%s,%s,%s,%s)",
                     (f"d{index}" * 10 + "d", observer + " sees " + observed, observer, observed, workspace))
    # Existing linked documents are now in the selected AI's view of the person.
    conn.execute("UPDATE public.documents SET observer='source-ai' WHERE id=ANY(%s)",
                 (["ad" * 10 + "d", "ae" * 10 + "e"],))


def create_dump(admin_dsn: str, destination: Path, work_dir: Path, *, workspace: str = "alpha",
                python: Path | None = None, dangling_source: bool = False,
                shared_peers: bool = False) -> Path:
    """Produce a real pre-l19 logical archive without a live Honcho service."""
    python = python or honcho_python()
    work_dir.mkdir(parents=True, exist_ok=True)
    with database(admin_dsn) as dsn:
        upgrade(dsn, "public", "3e7a1c9d5b20", python, work_dir, dimensions=1536)
        with psycopg.connect(dsn) as conn:
            seed_workspace(conn, "public", workspace)
            seed_workspace(conn, "public", "not-selected", "z")
            if shared_peers:
                seed_shared_peers(conn, workspace)
            else:
                conn.execute("INSERT INTO public.peers(id,name,workspace_name,metadata) VALUES (%s,'source-ai',%s,%s)",
                             ("i" * 21, workspace, Jsonb({"is_bot": True})))
                conn.execute("INSERT INTO public.session_peers(workspace_name,session_name,peer_name) "
                             "VALUES (%s,'session-a','source-ai')", (workspace,))
                conn.execute("INSERT INTO public.collections(id,observer,observed,workspace_name) "
                             "VALUES (%s,'source-ai','user',%s)", ("v" * 21, workspace))
                conn.execute("UPDATE public.documents SET observer='source-ai' WHERE workspace_name=%s", (workspace,))
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
