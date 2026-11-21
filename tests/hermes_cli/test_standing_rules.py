"""Owner provenance, visibility and version history on the real store."""

import uuid
from concurrent.futures import ThreadPoolExecutor

import psycopg
import pytest


@pytest.fixture
def app(_memory_store):
    with psycopg.connect(_memory_store.app_dsn(), autocommit=True) as conn:
        conn.execute("SET lina.agent_id = 'alpha'")
        yield conn


@pytest.fixture
def turns(memory_store_sql):
    memory_store_sql("""
        INSERT INTO memory.messages
            (uid, source, agent_id, session_id, role, content, content_sha256, speaker, grade, ts)
        VALUES
            ('owner', 'agent', 'alpha', 'one', 'user', 'Use short lines', 'fixture', 'owner', NULL, '2025-01-01'),
            ('agent', 'agent', 'alpha', 'one', 'assistant', 'OK', 'fixture', 'agent', NULL, now()),
            ('outside', 'agent', 'beta', 'one', 'user', 'Different rule', 'fixture', 'owner', NULL, now()),
            ('lower', 'agent', 'alpha', 'one', 'user', 'Imported text', 'fixture', 'owner', 'external', now()),
            ('edit', 'agent', 'alpha', 'two', 'user', 'Use two lines', 'fixture', 'owner', NULL, '2025-01-02'),
            ('end', 'agent', 'alpha', 'two', 'user', 'Stop doing that', 'fixture', 'owner', NULL, '2025-01-03')
    """)
    return memory_store_sql("""
        INSERT INTO memory.message_parts (message_id, ordinal, agent_id, kind, content)
        SELECT message_id, 1, agent_id, 'reply_quote', 'Keep every line' FROM memory.messages WHERE uid = 'owner'
        RETURNING 'note:' || part_id
    """)[0][0]


def record(conn, rule, uid="owner", *, statement="Use short lines", scope="agent", target="agent:alpha",
           status="active"):
    return conn.execute(
        "SELECT memory.record_standing_rule(%s, %s, 'conduct', %s, %s, NULL, %s, %s)",
        (rule, uid, scope, target, statement, status),
    ).fetchone()[0]


def test_only_owner_turn_from_calling_agent_writes(app, turns):
    rule = uuid.uuid4()
    for uid in ("agent", turns, "outside", "lower"):
        with pytest.raises(psycopg.errors.CheckViolation):
            record(app, rule, uid)
    assert record(app, rule) == 1
    assert app.execute("SELECT source_uid, grade, recorded_by, said_at::date::text FROM memory.standing_rules"
                       ).fetchall() == [("owner", "owner", "agent:alpha", "2025-01-01")]


def test_own_and_shared_reads_with_function_only_writes(app, turns):
    own, shared, other = (uuid.uuid4() for _ in range(3))
    record(app, own)
    record(app, shared, scope="all", target="*")
    record(app, other, target="agent:beta")
    visible = "SELECT rule_id FROM memory.standing_rules"
    assert {row[0] for row in app.execute(visible)} == {own, shared}
    app.execute("SET lina.agent_id = 'beta'")
    assert {row[0] for row in app.execute(visible)} == {other, shared}
    with pytest.raises(psycopg.errors.InsufficientPrivilege):
        record(app, own, "outside", target="agent:beta")
    for sql in ("INSERT INTO memory.standing_rules DEFAULT VALUES",
                "UPDATE memory.standing_rules SET statement = 'changed'",
                "DELETE FROM memory.standing_rules"):
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            app.execute(sql)


def test_place_filter_keeps_match_and_rejects_external_citation(app, turns):
    rule = uuid.uuid4()
    query = """SELECT memory.record_standing_rule(%s, 'owner', 'filter', 'place', '*', 'channel:sample',
               'Hide duplicates', 'active', %s, '{"kind":"duplicate"}')"""
    with pytest.raises(psycopg.errors.CheckViolation):
        app.execute(query, (rule, ["lower"]))
    app.execute(query, (rule, ["agent"]))
    app.execute("SET lina.agent_id = 'beta'")
    assert app.execute("SELECT place_ref, cites, filter_match, filtered_count FROM memory.standing_rules").fetchall() == [
        ("channel:sample", ["agent"], {"kind": "duplicate"}, 0)]


def test_version_chain_is_atomic_and_immutable(app, turns, memory_store_sql):
    rule = uuid.uuid4()
    record(app, rule)
    with pytest.raises(psycopg.errors.CheckViolation):
        record(app, rule, "edit", statement="")
    assert app.execute("SELECT version, status FROM memory.standing_rules").fetchall() == [(1, "active")]
    for update in ("statement = 'changed'", "scope = 'all', agent_id = '*'", "source_uid = 'edit'"):
        with pytest.raises(psycopg.errors.CheckViolation):
            memory_store_sql(f"UPDATE memory.standing_rules SET {update} WHERE rule_id = %s", (rule,))
    assert record(app, rule, "edit", statement="Use two lines") == 2
    assert record(app, rule, "end", status="ended", statement="Stop doing that") == 3
    assert app.execute("SELECT version, status, replaced_by, replaced_at::date::text FROM memory.standing_rules "
                       "ORDER BY version").fetchall() == [
        (1, "replaced", 2, "2025-01-02"), (2, "replaced", 3, "2025-01-03"), (3, "ended", None, None)]
    with pytest.raises(psycopg.errors.CheckViolation):
        record(app, rule, status="forgotten")
    memory_store_sql("UPDATE memory.standing_rules SET forgotten_at = '2025-01-04' "
                     "WHERE rule_id = %s AND version = 3", (rule,))
    assert app.execute("SELECT rule_id, status, statement, grade, source_uid, forgotten_at::date::text, forgotten_by "
                       "FROM memory.standing_rules").fetchall() == [
        (rule, "forgotten", None, "owner", None, "2025-01-04", "owner")]
    with pytest.raises(psycopg.errors.CheckViolation):
        record(app, rule)


def test_simultaneous_first_versions_leave_one_active(app, turns, _memory_store):
    rule = uuid.uuid4()
    dsn = _memory_store.app_dsn()

    def write(uid):
        with psycopg.connect(dsn) as conn:
            conn.execute("SET lina.agent_id = 'alpha'")
            return record(conn, rule, uid)

    with ThreadPoolExecutor(max_workers=2) as pool:
        assert sorted(pool.map(write, ("owner", "edit"))) == [1, 2]
    assert app.execute("SELECT version, status FROM memory.standing_rules ORDER BY version").fetchall() == [
        (1, "replaced"), (2, "active")]
