"""Observable state transitions from checks, waits, and evidence (lina#36)."""

import json

import pytest

from tests.hermes_cli._work_env import WorkEnv


def test_passing_check_closes_todo_when_checked(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    work.goal_and_todos(check="true")
    # When
    work.ok("check", "T1", "--quiet")
    # Then
    row = next(row for row in work.json("--all")["items"] if row["id"] == "T1")
    assert row["state"] == "done"


def test_check_does_not_close_a_todo_whose_condition_changed_after_evaluation(tmp_path, monkeypatch):
    # Given a to-do whose check passes, and another writer that changes the check while it is evaluated
    from hermes_cli.work import ledger, state
    from hermes_cli.work.common import Lock
    work = WorkEnv(tmp_path, monkeypatch)
    flag = tmp_path / "ready"
    work.goal_and_todos(check=f"test -f {flag}")
    flag.write_text("", encoding="utf-8")
    evaluate_all = state._evaluate_all

    def edited_meanwhile(recs, ids, ctx, st):
        out = evaluate_all(recs, ids, ctx, st)
        with Lock():
            cur, _ = ledger.read_all()
            ledger.append(dict(cur["T1"], check="false"), "edit", "alpha")
        return out
    monkeypatch.setattr(state, "_evaluate_all", edited_meanwhile)
    # When
    work.ok("check", "T1", "--quiet")
    # Then the new revision is not closed with the old condition's result
    monkeypatch.setattr(state, "_evaluate_all", evaluate_all)
    row = next(row for row in work.json("--all")["items"] if row["id"] == "T1")
    assert (row["state"], row["check"]) == ("open", "false")


def test_wait_clear_recomputes_when_released(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    work.goal_and_todos()
    work.ok("wait", "T1", "--on", "확인 대기", "--next", "+1d")
    # When
    work.ok("wait", "T1", "--clear")
    # Then
    state = json.loads((work.work / "state.json").read_text(encoding="utf-8-sig"))
    assert state["items"]["T1"]["status"] != "waiting"


@pytest.mark.parametrize("command", ["touch result", "git commit", "work sprint set T1 --item x --state done", "echo x > result"])
def test_read_only_guard_refuses_mutating_checks(tmp_path, monkeypatch, command):
    # Given
    WorkEnv(tmp_path, monkeypatch)
    from hermes_cli.work.links import refuse_reason
    # When
    refusal = refuse_reason(command)
    # Then
    assert refusal


@pytest.mark.parametrize("command", ["printf ready", "true", "git status", "evidence:결과 보고서"])
def test_read_only_guard_accepts_observational_checks(tmp_path, monkeypatch, command):
    # Given
    WorkEnv(tmp_path, monkeypatch)
    from hermes_cli.work.links import refuse_reason
    # When
    refusal = refuse_reason(command)
    # Then
    assert refusal is None
