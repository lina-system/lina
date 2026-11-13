"""lina#667: a PR wait is met by what actually runs on the PR's head.

Required checks that never post on the head (a review bot's status, a linked-issue check, a base-branch
green mark) used to leave the wait ``missing`` for good, even after every check that does run had
finished and the PR had merged. A repository with no required checks woke the card on the first tick,
whatever was running. A wait that stays unmet for hours went on standing without a trace.

``collect_acceptance`` reads a fake GitHub; the wake runs through the real tick on a temporary board.
"""
from __future__ import annotations

from pathlib import Path

import pytest

from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli import kanban_db_dispatch as kbd
from hermes_cli import kanban_db_wait as wait
from hermes_cli import kanban_pr_acceptance

HEAD = "b" * 40
PR = {"kind": "pr", "repo": "o/r", "number": 7}


def _github(monkeypatch, *, required, runs, state="OPEN"):
    """One PR whose base branch requires ``required`` contexts; ``runs`` are ``(name, status)`` on its head."""
    def api(endpoint, *, query=None, paginate=False, profile_home=None):
        if endpoint == "graphql":
            return {"data": {"repository": {"pullRequest": {
                "headRefOid": HEAD, "baseRefName": "dev", "state": state,
                "baseRef": {"branchProtectionRule": {"requiredStatusChecks": [
                    {"context": context, "app": None} for context in required]}}}}}}
        if "/rules/branches/" in endpoint or endpoint.endswith("/statuses?per_page=100"):
            return [[]]
        if "/check-runs" in endpoint:
            return [{"total_count": len(runs), "check_runs": [
                {"id": i, "name": name, "app": {"id": 5}, "status": status, "head_sha": HEAD,
                 "conclusion": "success" if status == "completed" else None}
                for i, (name, status) in enumerate(runs, 1)]}]
        return {"head": {"sha": HEAD}, "base": {"ref": "dev"}, "state": "open"}

    monkeypatch.setattr(kanban_pr_acceptance, "_api", api)
    monkeypatch.setattr(kanban_pr_acceptance, "_assignee_profile_home", lambda assignee: None)


def _met(parked_for=None):
    return wait.read_pr_checks({**PR, **({} if parked_for is None else {"parked_for": parked_for})}, None)


OLD = wait.PR_EVIDENCE_GRACE_S


def test_required_checks_that_never_post_do_not_hold_a_wait_once_the_runs_on_the_head_finished(monkeypatch):
    _github(monkeypatch, required=["lint", "tests", "Codex review"], runs=[("lint", "completed"), ("tests", "completed")])
    assert _met(OLD) is True


def test_a_run_still_going_holds_the_wait_even_when_other_required_checks_never_post(monkeypatch):
    _github(monkeypatch, required=["lint", "Codex review"], runs=[("lint", "completed"), ("tests", "in_progress")])
    assert _met(OLD) is False


def test_missing_required_checks_get_the_grace_before_the_head_counts_as_idle(monkeypatch):
    _github(monkeypatch, required=["lint"], runs=[])
    assert (_met(OLD - 1), _met(), _met(OLD)) == (False, False, True)


def test_a_required_check_that_posted_and_finished_is_met_without_waiting_for_the_grace(monkeypatch):
    _github(monkeypatch, required=["lint"], runs=[("lint", "completed")])
    assert _met(0) is True


@pytest.mark.parametrize("state", ["MERGED", "CLOSED"])
def test_a_merged_or_closed_pr_is_met_at_once(monkeypatch, state):
    _github(monkeypatch, required=["lint", "Codex review"], runs=[("lint", "queued")], state=state)
    assert _met(0) is True


def test_without_required_checks_a_run_going_on_the_head_holds_the_wait(monkeypatch):
    _github(monkeypatch, required=[], runs=[("tests", "completed"), ("lint", "in_progress")])
    assert _met(OLD) is False


def test_without_required_checks_finished_runs_meet_the_wait_and_no_runs_wait_out_the_grace(monkeypatch):
    _github(monkeypatch, required=[], runs=[("tests", "completed")])
    assert _met(0) is True
    _github(monkeypatch, required=[], runs=[])
    assert (_met(0), _met(OLD)) == (False, True)


@pytest.mark.parametrize("spec", ["pr:o/r#1,o/r#2", "merged:o/r#1 o/r#2",
                                  "pr:https://github.com/o/r/pull/1 https://github.com/o/r/pull/2"])
def test_a_wait_on_several_prs_is_refused(spec):
    with pytest.raises(ValueError, match="one PR"):
        wait.parse_wait_for(spec)
    assert wait.parse_wait_for("pr:o/r#1") == {"kind": "pr", "repo": "o/r", "number": 1}


@pytest.fixture
def board(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    monkeypatch.setattr(kbd, "_profile_exists_fn", lambda: None)
    monkeypatch.setattr(kbd, "_memory_pressure_level", lambda: "unknown")
    kb.init_db()
    with kbc.connect_closing() as conn:
        yield conn


def _parked_on_pr(board):
    tid = kb.create_task(board, title="waits on ci", assignee="worker")
    run_id = kb.claim_task(board, tid, claimer="worker").current_run_id
    assert kb.schedule_task(board, tid, reason="ci", wait_for=PR, expected_run_id=run_id)
    park = board.execute("SELECT created_at FROM task_events WHERE task_id = ? AND kind = 'scheduled'",
                         (tid,)).fetchone()
    return tid, int(park["created_at"])


def test_the_tick_tells_the_reader_how_long_the_card_has_been_parked(board):
    tid, parked_at = _parked_on_pr(board)
    seen = []

    def reader(wait_for, assignee):
        seen.append(wait_for["parked_for"])
        return False

    wait.wake_waiting_tasks(board, now=parked_at + 90, pr_checks_fn=reader)
    assert seen == [90]
    assert kb.get_task(board, tid).status == "scheduled"


def test_a_pr_wait_unmet_for_hours_goes_to_a_person_with_an_event(board):
    tid, parked_at = _parked_on_pr(board)

    wait.wake_waiting_tasks(board, now=parked_at + wait.PR_WAIT_STALL_S - 1, pr_checks_fn=lambda w, a: False)
    assert kb.get_task(board, tid).status == "scheduled"

    assert wait.wake_waiting_tasks(board, now=parked_at + wait.PR_WAIT_STALL_S, pr_checks_fn=lambda w, a: False) == []
    task = kb.get_task(board, tid)
    kinds = [e.kind for e in kb.list_events(board, tid)]
    assert (task.status, task.block_kind, kinds.count("wait_stalled")) == ("blocked", "needs_input", 1)

    wait.wake_waiting_tasks(board, now=parked_at + wait.PR_WAIT_STALL_S + 60, pr_checks_fn=lambda w, a: False)
    assert [e.kind for e in kb.list_events(board, tid)].count("wait_stalled") == 1


def test_a_long_wait_that_finally_met_wakes_instead_of_going_to_a_person(board):
    tid, parked_at = _parked_on_pr(board)

    assert wait.wake_waiting_tasks(board, now=parked_at + wait.PR_WAIT_STALL_S,
                                   pr_checks_fn=lambda w, a: True) == [tid]
    assert "wait_stalled" not in [e.kind for e in kb.list_events(board, tid)]
