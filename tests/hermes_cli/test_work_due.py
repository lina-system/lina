"""Due date persistence, hints, and reminders (lina#36)."""

import pytest

import datetime

from tests.hermes_cli._work_env import WorkEnv


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    env.goal_and_todos()
    return env


@pytest.mark.parametrize("item", ["G1", "T1"])
def test_due_roundtrips_when_set(work, item):
    # Given / When
    work.ok("edit", item, "--due", "2027-01-20")
    # Then
    row = next(row for row in work.json()["items"] if row["id"] == item)
    assert row["due"] == "2027-01-20"


def test_due_is_removed_when_cleared(work):
    # Given
    work.ok("edit", "T1", "--due", "2027-01-20")
    # When
    work.ok("edit", "T1", "--due", "")
    # Then
    assert "due" not in next(row for row in work.json()["items"] if row["id"] == "T1")


@pytest.mark.parametrize("date", ["11-05", "2027-02-30", "다음 금요일"])
def test_invalid_due_is_refused_when_editing(work, date):
    # Given / When
    code, _, error = work.run("edit", "T1", "--due", date)
    # Then
    assert code == 2
    assert "--due" in error


@pytest.mark.parametrize("args", [
    ["edit", "T1", "--title", "11월 5일 보고서 정리"],
    ["edit", "T1", "--why", "마감 전에 결과가 보이게"],
    ["note", "T1", "이번 주 안에 정리한다"],
])
def test_date_hint_names_due_edit_when_date_is_mentioned(work, args):
    # Given / When
    output = work.ok(*args)
    # Then
    assert output.splitlines()[-1] == "날짜가 보인다. 마감일이면: work edit T1 --due YYYY-MM-DD"


def test_date_hint_is_suppressed_when_due_exists(work):
    # Given
    work.ok("edit", "T1", "--due", "2027-01-20")
    # When
    output = work.ok("note", "T1", "금요일까지 정리한다")
    # Then
    assert "--due YYYY-MM-DD" not in output


def test_overdue_open_item_notifies_when_checked(work):
    # Given
    work.ok("edit", "T1", "--due", "2020-01-01")
    # When
    work.ok("check", "T1", "--notify", "--quiet")
    # Then
    notice = work.notices("alpha")
    assert "2020-01-01" in notice
    assert "T1" in notice


def test_due_today_follows_configured_timezone_not_host_timezone(work):
    # Given
    work.config(extra="  timezone: Asia/Seoul\n")
    work.clock[0] = datetime.datetime(2027, 1, 15, 8, tzinfo=datetime.timezone(datetime.timedelta(hours=9))).timestamp()
    work.ok("edit", "T1", "--due", "2027-01-15")
    # When
    work.ok("check", "T1", "--notify", "--quiet")
    # Then
    notice = work.notices("alpha")
    assert "오늘 마감" in notice
    assert "2027-01-15" in notice
