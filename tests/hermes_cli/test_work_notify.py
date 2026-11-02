"""Notice files replace owner-specific notification spawns (lina#36)."""

from tests.hermes_cli._work_env import WorkEnv

import pytest


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
