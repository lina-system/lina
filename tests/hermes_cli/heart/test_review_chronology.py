"""Calendar and journal ordering at the daily heart review's PostgreSQL boundary."""

from datetime import timedelta
from zoneinfo import ZoneInfo

import pytest

from hermes_cli.heart import refiner, rows
from hermes_cli.heart.models import Entry, Pref
from tests.hermes_cli.test_heart_journal import sandbox
from tests.hermes_cli.test_taste_refiner import DAY, daily, turn

__all__ = ["sandbox"]


def observation(uid, stamp, direction, route="outcome", citation="turn"):
    entry = Entry(ts=stamp, event="Synthetic task", note="Synthetic result", feeling="calm",
                  strength=3, evidence=(citation,),
                  pref=Pref(target="grid layout", kind="method", dir=direction, route=route))
    return rows.row(uid, entry, "Synthetic observation")


def test_new_taste_uses_profile_today_and_restores_writer_timezone(sandbox, memory_store_sql, monkeypatch):
    with rows.open_store(sandbox["alpha"]) as store:
        with store.write(), store._writer_cursor() as cur:
            cur.execute("SELECT CURRENT_TIMESTAMP, pg_backend_pid()")
            instant, writer = cur.fetchone()
            db_zone, profile_zone = "UTC", "Asia/Seoul"
            if instant.astimezone(ZoneInfo(db_zone)).date() == instant.astimezone(ZoneInfo(profile_zone)).date():
                # These two calendars stay a full day apart at every hour in CI.
                db_zone, profile_zone = "Etc/GMT+12", "Pacific/Tarawa"
            monkeypatch.setenv("HERMES_TIMEZONE", profile_zone)
            cur.execute("SELECT set_config('TimeZone', %s, false)", (db_zone,))
            cur.execute("SELECT CURRENT_DATE")
            today = instant.astimezone(ZoneInfo(profile_zone)).date()
            assert cur.fetchone() == (today - timedelta(days=1),)
            turn(memory_store_sql, day=today)
            rows.put(store, [observation("heart:alpha:sample", f"{today}T10:30:00Z", "like")])
            assert refiner.review(store, today)["written"] == 1

        with store.write(), store._writer_cursor() as cur:
            cur.execute("SELECT first_seen, current_setting('TimeZone'), pg_backend_pid() FROM memory.tastes")
            assert cur.fetchall() == [(today, db_zone, writer)]


@pytest.mark.parametrize("first_direction", ["dislike", "like"])
@pytest.mark.parametrize("first_uid,last_uid", [("a", "z"), ("z", "a")])
@pytest.mark.parametrize("tied", [False, True], ids=["timestamp", "message-id"])
def test_expiry_follows_first_journal(sandbox, memory_store_sql, first_direction, first_uid, last_uid, tied):
    turn(memory_store_sql)
    last_direction = "like" if first_direction == "dislike" else "dislike"
    first = observation(first_uid, f"{DAY}T11:00:00Z", first_direction)
    last = observation(last_uid, f"{DAY}T{11 if tied else 12}:00:00Z", last_direction)
    with rows.open_store(sandbox["alpha"]) as store:
        # Deliver the later journal first unless message order itself decides the tie.
        for item in ([first, last] if tied else [last, first]):
            rows.put(store, [item])
    assert "written=2" in daily(DAY)

    ending = DAY + timedelta(days=14)
    turn(memory_store_sql, "later", day=ending)
    with rows.open_store(sandbox["alpha"]) as store:
        rows.put(store, [observation("exposure", f"{ending}T12:00:00Z", "like", "exposure", "later")])
    assert "written=1" in daily(ending)
    expected = [] if first_direction == "dislike" else [("candidate",)]
    assert memory_store_sql("SELECT status FROM memory.tastes") == expected
