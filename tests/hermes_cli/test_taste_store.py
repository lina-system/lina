"""The taste store contract as seen by ordinary agent connections."""

from datetime import date
from decimal import Decimal
from hashlib import sha256

import psycopg
import pytest

import hermes_state_store


@pytest.fixture
def agent():
    with psycopg.connect(hermes_state_store.resolve_dsn(), autocommit=True) as conn:
        conn.execute("SET lina.agent_id = 'alpha'")
        yield conn


def taste(conn, label="grid sorting"):
    return conn.execute(
        "INSERT INTO memory.tastes (agent_id, target_kind, label, valence, strength, status, origin) "
        "VALUES (public.lina_current_agent(), 'method', %s, 'like', 0.4, 'active', 'outcome') RETURNING id",
        (label,),
    ).fetchone()[0]


def message(conn, uid, source):
    conn.execute(
        "INSERT INTO memory.messages (agent_id, uid, source, content_sha256) "
        "VALUES (public.lina_current_agent(), %s, %s, 'synthetic')", (uid, source),
    )


def evidence(conn, taste_id, journal, turns):
    conn.execute(
        "INSERT INTO memory.taste_evidence "
        "(agent_id, taste_id, journal_uid, message_uids, observed_on, route, sign, weight, note) "
        "VALUES (public.lina_current_agent(), %s, %s, %s, '2026-01-01', 'outcome', 1, 0.15, 'fixture')",
        (taste_id, journal, turns),
    )


def event(conn, taste_id, reason):
    conn.execute(
        "INSERT INTO memory.taste_events (agent_id, taste_id, reason, changed_by, to_strength) "
        "VALUES (public.lina_current_agent(), %s, %s, 'review', 0.4)", (taste_id, reason),
    )


def test_round_trip_and_agent_visibility(agent):
    tid = taste(agent)
    for suffix in ("1", "2"):
        message(agent, "journal" + suffix, "heart")
        message(agent, "turn" + suffix, "agent")
        evidence(agent, tid, "journal" + suffix, ["turn" + suffix])
    reasons = ["created", "strength_changed", "status_changed"]
    for reason in reasons:
        event(agent, tid, reason)

    assert agent.execute(
        "SELECT id, target_kind, label, valence, strength, status, origin FROM memory.tastes"
    ).fetchall() == [(tid, "method", "grid sorting", "like", Decimal("0.4"), "active", "outcome")]
    assert agent.execute(
        "SELECT taste_id, journal_uid, message_uids, observed_on, route, sign, weight, note "
        "FROM memory.taste_evidence ORDER BY journal_uid"
    ).fetchall() == [
        (tid, "journal" + n, ["turn" + n], date(2026, 1, 1), "outcome", 1, Decimal("0.15"), "fixture")
        for n in ("1", "2")
    ]
    assert agent.execute(
        "SELECT taste_id, reason, changed_by, to_strength FROM memory.taste_events ORDER BY event_id"
    ).fetchall() == [(tid, reason, "review", Decimal("0.4")) for reason in reasons]
    with psycopg.connect(hermes_state_store.resolve_dsn(), autocommit=True) as other:
        other.execute("SET lina.agent_id = 'beta'")
        assert other.execute(
            "SELECT (SELECT count(*) FROM memory.tastes), (SELECT count(*) FROM memory.taste_evidence), "
            "(SELECT count(*) FROM memory.taste_events)"
        ).fetchone() == (0, 0, 0)
        assert taste(other) != tid


def test_history_is_append_only_with_fixed_reasons(agent):
    tid = taste(agent)
    event(agent, tid, "created")
    with pytest.raises(psycopg.errors.CheckViolation):
        event(agent, tid, "grid sorting")
    for statement in ("UPDATE memory.taste_events SET reason = 'status_changed'", "DELETE FROM memory.taste_events"):
        with pytest.raises(psycopg.errors.CheckViolation):
            agent.execute(statement)
    assert agent.execute("SELECT reason FROM memory.taste_events").fetchall() == [("created",)]


@pytest.mark.parametrize("field", ["journal", "turn"])
@pytest.mark.parametrize("invalid", ["missing", "forgotten", "foreign"])
def test_unusable_evidence_is_refused(agent, field, invalid):
    tid = taste(agent)
    message(agent, "validjournal", "heart")
    message(agent, "validturn", "agent")
    if invalid != "missing":
        if invalid == "foreign":
            agent.execute("SET lina.agent_id = 'beta'")
        message(agent, "invalid", "heart" if field == "journal" else "agent")
        if invalid == "forgotten":
            agent.execute("UPDATE memory.messages SET forgotten_at = now() WHERE uid = 'invalid'")
        agent.execute("SET lina.agent_id = 'alpha'")
    with pytest.raises(psycopg.errors.CheckViolation):
        evidence(agent, tid, "invalid" if field == "journal" else "validjournal",
                 ["validturn", "invalid"] if field == "turn" else ["validturn"])
    assert agent.execute("SELECT count(*) FROM memory.taste_evidence").fetchone() == (0,)


@pytest.mark.parametrize(("speaker", "ref", "role"), [
    ("agent", "agent:beta", "assistant"),
    ("agent", None, "assistant"),
    ("outside", "relay:guest", "user"),
])
def test_turn_evidence_checks_the_speaker_within_the_profile(agent, speaker, ref, role):
    tid = taste(agent)
    message(agent, "journal", "heart")
    agent.execute(
        "INSERT INTO memory.messages (agent_id, uid, source, speaker, speaker_ref, role, content_sha256) VALUES "
        "('alpha', 'own', 'agent', 'agent', 'agent:alpha', 'assistant', 'synthetic'), "
        "('alpha', 'foreign', 'agent', %s, %s, %s, 'synthetic')", (speaker, ref, role),
    )
    with pytest.raises(psycopg.errors.CheckViolation):
        evidence(agent, tid, "journal", ["own", "foreign"])
    evidence(agent, tid, "journal", ["own"])
    assert agent.execute("SELECT message_uids FROM memory.taste_evidence").fetchone() == (["own"],)


def test_journal_cannot_double_as_a_cited_turn(agent):
    tid = taste(agent)
    message(agent, "journal", "heart")
    with pytest.raises(psycopg.errors.CheckViolation):
        evidence(agent, tid, "journal", ["journal"])


def test_forget_scrubs_record_and_evidence_and_preserves_history(agent):
    tid = taste(agent)
    message(agent, "journal", "heart")
    evidence(agent, tid, "journal", [])
    event(agent, tid, "created")
    agent.execute("UPDATE memory.tastes SET status = 'forgotten', forgotten_by = 'owner' WHERE id = %s", (tid,))
    assert agent.execute(
        "SELECT id, target_kind, label_hash, forgotten_on = CURRENT_DATE, forgotten_by, label "
        "FROM memory.tastes"
    ).fetchone() == (tid, "method", sha256(b"grid sorting").digest(), True, "owner", None)
    assert agent.execute("SELECT count(*) FROM memory.taste_evidence").fetchone() == (0,)
    assert agent.execute("SELECT reason FROM memory.taste_events ORDER BY event_id").fetchall() == [
        ("created",), ("forgotten",),
    ]
    with pytest.raises(psycopg.IntegrityError):
        evidence(agent, tid, "journal", [])
    with pytest.raises(psycopg.IntegrityError):
        taste(agent)


@pytest.mark.parametrize("age,blocked", [(89, True), (90, False)])
def test_tombstone_cooldown_boundary(agent, age, blocked):
    # A dated tombstone, with no source label available anymore.
    agent.execute(
        "INSERT INTO memory.tastes (agent_id, target_kind, label_hash, status, first_seen, forgotten_on, forgotten_by) "
        "VALUES ('alpha', 'method', %s, 'forgotten', CURRENT_DATE - 100, CURRENT_DATE - %s::int, 'owner')",
        (sha256(b"grid sorting").digest(), age),
    )
    if blocked:
        with pytest.raises(psycopg.IntegrityError):
            taste(agent)
    else:
        taste(agent)
        assert agent.execute("SELECT status FROM memory.tastes ORDER BY id").fetchall() == [("forgotten",), ("active",)]


@pytest.mark.parametrize("has_evidence", [False, True])
def test_expire_candidate_keeps_its_immutable_history(agent, has_evidence):
    tid = agent.execute(
        "INSERT INTO memory.tastes (agent_id, target_kind, label, valence, strength, status, origin, first_seen) "
        "VALUES ('alpha', 'method', 'grid sorting', 'like', 0.15, 'candidate', 'outcome', CURRENT_DATE - 14) "
        "RETURNING id"
    ).fetchone()[0]
    for reason in ("created", "strength_changed"):
        event(agent, tid, reason)
    if has_evidence:
        message(agent, "journal", "heart")
        evidence(agent, tid, "journal", [])
        agent.execute("UPDATE memory.taste_evidence SET observed_on = CURRENT_DATE - 13")
        with pytest.raises(psycopg.errors.ForeignKeyViolation):
            agent.execute("DELETE FROM memory.tastes WHERE id = %s", (tid,))
        agent.execute("UPDATE memory.taste_evidence SET observed_on = CURRENT_DATE - 14")
    assert agent.execute(
        "DELETE FROM memory.tastes WHERE id = %s AND status = 'candidate' AND first_seen <= CURRENT_DATE - 14 "
        "RETURNING id", (tid,),
    ).fetchone() == (tid,)
    assert agent.execute("SELECT id FROM memory.tastes").fetchall() == []
    assert agent.execute("SELECT taste_id FROM memory.taste_evidence").fetchall() == []
    assert agent.execute("SELECT taste_id, reason FROM memory.taste_events ORDER BY event_id").fetchall() == [
        (tid, "created"), (tid, "strength_changed"),
    ]
    with pytest.raises(psycopg.errors.CheckViolation):
        agent.execute("DELETE FROM memory.taste_events WHERE taste_id = %s", (tid,))
    with pytest.raises(psycopg.errors.ForeignKeyViolation):
        event(agent, tid, "correction")
    assert taste(agent) != tid
