"""Daily and weekly taste reviews through the heart CLI on a disposable store."""

from datetime import date, timedelta

import pytest

from hermes_cli.heart.models import Entry, Pref
from tests.hermes_cli.test_heart_journal import invoke, put_entry, sandbox

__all__ = ["sandbox"]
DAY = date(2025, 6, 1)


def turn(sql, uid="turn", source="agent", scope="agent", speaker="agent", ref="agent:alpha", day=DAY):
    sql("INSERT INTO memory.messages(uid,source,agent_id,session_id,role,scope,speaker,speaker_ref,ts,content_sha256) "
        "VALUES (%s,%s,'alpha','scene','assistant',%s,%s,%s,%s,'synthetic')",
        (uid, source, scope, speaker, ref, f"{day}T10:00:00+00:00"))


def journal(home, day, direction, refs=("turn",), target="grid layout"):
    put_entry(home, Entry(ts=f"{day}T12:00:00+00:00", event="Synthetic task", feeling="calm",
                          note="Synthetic result", evidence=refs, strength=3,
                          pref=Pref(target=target, kind="method", dir=direction, route="outcome")))


def daily(day):
    code, output, error = invoke("refine", "--date", day.isoformat())
    assert code == 0, error
    return output


def refusals(home, sql):
    turn(sql)
    journal(home, DAY, "like", ("turn", "unusable"))
    assert "citation" in daily(DAY)
    assert sql("SELECT count(*) FROM memory.tastes") == [(0,)]
    assert sql("SELECT count(*) FROM memory.taste_evidence") == [(0,)]


def test_relay_from_another_agent_is_refused(sandbox, memory_store_sql):
    turn(memory_store_sql, "unusable", ref="agent:beta")
    refusals(sandbox["alpha"], memory_store_sql)


def test_shared_external_message_is_refused(sandbox, memory_store_sql):
    turn(memory_store_sql, "unusable", source="messages", scope="agents")
    refusals(sandbox["alpha"], memory_store_sql)


def test_lcm_summary_is_refused(sandbox, memory_store_sql):
    # Compacted session rows can carry the agent's own speaker identity (0013_speaker.sql).
    turn(memory_store_sql, "unusable")
    memory_store_sql("""UPDATE memory.messages SET role='user', native='{"_compressed_summary":true}'
                        WHERE uid='unusable'""")
    refusals(sandbox["alpha"], memory_store_sql)


def test_turn_from_another_day_is_refused(sandbox, memory_store_sql):
    turn(memory_store_sql, "unusable", day=DAY - timedelta(days=1))
    refusals(sandbox["alpha"], memory_store_sql)


def test_signed_score_flip_and_rerun_persist_once(sandbox, memory_store_sql):
    sql = memory_store_sql
    for name in ("spacing", "columns", "tiles"):
        sql("INSERT INTO memory.tastes(agent_id,target_kind,label,status,origin,valence,strength,first_seen) "
            "VALUES ('alpha','method',%s,'active','temperament','like',0.3,%s)", (name, DAY))
    lookup = "SELECT status,valence,strength FROM memory.tastes WHERE label='grid layout'"
    for n, direction in enumerate(("dislike", "dislike", "like", "like")):
        day = DAY + timedelta(days=n)
        uid = f"turn-{n}"
        turn(sql, uid, day=day)
        journal(sandbox["alpha"], day, direction, (uid,))
        daily(day)
        status, valence, strength = sql(lookup)[0]
        if n == 0:
            assert (status, valence, float(strength)) == ("candidate", "dislike", 0.3)
        if n == 1:
            assert (status, valence) == ("active", "dislike")
            assert float(strength) == pytest.approx(0.445, abs=0.002)
    events = sql("SELECT reason,from_valence,to_valence FROM memory.taste_events ORDER BY event_id")
    assert valence == "like" and ("valence_changed", "dislike", "like") in events
    daily(day)
    assert sql("SELECT reason,from_valence,to_valence FROM memory.taste_events ORDER BY event_id") == events
    assert sql("SELECT count(*) FROM memory.taste_evidence") == [(4,)]


def test_only_weekly_review_promotes_a_continuously_active_taste(sandbox, tmp_path, memory_store_sql):
    sql = memory_store_sql
    home = sandbox["alpha"]
    (home / "heart").mkdir(exist_ok=True)
    for offset in (0, 1, 31):
        day = DAY + timedelta(days=offset)
        for n in range(4):
            uid = f"turn-{offset}-{n}"
            turn(sql, uid, day=day)
            journal(home, day, "like", (uid,))
        daily(day)
    lookup = "SELECT status FROM memory.tastes WHERE label='grid layout'"
    assert sql(lookup) == [("active",)]
    for _ in range(2):
        code, _, error = invoke("weekly", "--week-ending", day.isoformat(), "--out-dir", str(tmp_path / "weekly"))
        assert code == 0, error
    assert sql(lookup) == [("core",)]
    assert sql("SELECT count(*) FROM memory.taste_events WHERE reason='status_changed' AND to_status='core'") == [(1,)]
