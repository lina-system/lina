"""Daily and weekly taste reviews through the heart CLI on a disposable store."""

import time
from datetime import UTC, date, datetime, timedelta
from zoneinfo import ZoneInfo

import pytest

import hermes_time
from hermes_cli.heart import storage
from hermes_cli.heart.models import Change, Entry, Pref
from tests.hermes_cli.test_heart_journal import database, invoke, put_entry, sandbox

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


def test_weekly_rechecks_decayed_strength_after_missed_reviews(sandbox, tmp_path, memory_store_sql):
    home = sandbox["alpha"]
    (home / "heart").mkdir()
    sql = memory_store_sql
    for offset, count in ((0, 1), (1, 3)):
        day = DAY + timedelta(days=offset)
        for index in range(count):
            uid = f"past-{offset}-{index}"
            turn(sql, uid, day=day)
            journal(home, day, "like", (uid,))
        daily(day)
    assert sql("SELECT status,strength::float FROM memory.tastes") == [("active", 0.8)]
    ending = day + timedelta(days=30)
    code, _, error = invoke("weekly", "--week-ending", ending.isoformat(), "--out-dir", str(tmp_path / "weekly"))
    assert code == 0, error
    assert sql("SELECT status FROM memory.tastes") == [("active",)]
    assert sql("SELECT count(*) FROM memory.taste_events WHERE to_status='core'") == [(0,)]


@pytest.mark.parametrize(("command", "flag"), [("refine", "--date"), ("weekly", "--week-ending")])
def test_future_review_refuses_without_writes(sandbox, tmp_path, memory_store_sql, monkeypatch, command, flag):
    # UTC has reached June 2, but June 2 is still tomorrow in the invoking profile.
    monkeypatch.setenv("HERMES_TIMEZONE", "America/Los_Angeles")
    monkeypatch.setattr(storage, "now", lambda: datetime(2025, 6, 2, 1, tzinfo=UTC).timestamp())
    sql = memory_store_sql
    home = sandbox["alpha"]
    (home / "heart").mkdir()
    tomorrow = DAY + timedelta(days=1)
    turn(sql, day=tomorrow)
    journal(home, tomorrow, "like")
    sql("INSERT INTO memory.tastes(agent_id,target_kind,label,status,origin,valence,strength,first_seen) "
        "VALUES ('alpha','method','spacing','active','outcome','like',0.8,%s)", (DAY - timedelta(days=31),))

    def snapshot():
        return [sql(f"SELECT * FROM memory.{table} ORDER BY 1,2")
                for table in ("tastes", "taste_evidence", "taste_events")]

    before = snapshot()
    flags = ("--out-dir", str(tmp_path / "weekly")) if command == "weekly" else ()
    code, _, error = invoke(command, flag, tomorrow.isoformat(), *flags)
    assert snapshot() == before
    assert code == 2 and "future" in error.lower() and tomorrow.isoformat() in error
    assert invoke(command, flag, DAY.isoformat(), *flags)[0] == 0


@pytest.fixture
def la_evening(sandbox, monkeypatch):
    instant = datetime(2025, 6, 2, 1, tzinfo=UTC)
    try:
        with monkeypatch.context() as patch:
            patch.setenv("TZ", "UTC")
            time.tzset()
            patch.setenv("HERMES_TIMEZONE", "America/Los_Angeles")
            hermes_time.reset_cache()
            assert hermes_time.get_timezone() == ZoneInfo("America/Los_Angeles")
            assert instant.astimezone().utcoffset() == timedelta(0)
            patch.setattr(storage, "now", instant.timestamp)
            yield instant
    finally:
        time.tzset()
        hermes_time.reset_cache()


@pytest.mark.platforms("posix")
def test_refine_reads_evening_journal_and_citation_on_profile_day(sandbox, la_evening, memory_store_sql):
    home = sandbox["alpha"]
    [uid] = database(home, [("owner", "assistant", "Synthetic turn", la_evening.timestamp() - 60)])
    put_entry(home, Entry(ts=la_evening.isoformat(), event="Synthetic task", feeling="calm", note="Synthetic",
                          evidence=(uid,), strength=3,
                          pref=Pref(target="spacing", kind="method", dir="like", route="outcome")))
    assert invoke("refine")[0] == 0
    assert memory_store_sql("SELECT observed_on,message_uids FROM memory.taste_evidence") == [(DAY, [uid])]
    assert memory_store_sql("SELECT changed_at FROM memory.taste_events WHERE reason='created'") == [
        (datetime(2025, 6, 1, 7, tzinfo=UTC),)]


@pytest.mark.platforms("posix")
def test_refine_ages_active_status_from_profile_day(la_evening, memory_store_sql):
    sql = memory_store_sql
    [(tid,)] = sql("INSERT INTO memory.tastes(agent_id,target_kind,label,status,origin,valence,strength,first_seen)"
                   " VALUES ('alpha','method','spacing','active','outcome','like',0.4,'2025-05-31') RETURNING id")
    # The previous status change was on May 31 in Los Angeles.
    sql("INSERT INTO memory.taste_events(agent_id,taste_id,changed_at,reason,changed_by,from_status,to_status)"
        " VALUES ('alpha',%s,'2025-06-01T01:00:00Z','status_changed','review','candidate','active')", (tid,))
    assert invoke("refine")[0] == 0
    assert sql("SELECT status FROM memory.tastes") == [("dormant",)]


@pytest.mark.platforms("posix")
def test_weekly_includes_profile_evening_until_local_midnight(sandbox, la_evening, tmp_path):
    changes = sandbox["alpha"] / "heart" / "changes.jsonl"
    storage.append(changes, Change(ts=la_evening.isoformat(), added=("Sunday evening",)))
    storage.append(changes, Change(ts="2025-06-02T07:00:00Z", added=("Monday midnight",)))
    assert invoke("weekly", "--week-ending", "2025-06-01", "--out-dir", str(tmp_path))[0] == 0
    page = (tmp_path / "250601.md").read_text()
    assert "Sunday evening" in page
    assert "Monday midnight" not in page


@pytest.mark.parametrize(("caller", "target", "activated", "status", "journal_count", "promoted_at"), [
    pytest.param("UTC", "Asia/Seoul", "2025-05-03T00:00:00+09:00", "active", 3,
                 "2025-06-01T15:00:00Z", id="utc-to-seoul"),
    pytest.param("Asia/Seoul", "America/Los_Angeles", "2025-05-02T23:30:00-07:00", "core", 4,
                 "2025-06-01T07:00:00Z", id="seoul-to-los-angeles"),
])
def test_weekly_reviews_each_profiles_local_week(sandbox, monkeypatch, tmp_path, memory_store_sql,
                                                caller, target, activated, status, journal_count, promoted_at):
    home = sandbox["alpha"]
    (home / "config.yaml").write_text(f"timezone: {target}\n")
    monkeypatch.delenv("HERMES_TIMEZONE", raising=False)
    assert hermes_time.get_timezone() == ZoneInfo(target)
    sql = memory_store_sql
    [(tid,)] = sql("INSERT INTO memory.tastes(agent_id,target_kind,label,status,origin,valence,strength,first_seen)"
                   " VALUES ('alpha','method','grid layout','active','outcome','like',0.8,'2025-05-01') RETURNING id")
    sql("INSERT INTO memory.taste_events(agent_id,taste_id,changed_at,reason,changed_by,from_status,to_status)"
        " VALUES ('alpha',%s,%s,'status_changed','review','candidate','active')", (tid, activated))
    for n in range(3):
        uid = f"turn-{n}"
        turn(sql, uid)
        journal(home, DAY, "like", (uid,))
    daily(DAY)
    stamp = "2025-06-01T16:00:00Z"  # Monday in Seoul, Sunday in Los Angeles.
    put_entry(home, Entry(ts=stamp, event="Synthetic", feeling="calm", note="Boundary", evidence=(), strength=1))
    storage.append(home / "heart/changes.jsonl", Change(ts=stamp, added=("Boundary change",)))

    monkeypatch.setenv("HERMES_HOME", str(sandbox["beta"]))
    monkeypatch.setenv("HERMES_TIMEZONE", caller)
    code, _, error = invoke("weekly", "--week-ending", "2025-06-01", "--out-dir", str(tmp_path))
    assert code == 0, error
    assert sql("SELECT status FROM memory.tastes WHERE id=%s", (tid,)) == [(status,)]
    page = (tmp_path / "250601.md").read_text()
    assert f"Journal: {journal_count} lines" in page
    assert ("Boundary change" in page) == (journal_count == 4)
    assert storage.home() == sandbox["beta"] and hermes_time.get_timezone() == ZoneInfo(caller)
    if status == "active":
        assert invoke("weekly", "--week-ending", "2025-06-02", "--out-dir", str(tmp_path))[0] == 0
    assert sql("SELECT changed_at FROM memory.taste_events WHERE taste_id=%s AND reason='status_changed'"
               " AND to_status='core'", (tid,)) == [(datetime.fromisoformat(promoted_at),)]
