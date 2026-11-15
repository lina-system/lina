"""lina#442: migration 0014_brain applies on an empty store and on one that already holds memory rows, follows the
agent_id row-level security rules of docs/store.md, and adds the shared page vectors to vec. lina#444: migration
0019_brain_proposals reshapes brain.proposals, keeps older rows and scopes the cycle tables per agent.

These run on the real Postgres from tests/_fixtures/memory_store.py, each in a database of its own.
"""

from __future__ import annotations

import hashlib
import shutil
import uuid

import psycopg
import pytest

from hermes_cli.store_migrations import MIGRATIONS_DIR, apply, migration_files
from tests._fixtures.memory_store import _as_app, _initdb_sql, _with_db

BRAIN = "0014_brain.sql"
PROPOSALS = "0019_brain_proposals.sql"
BRAIN_TABLES = {"roots", "pages", "aliases", "edges", "timeline", "atoms", "merges", "proposals",
                "dream_cycles", "dream_pages_seen", "dream_inputs", "cycle_lock", "search_docs", "search_terms"}
VECTOR = "[" + ",".join(["1"] + ["0"] * 1023) + "]"


@pytest.fixture
def fresh_db(_memory_store):
    """(owner DSN, lina_app DSN) of a database with only services/store/initdb applied, dropped after the test."""
    owner = _memory_store.superuser_dsn()
    name = f"lina_brain_{uuid.uuid4().hex[:10]}"
    with psycopg.connect(_with_db(owner, "postgres"), autocommit=True) as admin:
        admin.execute(f"CREATE DATABASE {name}")
    dsn = _with_db(owner, name)
    with psycopg.connect(dsn, autocommit=True) as conn:
        conn.execute(_initdb_sql())
    yield dsn, _as_app(owner, name)
    with psycopg.connect(_with_db(owner, "postgres"), autocommit=True) as admin:
        admin.execute(f"DROP DATABASE IF EXISTS {name} WITH (FORCE)")


def _runner(conn):
    def run(sql: str) -> str:
        cur = conn.execute(sql)
        return "\n".join("|".join(str(v) for v in row) for row in cur.fetchall()) if cur.description else ""
    return run


def _brain_tables(conn) -> set[str]:
    return {row[0] for row in conn.execute("SELECT tablename FROM pg_tables WHERE schemaname = 'brain'")}


def _before(tmp_path, name: str, *, including: bool = False):
    """A migrations directory holding the files before *name* (and *name* itself when *including*)."""
    earlier = tmp_path / "earlier"
    earlier.mkdir()
    for path in migration_files():
        if path.name < name or (including and path.name == name):
            shutil.copy(path, earlier / path.name)
    return earlier


def test_it_applies_on_an_empty_store_and_its_body_runs_again_without_error(fresh_db, tmp_path):
    with psycopg.connect(fresh_db[0], autocommit=True) as conn:
        assert BRAIN in apply(_runner(conn), out=lambda _line: None, directory=_before(tmp_path, BRAIN,
                                                                                         including=True))
        conn.execute((MIGRATIONS_DIR / BRAIN).read_text(encoding="utf-8"))

        assert PROPOSALS in apply(_runner(conn), out=lambda _line: None)
        assert _brain_tables(conn) == BRAIN_TABLES
        assert apply(_runner(conn), out=lambda _line: None) == []
        conn.execute((MIGRATIONS_DIR / PROPOSALS).read_text(encoding="utf-8"))
        assert _brain_tables(conn) == BRAIN_TABLES


def test_it_applies_on_a_store_that_holds_memory_rows_and_keeps_them(fresh_db, tmp_path):
    with psycopg.connect(fresh_db[0], autocommit=True) as conn:
        assert BRAIN not in apply(_runner(conn), out=lambda _line: None, directory=_before(tmp_path, BRAIN))
        conn.execute("INSERT INTO memory.messages (uid, source, agent_id, role, content, content_sha256) "
                     "VALUES ('agent:alpha:1', 'agent', 'alpha', 'user', 'hello', %s)",
                     (hashlib.sha256(b"hello").hexdigest(),))
        conn.execute("INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding) "
                     "VALUES ('chunk', 1, 'alpha', 'm', 1024, %s)", (VECTOR,))
        before = conn.execute("SELECT message_id, uid, content FROM memory.messages").fetchall()

        assert BRAIN in apply(_runner(conn), out=lambda _line: None)

        assert conn.execute("SELECT message_id, uid, content FROM memory.messages").fetchall() == before
        assert conn.execute("SELECT kind, agent_id FROM vec.embeddings").fetchall() == [("chunk", "alpha")]
        assert "pages" in _brain_tables(conn)


def test_proposals_are_agent_scoped_pages_are_shared_and_page_vectors_are_read_only(fresh_db):
    owner, app = fresh_db
    with psycopg.connect(owner, autocommit=True) as conn:
        apply(_runner(conn), out=lambda _line: None)
        root = conn.execute("INSERT INTO brain.roots (path, mapping) VALUES ('/wiki', '{}') RETURNING root_id"
                            ).fetchone()[0]
        conn.execute("INSERT INTO brain.pages (slug, page_id, root_id, path, type, title, compiled_truth, "
                     "content_sha256) VALUES ('미나', 7, %s, 'people/미나.md', 'person', '미나', '', 'x')", (root,))
        conn.execute("INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding) "
                     "VALUES ('page', 7, '*', 'm', 1024, %s)", (VECTOR,))
        conn.execute("INSERT INTO brain.proposals (agent_id, phase, kind, target) "
                     "VALUES ('beta', 'consolidate', 'merge', '미나')")
        with pytest.raises(psycopg.errors.CheckViolation):
            conn.execute("INSERT INTO brain.pages (slug, page_id, root_id, path, type, title, compiled_truth, "
                         "content_sha256, frontmatter) VALUES ('x', 8, %s, 'x.md', 'concept', 'x', '', 'x', "
                         "'{\"status\": \"open\"}')", (root,))

    with psycopg.connect(app, autocommit=True) as conn:
        conn.execute("SELECT set_config('lina.agent_id', 'alpha', false)")
        assert conn.execute("SELECT slug FROM brain.pages").fetchall() == [("미나",)]
        assert conn.execute("SELECT kind, item_id FROM vec.embeddings").fetchall() == [("page", 7)]
        assert conn.execute("SELECT agent_id FROM brain.proposals").fetchall() == []
        conn.execute("INSERT INTO brain.proposals (agent_id, phase, kind) VALUES ('alpha', 'drift', 'timeline')")
        assert conn.execute("SELECT agent_id FROM brain.proposals").fetchall() == [("alpha",)]
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            conn.execute("INSERT INTO brain.pages (slug, page_id, root_id, path, type, title, compiled_truth, "
                         "content_sha256) VALUES ('y', 9, %s, 'y.md', 'idea', 'y', '', 'y')", (root,))
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            conn.execute("INSERT INTO brain.proposals (agent_id, phase, kind) VALUES ('beta', 'drift', 'timeline')")

    with psycopg.connect(owner, autocommit=True) as conn:
        def page_vectors() -> int:
            return conn.execute("SELECT count(*) FROM vec.embeddings WHERE kind = 'page'").fetchone()[0]
        conn.execute("UPDATE brain.pages SET title = 'Mina', content_sha256 = 'x' WHERE slug = '미나'")
        assert page_vectors() == 1
        conn.execute("UPDATE brain.pages SET content_sha256 = 'y' WHERE slug = '미나'")
        assert page_vectors() == 0
        conn.execute("INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding) "
                     "VALUES ('page', 7, '*', 'm', 1024, %s)", (VECTOR,))
        conn.execute("DELETE FROM brain.pages WHERE slug = '미나'")
        assert page_vectors() == 0


def test_0019_keeps_older_proposals_and_scopes_new_tables_per_agent(fresh_db, tmp_path):
    owner, app = fresh_db
    with psycopg.connect(owner, autocommit=True) as conn:
        apply(_runner(conn), out=lambda _line: None, directory=_before(tmp_path, PROPOSALS))
        conn.execute("INSERT INTO brain.proposals (agent_id, kind, page_slug) VALUES ('alpha', 'merge', 'mina')")

        assert PROPOSALS in apply(_runner(conn), out=lambda _line: None)
        conn.execute((MIGRATIONS_DIR / PROPOSALS).read_text(encoding="utf-8"))

        assert conn.execute("SELECT agent_id, phase, kind, target, status, grade FROM brain.proposals").fetchall() == [
            ("alpha", "merge", "merge", "mina", "proposed", "external")]
        conn.execute("INSERT INTO brain.proposals (agent_id, phase, kind, status) VALUES ('beta', 'drift', 'x', "
                     "'rejected')")
        with pytest.raises(psycopg.errors.CheckViolation):
            conn.execute("INSERT INTO brain.proposals (agent_id, phase, kind, status) VALUES ('beta', 'x', 'x', 'open')")
        for agent in ("alpha", "beta"):
            conn.execute("INSERT INTO brain.dream_cycles (agent_id, finished_at) VALUES (%s, now())", (agent,))

    with psycopg.connect(app, autocommit=True) as conn:
        conn.execute("SELECT set_config('lina.agent_id', 'beta', false)")
        assert conn.execute("SELECT agent_id, phase, rejected, acceptance_rate FROM brain.proposal_acceptance"
                            ).fetchall() == [("beta", "drift", 1, 0)]
        assert conn.execute("SELECT agent_id FROM brain.dream_metrics").fetchall() == [("beta",)]
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            conn.execute("INSERT INTO brain.dream_cycles (agent_id) VALUES ('beta')")
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            conn.execute("SELECT * FROM brain.cycle_lock")
