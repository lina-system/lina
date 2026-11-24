"""Notice files replace owner-specific notification spawns (lina#36)."""

from datetime import datetime
from zoneinfo import ZoneInfo

import pytest

from tests.hermes_cli._work_env import WorkEnv


@pytest.mark.parametrize("check,expected", [("false", "bad"), ("true", "ok")])
def test_recurring_wait_exposes_check_and_releases_at_expiry(tmp_path, monkeypatch, check, expected):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    from hermes_cli.work import evaluate
    record = dict(id="G1", kind="목표", by="alpha", every="1h", check=check, state="open",
                  created=work.clock[0] - 60, wait=dict(on="확인 대기", next=work.clock[0] + 3600))
    held = evaluate.evaluate(record, {})
    assert held["status"] == "waiting"
    assert held["check"]["rc"] == (1 if check == "false" else 0)
    work.clock[0] += 3600
    # When
    released = evaluate.evaluate(record, {})
    # Then
    assert released["status"] == expected


def test_notification_is_throttled_when_check_repeats(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    work.goal_and_todos(check="evidence:결과 보고서")
    work.ok("evidence", "T1", "omo:OT-RESULT")
    work.ok("check", "T1", "--notify", "--quiet")
    before = work.notices("alpha")
    assert before
    # When
    work.ok("check", "T1", "--notify", "--quiet")
    # Then
    assert work.notices("alpha") == before


def test_dry_check_is_quiet_when_notification_requested(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    work.goal_and_todos(check="evidence:결과 보고서")
    work.ok("evidence", "T1", "omo:OT-RESULT")
    # When
    work.ok("check", "T1", "--notify", "--dry", "--quiet")
    # Then
    assert work.notices("alpha") == ""


@pytest.fixture
def morning(tmp_path, monkeypatch):
    work = WorkEnv(tmp_path, monkeypatch, extra="  timezone: America/New_York\n")
    work.clock[0] = datetime(2027, 1, 15, 9, tzinfo=ZoneInfo("America/New_York")).timestamp()
    work.goal_and_todos(todos=2)
    work.ok("edit", "T1", "--due", "2027-01-15")
    return work


def test_due_reminder_uses_owner_day_across_seoul_midnight(morning):
    for hours, expected in [(0, 1), (2, 1), (20, 2)]:
        morning.clock[0] += hours * 3600
        morning.ok("check", "T1", "--notify", "--quiet")
        assert morning.notices("alpha").count("(T1) 2027-01-15") == expected


def test_due_digest_omits_item_sent_by_partial_check(morning):
    morning.ok("edit", "G1", "--due", "2027-01-15")
    morning.ok("check", "T1", "--notify", "--quiet")
    before = morning.notices("alpha")
    assert "(T1) 2027-01-15" in before

    morning.ok("check", "--notify", "--quiet")
    digest = morning.notices("alpha")[len(before):]
    assert "(T1) 2027-01-15" not in digest
    assert digest.count("(G1) 2027-01-15") == 1


def test_due_edit_after_digest_notifies_once_that_morning(morning):
    morning.ok("check", "--notify", "--quiet")
    assert "(T1) 2027-01-15" in morning.notices("alpha")
    morning.clock[0] += 1800
    morning.ok("edit", "T2", "--due", "2027-01-15")

    morning.ok("check", "--notify", "--quiet")
    assert morning.notices("alpha").count("(T2) 2027-01-15") == 1
    morning.ok("check", "T2", "--notify", "--quiet")
    notices = morning.notices("alpha")
    assert notices.count("(T2) 2027-01-15") == 1
    assert notices.count("(T1) 2027-01-15") == 1
