"""Comments, decisions, and owner completion contracts (lina#36)."""

import pytest

from tests.hermes_cli._work_env import NOW, WorkEnv


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    env.goal_and_todos(todos=2)
    yield env
    from hermes_cli.work import ledger
    ledger.close()


def test_comments_number_and_notify_when_owner_posts(work):
    # Given / When
    first = work.ok("comment", "T1", "첫 의견")
    second = work.ok("comment", "T1", "둘째 의견")
    # Then
    assert (first.strip(), second.strip()) == ("c1", "c2")
    notice = work.notices("alpha")
    assert "T1 c1" in notice and "T1 c2" in notice
    assert "work reply T1 c1" in notice


@pytest.mark.parametrize("result,status", [("반영함", "resolved"), ("반영 안 함", "resolved"), ("답변 필요", "open")])
def test_reply_updates_comment_when_result_is_given(work, result, status):
    # Given
    work.ok("comment", "T1", "색을 바꿔")
    # When
    work.ok("reply", "T1", "c1", "색을 확인했다", "--result", result)
    # Then
    from hermes_cli.work import ledger
    row = ledger.read_all()[0]["T1"]
    assert row["comments"][0]["status"] == status
    assert row["comments"][0]["replies"][0]["result"] == result


def test_answer_needed_stores_decision_when_reply_asks_question(work):
    # Given
    work.ok("comment", "T1", "색을 바꿔")
    # When
    work.ok("reply", "T1", "c1", "파랑으로 할까?", "--result", "답변 필요")
    # Then
    from hermes_cli.work import ledger
    row = ledger.read_all()[0]["T1"]
    assert (row["decide"], row["decide_cid"]) == ("파랑으로 할까?", "c1")


def test_goal_refuses_completion_when_children_are_open(work):
    # Given / When
    code, _, error = work.run("owner-done", "G1")
    # Then
    assert code == 2
    assert "--with-open" in error
    assert "T1" in error and "T2" in error


def test_goal_undo_reopens_only_children_closed_together(work):
    # Given
    work.ok("owner-done", "T1")
    work.ok("owner-done", "G1", "--with-open", "--via", "board")
    # When
    work.clock[0] = NOW + 600
    work.ok("owner-undo", "G1")
    # Then
    from hermes_cli.work import ledger
    rows = ledger.read_all()[0]
    assert [rows[item]["state"] for item in ("G1", "T1", "T2")] == ["open", "done", "open"]


def test_owner_undo_refuses_when_ten_minutes_have_elapsed(work):
    # Given
    work.ok("owner-done", "T1")
    work.clock[0] = NOW + 601
    # When
    code, _, _ = work.run("owner-undo", "T1")
    # Then
    assert code == 2


def test_decision_choices_clear_when_decision_is_removed(work):
    # Given
    work.ok("wait", "T1", "--decide", "색을 고를까?", "--choice", "회색", "--choice", "파랑", "--recommend", "회색")
    # When
    work.ok("wait", "T1", "--decide", "")
    # Then
    from hermes_cli.work import ledger
    row = ledger.read_all()[0]["T1"]
    assert row["decide"] == ""
    assert "decide_choices" not in row and "decide_recommend" not in row


def test_resolving_reply_clears_linked_decision(work):
    # Given
    work.ok("comment", "T1", "색을 바꿔")
    work.ok("reply", "T1", "c1", "파랑으로 할까?", "--result", "답변 필요")
    # When
    work.ok("reply", "T1", "c1", "파랑으로 고쳤다", "--result", "반영함")
    # Then
    from hermes_cli.work import ledger
    row = ledger.read_all()[0]["T1"]
    assert row["decide"] == ""
    assert row["comments"][0]["status"] == "resolved"
