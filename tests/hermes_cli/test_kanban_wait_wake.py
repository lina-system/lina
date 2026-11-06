"""lina#161: a WAIT verdict parks the card on the event it waits for, and the
dispatcher wakes it exactly once when that event happens.

Parking and waking reuse the board's own transitions (``schedule_task`` /
``block_task(kind="needs_input")`` and ``unblock_task``); the awaited event is
recorded on the parking event as ``wait_for``. The loop is driven with a
scripted judge against a real temporary board, the wake through real
dispatcher ticks.
"""
from __future__ import annotations

import argparse
import importlib
import os
import time
from pathlib import Path

import pytest

from hermes_cli import goals
from hermes_cli import kanban as kanban_cli
from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli import kanban_db_dispatch as kbd
from hermes_cli.kanban_goal_gates import loop_gate_check


def _wait_mod():
    # Imported per test so a board without the module fails on behaviour, not at collection.
    return importlib.import_module("hermes_cli.kanban_db_wait")


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


def _running(conn, title="waiting card") -> tuple[str, int]:
    tid = kb.create_task(conn, title=title, body="Ship it once the dependency lands.", assignee="worker")
    claimed = kb.claim_task(conn, tid, claimer="worker")
    assert claimed is not None
    return tid, claimed.current_run_id


def _scripted_judge(monkeypatch, *replies):
    seq = list(replies)
    calls: list[dict] = []

    def judge(goal, response, **kwargs):
        calls.append(kwargs)
        return seq.pop(0) if seq else ("continue", "more", False, None, False)

    monkeypatch.setattr(goals, "judge_goal", judge)
    return calls


def _loop(conn, tid, run_id, turns, *, max_turns=50):
    def turn(prompt):
        turns.append(prompt)
        return {"response": "still waiting", "failed": False}

    return goals.run_kanban_goal_loop(
        task_id=tid, goal_text="waiting card", run_turn=turn,
        task_status_fn=lambda: kb.goal_run_status(conn, tid, run_id),
        block_fn=lambda reason: kb.block_task(conn, tid, reason=reason, expected_run_id=run_id),
        max_turns=max_turns, first_response="waiting on the upstream card",
        gate_check=loop_gate_check(tid, "waiting card", run_id=run_id), wait_fn=lambda s: None,
    )


def _tick(conn, spawned: list[str]):
    return kbd.dispatch_once(conn, spawn_fn=lambda task, ws: spawned.append(task.id))


def _event_kinds(conn, tid):
    return [e.kind for e in kb.list_events(conn, tid)]


def test_wait_verdict_parks_card_with_its_event_and_spends_no_turn(board, monkeypatch):
    upstream = kb.create_task(board, title="upstream", assignee="other")
    tid, run_id = _running(board)
    calls = _scripted_judge(monkeypatch, ("wait", "needs upstream", False, {"card": upstream}, False))
    turns: list[str] = []

    res = _loop(board, tid, run_id, turns)

    assert res["outcome"] == "parked"
    assert turns == []
    assert calls == [{"kanban": True}]
    assert kb.get_task(board, tid).status == "scheduled"
    assert _wait_mod().recorded_wait(board, tid, "scheduled") == {"kind": "card", "task_id": upstream}


def test_parked_card_gets_no_turns_until_its_event_then_resumes_exactly_once(board, monkeypatch):
    upstream = kb.create_task(board, title="upstream")   # unassigned: the dispatcher never spawns it
    tid, run_id = _running(board)
    _scripted_judge(monkeypatch, ("wait", "needs upstream", False, {"card": upstream}, False))
    turns: list[str] = []
    assert _loop(board, tid, run_id, turns)["outcome"] == "parked"

    spawned: list[str] = []
    for _ in range(3):
        assert tid not in _tick(board, spawned).woken
    assert (kb.get_task(board, tid).status, spawned, turns) == ("scheduled", [], [])
    assert "goal_turn" not in _event_kinds(board, tid)

    assert kb.complete_task(board, upstream, result="landed") is True
    first = _tick(board, spawned)
    second = _tick(board, spawned)

    assert (first.woken, second.woken) == ([tid], [])
    assert spawned.count(tid) == 1
    assert _event_kinds(board, tid).count("unblocked") == 1


@pytest.mark.parametrize("kind", ["card", "review", "pr", "until"])
def test_each_recorded_event_wakes_the_card_once_it_happens(board, kind):
    wait = _wait_mod()
    other = kb.create_task(board, title="other card", assignee="other")
    wait_for = {"card": {"kind": "card", "task_id": other}, "review": {"kind": "review", "task_id": other},
                "pr": {"kind": "pr", "repo": "o/r", "number": 7},
                "until": {"kind": "until", "at": 1_000}}[kind]
    tid, run_id = _running(board)
    assert kb.schedule_task(board, tid, reason="park", wait_for=wait_for, expected_run_id=run_id)
    checks_done = {"value": False}

    def wake(now):
        return wait.wake_waiting_tasks(board, now=now, pr_checks_fn=lambda w, a: checks_done["value"])

    assert wake(999) == []
    assert kb.get_task(board, tid).status == "scheduled"
    if kind in ("card", "review"):
        kb.complete_task(board, other, result="verdict: approve")
    checks_done["value"] = True
    assert wake(1_000) == [tid]
    assert wake(1_001) == []
    assert kb.get_task(board, tid).status == "ready"


@pytest.mark.parametrize(("checks", "finished"), [
    ([{"classification": "pending"}, {"classification": "success"}], False),
    ([{"classification": "missing"}], False),
    ([{"classification": "failure"}, {"classification": "success"}], True),
])
def test_pr_wait_is_met_only_when_every_required_check_has_finished(monkeypatch, checks, finished):
    from hermes_cli import kanban_pr_acceptance

    receipt = {"classification": "pending", "required": [{"context": "tests"}], "checks": checks}
    monkeypatch.setattr(kanban_pr_acceptance, "collect_acceptance", lambda url, pr, assignee: receipt)

    assert _wait_mod().pr_checks_finished({"kind": "pr", "repo": "o/r", "number": 7}, None) is finished


_HEAD = "a" * 40


def _fake_github(monkeypatch, *, conclusion: str, head_recheck):
    """The GitHub answers ``collect_acceptance`` reads for one PR with one required check."""
    from hermes_cli import kanban_pr_acceptance

    def api(endpoint, *, query=None, paginate=False, profile_home=None):
        if endpoint == "graphql":
            return {"data": {"repository": {"pullRequest": {
                "headRefOid": _HEAD, "baseRefName": "dev", "state": "OPEN",
                "baseRef": {"branchProtectionRule": {"requiredStatusChecks": [{"context": "tests", "app": None}]}}}}}}
        if "/rules/branches/" in endpoint or endpoint.endswith("/statuses?per_page=100"):
            return [[]]
        if "/check-runs" in endpoint:
            return [{"total_count": 1, "check_runs": [{"id": 1, "name": "tests", "app": {"id": 5},
                     "status": "completed", "conclusion": conclusion, "head_sha": _HEAD}]}]
        return head_recheck()

    monkeypatch.setattr(kanban_pr_acceptance, "_api", api)
    monkeypatch.setattr(kanban_pr_acceptance, "_assignee_profile_home", lambda assignee: None)


def test_a_pr_wait_keeps_waiting_when_the_head_recheck_fails(monkeypatch):
    def unreachable():
        raise OSError("network is unreachable")

    _fake_github(monkeypatch, conclusion="success", head_recheck=unreachable)

    assert _wait_mod().pr_checks_finished({"kind": "pr", "repo": "o/r", "number": 7}, None) is False


def test_a_pr_wait_is_met_by_a_completed_read_whose_check_was_cancelled(monkeypatch):
    _fake_github(monkeypatch, conclusion="cancelled", head_recheck=lambda: {
        "head": {"sha": _HEAD}, "base": {"ref": "dev"}, "state": "open"})

    assert _wait_mod().pr_checks_finished({"kind": "pr", "repo": "o/r", "number": 7}, None) is True


def test_needs_input_block_with_an_event_returns_to_ready_but_a_question_waits_for_a_person(board, capsys):
    upstream = kb.create_task(board, title="upstream", assignee="other")
    on_event, _ = _running(board, "blocked on upstream")
    on_person, _ = _running(board, "blocked on a person")
    args = argparse.Namespace(task_id=on_event, ids=None, reason=["needs", "upstream"], kind=None,
                              wait_for=f"card:{upstream}")
    assert kanban_cli._cmd_block(args) == 0
    assert kb.block_task(board, on_person, reason="which colour?", kind="needs_input") is True
    blocked = kb.get_task(board, on_event)
    assert (blocked.status, blocked.block_kind) == ("blocked", "needs_input")

    spawned: list[str] = []
    assert _tick(board, spawned).woken == []
    kb.complete_task(board, upstream, result="done")
    assert _tick(board, spawned).woken == [on_event]
    _tick(board, spawned)

    assert kb.get_task(board, on_event).status in ("ready", "running")
    assert kb.get_task(board, on_person).status == "blocked"


def test_wait_recorded_while_classifying_a_breaker_block_wakes_the_card_once(board):
    upstream = kb.create_task(board, title="upstream")   # unassigned: the dispatcher never spawns it
    tid = kb.create_task(board, title="crashed twice", assignee="worker")
    for _ in range(2):
        assert kb.claim_task(board, tid) is not None
        kbd._record_task_failure(board, tid, "worker crashed", outcome="crashed", release_claim=True, end_run=True)
    assert (kb.get_task(board, tid).status, kb.get_task(board, tid).block_kind) == ("blocked", None)
    args = argparse.Namespace(task_id=tid, ids=None, reason=["needs", "upstream"], kind=None,
                              wait_for=f"card:{upstream}")
    assert kanban_cli._cmd_block(args) == 0
    assert kb.get_task(board, tid).block_kind == "needs_input"

    spawned: list[str] = []
    assert _tick(board, spawned).woken == []
    assert kb.complete_task(board, upstream, result="landed") is True
    first = _tick(board, spawned)
    second = _tick(board, spawned)

    assert (first.woken, second.woken) == ([tid], [])
    assert _event_kinds(board, tid).count("unblocked") == 1


@pytest.mark.parametrize("stop", ["wait", "budget"])
def test_a_parked_reviewer_run_wakes_back_into_review(board, monkeypatch, stop):
    upstream = kb.create_task(board, title="upstream")
    tid, impl_run = _running(board, "reviewed card")
    assert kb.request_review(board, tid, summary="implemented", expected_run_id=impl_run) is True
    review_run = kb.claim_review_task(board, tid).current_run_id
    replies = [("wait", "needs upstream", False, {"card": upstream}, False)] if stop == "wait" else []
    _scripted_judge(monkeypatch, *replies)

    res = _loop(board, tid, review_run, [], max_turns=1)
    assert res["outcome"] == {"wait": "parked", "budget": "handoff_budget"}[stop]
    kb.complete_task(board, upstream, result="landed")
    assert _wait_mod().wake_waiting_tasks(board) == [tid]

    assert kb.get_task(board, tid).status == "review"
    reclaimed = kb.claim_review_task(board, tid)
    assert reclaimed is not None
    claimed = [e for e in kb.list_events(board, tid) if e.kind == "claimed"][-1]
    assert claimed.payload["source_status"] == "review"
    assert kb.request_changes(board, tid, reason="fix the edge case", expected_run_id=reclaimed.current_run_id)[0] is True


def test_a_manual_schedule_without_a_wait_still_unblocks_to_ready(board):
    tid, impl_run = _running(board, "reviewed card")
    assert kb.request_review(board, tid, summary="implemented", expected_run_id=impl_run) is True
    review_run = kb.claim_review_task(board, tid).current_run_id

    assert kb.schedule_task(board, tid, reason="handed to a worker run", expected_run_id=review_run)
    assert kb.unblock_task(board, tid) is True
    assert kb.get_task(board, tid).status == "ready"


def _crash(conn, tid):
    assert kb.claim_task(conn, tid) is not None
    kbd._record_task_failure(conn, tid, "worker crashed", outcome="crashed", release_claim=True, end_run=True)


def test_an_automatic_wake_keeps_the_failure_count(board, monkeypatch):
    tid = kb.create_task(board, title="flaky", assignee="worker")
    _crash(board, tid)
    run_id = kb.claim_task(board, tid).current_run_id
    _scripted_judge(monkeypatch, ("wait", "backing off", False, {"seconds": 60}, False))
    assert _loop(board, tid, run_id, [])["outcome"] == "parked"

    assert _wait_mod().wake_waiting_tasks(board, now=int(time.time()) + 120) == [tid]
    task = kb.get_task(board, tid)
    assert (task.status, task.consecutive_failures) == ("ready", 1)


def test_crashes_alternating_with_budget_handoffs_reach_max_retries(board, monkeypatch):
    tid = kb.create_task(board, title="flaky", assignee="worker", max_retries=2)
    _scripted_judge(monkeypatch)
    _crash(board, tid)
    run_id = kb.claim_task(board, tid).current_run_id
    assert _loop(board, tid, run_id, [], max_turns=1)["outcome"] == "handoff_budget"
    assert _wait_mod().wake_waiting_tasks(board) == [tid]

    _crash(board, tid)

    task = kb.get_task(board, tid)
    assert (task.status, task.consecutive_failures) == ("blocked", 2)
    assert "gave_up" in _event_kinds(board, tid)


def test_a_manual_unblock_still_resets_the_failure_count(board):
    tid = kb.create_task(board, title="flaky", assignee="worker")
    _crash(board, tid)
    run_id = kb.claim_task(board, tid).current_run_id
    assert kb.schedule_task(board, tid, reason="park", wait_for={"kind": "until", "at": 0}, expected_run_id=run_id)

    assert kb.unblock_task(board, tid) is True
    assert kb.get_task(board, tid).consecutive_failures == 0


@pytest.mark.parametrize("repark", ["question", "other_event"])
def test_a_card_re_parked_during_a_pr_read_is_not_woken_by_the_old_result(board, repark):
    upstream = kb.create_task(board, title="upstream")
    tid, run_id = _running(board)
    assert kb.schedule_task(board, tid, reason="ci", wait_for={"kind": "pr", "repo": "o/r", "number": 7},
                            expected_run_id=run_id)

    def checks_finish_while_someone_re_parks(wait_for, assignee):
        assert kb.unblock_task(board, tid) is True
        if repark == "question":
            assert kb.block_task(board, tid, reason="which colour?", kind="needs_input") is True
        else:
            assert kb.schedule_task(board, tid, reason="now upstream", wait_for={"kind": "card", "task_id": upstream})
        return True

    assert _wait_mod().wake_waiting_tasks(board, pr_checks_fn=checks_finish_while_someone_re_parks) == []
    task = kb.get_task(board, tid)
    expected = ("blocked", "needs_input") if repark == "question" else ("scheduled", None)
    assert (task.status, task.block_kind) == expected


def test_turn_budget_end_hands_off_and_returns_to_ready_without_a_failure(board, monkeypatch):
    tid, run_id = _running(board)
    with kb.write_txn(board):
        board.execute("UPDATE tasks SET consecutive_failures = 1 WHERE id = ?", (tid,))
    _scripted_judge(monkeypatch)
    turns: list[str] = []

    res = _loop(board, tid, run_id, turns, max_turns=2)

    assert res["outcome"] == "handoff_budget"
    assert len(turns) == 1
    parked = kb.get_task(board, tid)
    assert (parked.status, parked.consecutive_failures) == ("scheduled", 1)
    assert any("turn budget (2/2)" in c.body for c in kb.list_comments(board, tid))
    _tick(board, [])
    assert kb.get_task(board, tid).status in ("ready", "running")
    assert "blocked" not in _event_kinds(board, tid)


def test_runtime_end_hands_off_without_counting_toward_max_retries(board, monkeypatch):
    monkeypatch.setattr(kb, "_pid_alive", lambda pid: False)
    tid = kb.create_task(board, title="long job", assignee="worker", max_runtime_seconds=1, max_retries=1)
    for _ in range(2):
        kb.claim_task(board, tid)
        kbd._set_worker_pid(board, tid, os.getpid())
        started = int(time.time()) - 30
        with kb.write_txn(board):
            board.execute("UPDATE task_runs SET started_at = ? "
                          "WHERE id = (SELECT current_run_id FROM tasks WHERE id = ?)", (started, tid))
        assert kbd.enforce_max_runtime(board, signal_fn=lambda pid, sig: None) == [tid]

    task = kb.get_task(board, tid)
    assert (task.status, task.consecutive_failures) == ("ready", 0)
    assert "gave_up" not in _event_kinds(board, tid)
    assert sum("runtime limit" in c.body for c in kb.list_comments(board, tid)) == 2


def test_a_targetless_kanban_wait_parks_on_the_default_time_wait(board, monkeypatch):
    raw = '{"verdict": "wait", "reason": "waiting on the deploy"}'

    def judge(goal, response, **kwargs):
        verdict, reason, parse_failed, directive = goals._parse_judge_response(raw, **kwargs)
        return verdict, reason, parse_failed, directive, False

    monkeypatch.setattr(goals, "judge_goal", judge)
    tid, run_id = _running(board)
    turns: list[str] = []
    before = int(time.time())

    assert _loop(board, tid, run_id, turns)["outcome"] == "parked"
    wait_for = _wait_mod().recorded_wait(board, tid, "scheduled")
    assert turns == []
    assert wait_for["kind"] == "until"
    assert before + goals.GATE_WAIT_MAX_S <= wait_for["at"] <= int(time.time()) + goals.GATE_WAIT_MAX_S
    assert goals._parse_judge_response(raw)[0] == "continue"


@pytest.mark.parametrize("kind", ["card", "review"])
@pytest.mark.parametrize("completed", [True, False])
def test_an_archived_upstream_counts_only_when_it_was_completed(board, kind, completed):
    upstream = kb.create_task(board, title="upstream")
    tid, run_id = _running(board)
    assert kb.schedule_task(board, tid, reason="park", wait_for={"kind": kind, "task_id": upstream},
                            expected_run_id=run_id)
    if completed:
        assert kb.complete_task(board, upstream, result="landed") is True
    assert kb.archive_task(board, upstream) is True

    spawned: list[str] = []
    assert _tick(board, spawned).woken == ([tid] if completed else [])
    assert (kb.get_task(board, tid).status == "scheduled") is (not completed)


def test_kanban_judge_reply_names_card_events_only_for_a_kanban_judge():
    raw = '{"verdict": "wait", "wait_on_pr": "https://github.com/o/r/pull/7", "reason": "ci"}'

    assert goals._parse_judge_response(raw, kanban=True)[3] == {"pr": "https://github.com/o/r/pull/7"}
    assert goals._parse_judge_response(raw)[0] == "continue"
