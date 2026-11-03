"""The configured owner's own to-dos (lina#36)."""

import pytest

from tests.hermes_cli._work_env import WorkEnv


@pytest.fixture
def work(tmp_path, monkeypatch):
    return WorkEnv(tmp_path, monkeypatch)


def test_owner_todo_roundtrips_when_requested_by_agent(work):
    # Given / When
    item = work.ok("add", "할 일", "--by", "owner", "--from", "alpha", "--title", "백업 설정 확인",
                   "--why", "매일 백업을 확인할 수 있게", "--done", "설정에서 켜져 있다",
                   "--due", "2027-01-22").splitlines()[0]
    # Then
    row = next(row for row in work.json()["items"] if row["id"] == item)
    assert (row["by"], row["from"], row["due"]) == ("owner", "alpha", "2027-01-22")


@pytest.mark.parametrize("flag,value", [("--session", "topic"), ("--omo", "next"), ("--every", "1d")])
def test_owner_todo_refuses_agent_only_fields(work, flag, value):
    # Given / When
    code, _, _ = work.run("add", "할 일", "--by", "owner", "--from", "alpha", "--title", "백업 설정 확인",
                          "--why", "매일 백업을 확인할 수 있게", "--done", "설정에서 켜져 있다", flag, value)
    # Then
    assert code == 2


def test_owner_comment_notifies_requester_when_posted(work):
    # Given
    work.ok("add", "할 일", "--by", "owner", "--from", "alpha", "--title", "백업 설정 확인",
            "--why", "매일 백업을 확인할 수 있게", "--done", "설정에서 켜져 있다")
    # When
    work.ok("comment", "T1", "설정을 확인했다")
    # Then
    assert "T1 c1" in work.notices("alpha")


def test_owner_completion_releases_waiting_agent_item(work):
    # Given
    work.goal_and_todos()
    work.ok("add", "할 일", "--by", "owner", "--from", "alpha", "--title", "백업 설정 확인",
            "--why", "매일 백업을 확인할 수 있게", "--done", "설정에서 켜져 있다")
    work.ok("wait", "T1", "--on-item", "T2")
    # When
    work.ok("owner-done", "T2")
    # Then
    from hermes_cli.work import ledger
    rows = ledger.read_all()[0]
    ledger.close()
    assert rows["T1"]["wait"] == {}
    assert rows["T2"]["state"] == "done"
    assert "T1" in work.notices("alpha") or "기기 1 점검" in work.notices("alpha")


def test_owner_undo_restores_waiting_dependency(work):
    # Given
    work.goal_and_todos()
    work.ok("add", "할 일", "--by", "owner", "--from", "alpha", "--title", "백업 설정 확인",
            "--why", "매일 백업을 확인할 수 있게", "--done", "설정에서 켜져 있다")
    work.ok("wait", "T1", "--on-item", "T2")
    work.ok("owner-done", "T2")
    # When
    work.ok("owner-undo", "T2")
    # Then
    from hermes_cli.work import ledger
    rows = ledger.read_all()[0]
    ledger.close()
    assert rows["T1"]["wait"]["item"] == "T2"
    assert rows["T2"]["state"] == "open"


def test_owner_transfer_preserves_requester_when_reassigned(work):
    # Given
    work.goal_and_todos()
    # When
    work.ok("edit", "T1", "--owner", "owner")
    # Then
    row = next(row for row in work.json()["items"] if row["id"] == "T1")
    assert (row["by"], row["from"], row["session"]) == ("owner", "alpha", "")
