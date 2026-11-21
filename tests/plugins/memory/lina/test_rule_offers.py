"""One-shot proposals and owner consent across real, RLS-scoped store connections."""

from uuid import UUID, uuid4

import psycopg
import pytest
from psycopg.types.json import Jsonb

STATEMENT = "Always use short reports"
ROOM = "conversation"


@pytest.fixture(name="rule_offers")
def offer_api():
    from plugins.memory.lina import rule_offers

    return rule_offers


@pytest.fixture
def app(_memory_store):
    with psycopg.connect(_memory_store.app_dsn(), autocommit=True) as conn:
        conn.execute("SET lina.agent_id = 'alpha'")
        yield conn


@pytest.fixture
def turn(memory_store_sql):
    def put(uid, words, *, agent="alpha", session=ROOM, role="user", speaker="owner",
            grade=None, source="agent", date="2025-04-01", seq=None):
        memory_store_sql(
            """INSERT INTO memory.messages
               (uid, source, agent_id, session_id, role, speaker, grade, content, ts, seq_in_session, content_sha256)
               VALUES (%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,'fixture')""",
            (uid, source, agent, session, role, speaker, grade, words, date, seq),
        )
        return uid
    return put


@pytest.fixture
def journal(memory_store_sql):
    def put(uid, note, evidence, agent="alpha"):
        memory_store_sql(
            """INSERT INTO memory.messages (uid, source, agent_id, native, content, content_sha256)
               VALUES (%s,'heart',%s,%s,%s,'fixture')""",
            (uid, agent, Jsonb({"note": note, "evidence": evidence}), note),
        )
        return uid
    return put


def local_rule(app, uid, *, target, statement=STATEMENT, rule=None):
    rid = rule or uuid4()
    app.execute(
        "SELECT memory.record_standing_rule(%s,%s,'conduct','agent',%s,NULL,%s)",
        (rid, uid, "agent:" + target, statement),
    )
    return rid


@pytest.fixture
def pair(app, turn, rule_offers):
    alpha = local_rule(app, turn("owner-alpha", STATEMENT), target="alpha")
    assert rule_offers.shared_offer(app, alpha, ROOM) is None
    app.execute("SET lina.agent_id = 'beta'")
    beta = local_rule(app, turn("owner-beta", STATEMENT, agent="beta"), target="beta")
    app.execute("SET lina.agent_id = 'alpha'")
    return alpha, beta


@pytest.fixture
def pattern(app, turn, journal, rule_offers):
    turn("basis", "This was easier to read.")
    journal("pattern", STATEMENT, ["basis"])
    offer = rule_offers.next_pattern_offer(app, ROOM)
    assert offer and STATEMENT in offer["question"]
    return offer


def display(turn, offer):
    turn("shown", "알겠습니다.\n" + offer["question"], role="assistant", speaker="agent", seq=2)


def test_shared_yes_replaces_only_offered_versions_and_preserves_private_history(app, pair, turn, rule_offers):
    alpha, beta = pair
    offer = rule_offers.shared_offer(app, alpha, ROOM)
    assert offer and STATEMENT in offer["question"]
    app.execute("SET lina.agent_id = 'beta'")
    assert rule_offers.shared_offer(app, beta, ROOM) is None
    app.execute("SET lina.agent_id = 'gamma'")
    later = local_rule(app, turn("owner-gamma", STATEMENT, agent="gamma"), target="gamma")
    app.execute("SET lina.agent_id = 'alpha'")
    display(turn, offer)
    uid = turn("answer", "네.", date="2025-04-03", seq=3)
    receipt = rule_offers.answer_offer(app, offer["offer_id"], uid, ROOM)
    assert receipt["status"] == "recorded"
    shared = UUID(receipt["rule_id"])
    assert rule_offers.answer_offer(app, offer["offer_id"], uid, ROOM) == receipt
    rows = """SELECT rule_id, status, source_uid, said_at::date::text, shared_rule_id,
                     replaced_by, replaced_at::date::text FROM memory.standing_rules"""
    common = (shared, "active", uid, "2025-04-03", None, None, None)
    assert set(app.execute(rows)) == {common, (alpha, "replaced", "owner-alpha", "2025-04-01", shared, 1, "2025-04-03")}
    app.execute("SET lina.agent_id = 'beta'")
    assert set(app.execute(rows)) == {common, (beta, "replaced", "owner-beta", "2025-04-01", shared, 1, "2025-04-03")}
    app.execute("SET lina.agent_id = 'gamma'")
    assert set(app.execute(rows)) == {common, (later, "active", "owner-gamma", "2025-04-01", None, None, None)}


@pytest.mark.parametrize("answer,expected", [(None, "ignored"), ("아니요.", "declined"), ("maybe", "ignored")])
def test_unaccepted_shared_offer_changes_neither_rule_nor_future_question(app, pair, turn, answer, expected, rule_offers):
    offer = rule_offers.shared_offer(app, pair[0], ROOM)
    assert offer
    display(turn, offer)
    uid = turn("answer", answer) if answer else "absent"
    assert rule_offers.answer_offer(app, offer["offer_id"], uid, ROOM) == {"status": expected}
    turn("later", "yes")
    assert rule_offers.answer_offer(app, offer["offer_id"], uid, ROOM) == {"status": "ignored"}
    assert rule_offers.shared_offer(app, pair[0], ROOM) is None
    for profile, original in zip(("alpha", "beta"), pair, strict=True):
        app.execute("SELECT set_config('lina.agent_id', %s, false)", (profile,))
        assert app.execute("SELECT rule_id,status FROM memory.standing_rules").fetchall() == [(original, "active")]


@pytest.mark.parametrize("changed", ["anchor", "peer", "peer-grade"])
def test_shared_stale_snapshot_cannot_replace_current_rules(app, pair, turn, memory_store_sql, changed, rule_offers):
    offer = rule_offers.shared_offer(app, pair[0], ROOM)
    assert offer
    if changed == "peer-grade":
        memory_store_sql("UPDATE memory.messages SET grade = 'external' WHERE uid = 'owner-beta'")
    else:
        profile, rid = ("alpha", pair[0]) if changed == "anchor" else ("beta", pair[1])
        app.execute("SELECT set_config('lina.agent_id', %s, false)", (profile,))
        local_rule(app, turn("edit", "Use two lines", agent=profile, session="other"),
                   target=profile, statement="Use two lines", rule=rid)
        app.execute("SET lina.agent_id = 'alpha'")
    display(turn, offer)
    uid = turn("answer", "yes")
    assert rule_offers.answer_offer(app, offer["offer_id"], uid, ROOM) == {"status": "stale"}
    assert set(memory_store_sql("SELECT rule_id FROM memory.standing_rules WHERE status = 'active'")) == {
        (pair[0],), (pair[1],)}


def test_pattern_yes_cites_evidence_and_uses_the_dated_owner_answer(app, pattern, turn, rule_offers):
    assert app.execute("SELECT 1 FROM memory.standing_rules").fetchall() == []
    display(turn, pattern)
    uid = turn("answer", "yes", date="2025-04-04", seq=3)
    assert rule_offers.answer_offer(app, pattern["offer_id"], uid, ROOM)["status"] == "recorded"
    assert app.execute(
        "SELECT scope, agent_id, statement, source_uid, said_at::date::text, cites, grade FROM memory.standing_rules"
    ).fetchall() == [("agent", "agent:alpha", STATEMENT, uid, "2025-04-04", ["pattern", "basis"], "owner")]
    with pytest.raises(psycopg.errors.InsufficientPrivilege):
        app.execute("SELECT members FROM memory.rule_offers")
    with pytest.raises(psycopg.errors.InsufficientPrivilege):
        app.execute("UPDATE memory.rule_offers SET decision = 'declined'")
    app.execute("SET lina.agent_id = 'beta'")
    assert app.execute("SELECT offer_id, pattern_uid FROM memory.rule_offers").fetchall() == []
    assert rule_offers.answer_offer(app, pattern["offer_id"], uid, ROOM) == {"status": "ignored"}
    assert app.execute("SELECT 1 FROM memory.standing_rules").fetchall() == []


@pytest.mark.parametrize("case", ["different-question", "ambiguous", "agent", "outside", "tool", "external-grade",
                                  "external-source", "quote", "forgotten", "superseded", "old-sequence",
                                  "backdated", "wrong-session", "wrong-uid"])
def test_exact_question_and_current_direct_owner_reply_are_required(app, pattern, turn, memory_store_sql, case, rule_offers):
    question = pattern["question"]
    if case == "different-question":
        question = question.replace(STATEMENT, "Use two lines")
    if case == "ambiguous":
        question = "Delete the report?\n" + question
    turn("shown", question, role="assistant", speaker="agent", seq=2)
    role, speaker = {"agent": ("assistant", "agent"), "outside": ("user", "outside"),
                     "tool": ("tool", "outside")}.get(case, ("user", "owner"))
    uid = turn("answer", "yes", role=role, speaker=speaker, grade="external" if case == "external-grade" else None,
               seq=1 if case == "old-sequence" else 3, session="other" if case == "wrong-session" else ROOM,
               source="messages" if case == "external-source" else "agent",
               date="2025-03-31" if case == "backdated" else "2025-04-01")
    if case == "quote":
        memory_store_sql(
            """INSERT INTO memory.message_parts (message_id, ordinal, agent_id, kind, content)
               SELECT message_id, 1, agent_id, 'reply_quote', 'yes' FROM memory.messages WHERE uid = 'answer'"""
        )
    if case == "forgotten":
        memory_store_sql("UPDATE memory.messages SET forgotten_at = now() WHERE uid = 'answer'")
    if case == "superseded":
        turn("newer", "Let's discuss something else")
    assert rule_offers.answer_offer(app, pattern["offer_id"], "basis" if case == "wrong-uid" else uid, ROOM) == {
        "status": "ignored"}
    assert app.execute("SELECT 1 FROM memory.standing_rules").fetchall() == []


@pytest.mark.parametrize("changed", ["grade", "statement", "evidence"])
def test_pattern_rechecks_its_snapshot_after_issuance(app, pattern, turn, memory_store_sql, changed, rule_offers):
    display(turn, pattern)
    if changed == "grade":
        memory_store_sql("UPDATE memory.messages SET grade = 'external' WHERE uid = 'basis'")
    else:
        turn("different", "I prefer this.", session="other")
        updated = {"note": "Use two lines", "evidence": ["basis"]} if changed == "statement" else {
            "note": STATEMENT, "evidence": ["different"]}
        memory_store_sql("UPDATE memory.messages SET native = %s WHERE uid = 'pattern'", (Jsonb(updated),))
    uid = turn("answer", "yes")
    assert rule_offers.answer_offer(app, pattern["offer_id"], "basis", ROOM) == {"status": "ignored"}
    assert rule_offers.answer_offer(app, pattern["offer_id"], uid, ROOM) == {"status": "stale"}
    assert rule_offers.answer_offer(app, pattern["offer_id"], uid, ROOM) == {"status": "stale"}
    assert app.execute("SELECT 1 FROM memory.standing_rules").fetchall() == []


def test_store_selects_next_eligible_unoffered_local_journal(app, journal, turn, rule_offers):
    turn("basis", "That was clear")
    journal("older", "Use two lines", ["basis"])
    journal("newer", STATEMENT, ["basis"])
    journal("unsafe", "Publish the password", ["basis"])
    journal("unknown", "Use short replies", ["missing"])
    journal("invented", "Keep replies short", [])
    turn("foreign", "Yes", agent="beta")
    journal("other", "Use three lines", ["foreign"], agent="beta")
    journal("foreign-evidence", "Use plain language", ["foreign"])
    turn("untrusted-source", "Yes", source="messages")
    journal("external-evidence", "Use three lines", ["untrusted-source"])
    first = rule_offers.next_pattern_offer(app, ROOM)
    assert first and STATEMENT in first["question"]
    assert rule_offers.next_pattern_offer(app, ROOM) is None
    display(turn, first)
    uid = turn("decline", "No.")
    assert rule_offers.answer_offer(app, first["offer_id"], uid, ROOM) == {"status": "declined"}
    turn("next", "Any other suggestions?")
    second = rule_offers.next_pattern_offer(app, ROOM)
    assert second and second["offer_id"] != first["offer_id"] and "Use two lines" in second["question"]
    assert app.execute("SELECT 1 FROM memory.standing_rules").fetchall() == []
