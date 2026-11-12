"""lina#604: no park without a waker, and a time wait really wakes.

``kanban_schedule`` (tool) and ``hermes kanban schedule`` (CLI) share one validator
(``kanban_db_park.resolve_park``): a park that nothing can wake is refused and changes nothing; each
accepted form parks. A time wait is recorded on the ``scheduled`` event and the dispatcher's tick wakes
the card from the store, so a restart between park and wake loses nothing. A park whose waker is late,
ended or missing shows "wake overdue" in ``kanban diagnostics`` and on the ``kanban list`` line, and
parks that carried their time only in the reason are given a recorded wait once, on the board's
migration pass. Driven against a real temporary board.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path
from types import SimpleNamespace

import pytest

from hermes_cli import kanban as kanban_cli
from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli import kanban_db_dispatch as kbd
from hermes_cli import kanban_db_park as park
from hermes_cli import kanban_db_wait as kw
from hermes_cli import kanban_diagnostics as kd

RUN = "OT-T0000000001"


@pytest.fixture
def board(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("LINA_HOME", str(tmp_path / "lina"))
    monkeypatch.setenv("HERMES_PROFILE", "test-worker")
    monkeypatch.delenv("HERMES_SESSION_ID", raising=False)
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    monkeypatch.setattr(kbd, "_profile_exists_fn", lambda: None)
    monkeypatch.setattr(kbd, "_memory_pressure_level", lambda: "unknown")
    kb._INITIALIZED_PATHS.clear()
    kb.init_db()
    return tmp_path


def _worker(monkeypatch, title="parked card") -> str:
    with kbc.connect_closing() as conn:
        tid = kb.create_task(conn, title=title, assignee="test-worker")
        run_id = kb.claim_task(conn, tid).current_run_id
    monkeypatch.setenv("HERMES_KANBAN_TASK", tid)
    monkeypatch.setenv("HERMES_KANBAN_RUN_ID", str(run_id))
    return tid


def _schedule(**args) -> dict:
    from tools import kanban_tools  # registers the kanban handlers
    from tools.registry import registry

    return json.loads(registry.dispatch("kanban_schedule", args))


def _status(tid: str) -> str:
    with kbc.connect_closing() as conn:
        return kb.get_task(conn, tid).status


def _last_park(tid: str) -> dict:
    with kbc.connect_closing() as conn:
        return [e for e in kb.list_events(conn, tid) if e.kind == "scheduled"][-1].payload


@pytest.mark.parametrize("args", [
    {"reason": "waiting on CI and the merge gate"},
    {"reason": "until 06:30, then check again"},
    {},
])
def test_a_park_nothing_can_wake_is_refused_naming_the_accepted_forms(board, monkeypatch, args):
    tid = _worker(monkeypatch)

    out = _schedule(**args)

    assert "a park needs something that wakes it" in out["error"]
    for form in ("run:", "card:", "review:", "until:", "pr:", "merged:", "unit:"):
        assert form in out["error"]
    assert _status(tid) == "running"


def test_the_cli_shares_the_refusal_and_changes_nothing(board, monkeypatch, capsys):
    with kbc.connect_closing() as conn:
        tid = kb.create_task(conn, title="cli park", assignee="test-worker")
    prose = argparse.Namespace(task_id=tid, ids=None, reason=["waiting", "on", "CI"], wait_for=None)
    timed = argparse.Namespace(task_id=tid, ids=None, reason=["nightly"], wait_for="until:2030-01-01T00:00:00Z")

    assert kanban_cli._cmd_schedule(prose) != 0
    assert "a park needs something that wakes it" in capsys.readouterr().err
    assert _status(tid) == "ready"
    assert kanban_cli._cmd_schedule(timed) == 0
    assert _status(tid) == "scheduled"
    assert _last_park(tid)["wait_for"] == {"kind": "until", "at": 1893456000}


@pytest.mark.parametrize(("args", "recorded"), [
    ({"wait_for": "until:2030-01-01T00:00:00Z"}, {"kind": "until", "at": 1893456000}),
    ({"wait_for": "pr:example/repo#7"}, {"kind": "pr", "repo": "example/repo", "number": 7}),
    ({"wait_for": "merged:https://github.com/example/repo/pull/7"},
     {"kind": "merged", "repo": "example/repo", "number": 7}),
    ({"wait_for": f"run:{RUN}"}, {"kind": "run", "runs": [RUN]}),
    ({"wait_for": "unit:release-train"}, {"kind": "unit", "name": "release-train"}),
    ({"reason": "SCHEDULED_UNTIL=2030-01-01T00:00:00Z nightly window"}, {"kind": "until", "at": 1893456000}),
    ({"reason": f"waiting on lina worker {RUN}"}, None),
    ({"reason": "checks pending · WAIT_EVENT=pr:example/repo#7 GOAL_ACTION=goal-card-run-1-check-park"}, None),
])
def test_each_accepted_waker_parks(board, monkeypatch, args, recorded):
    tid = _worker(monkeypatch)

    out = _schedule(**args)

    assert out["ok"] is True, out
    assert _status(tid) == "scheduled"
    assert _last_park(tid).get("wait_for") == recorded


@pytest.mark.parametrize("kind", ["card", "review"])
def test_a_card_or_review_wait_parks_and_must_name_an_existing_card(board, monkeypatch, kind):
    tid = _worker(monkeypatch)
    with kbc.connect_closing() as conn:
        upstream = kb.create_task(conn, title="upstream")

    assert "unknown task no-such-card" in _schedule(wait_for=f"{kind}:no-such-card")["error"]
    assert _schedule(wait_for=f"{kind}:{upstream}")["ok"] is True
    assert _last_park(tid)["wait_for"] == {"kind": kind, "task_id": upstream}


def test_a_time_wait_wakes_on_the_first_tick_after_it_even_across_a_dispatcher_restart(board, monkeypatch):
    clock = [1_900_000_000]
    monkeypatch.setattr(kw, "time", SimpleNamespace(time=lambda: clock[0]))
    tid = _worker(monkeypatch)
    assert _schedule(wait_for=f"until:{clock[0] + 2}")["ok"] is True

    with kbc.connect_closing() as conn:
        clock[0] += 1
        assert tid not in kbd.dispatch_once(conn, spawn_fn=lambda task, ws: None).woken
    kb._INITIALIZED_PATHS.clear()  # a new dispatcher process: nothing of the park is held in memory
    clock[0] += 1
    with kbc.connect_closing() as conn:
        assert kbd.dispatch_once(conn, spawn_fn=lambda task, ws: None).woken == [tid]
        assert kb.get_task(conn, tid).status in ("ready", "running")


def test_a_merge_wait_wakes_on_merge_and_a_pr_closed_unmerged_goes_to_a_person(board, monkeypatch):
    with kbc.connect_closing() as conn:
        merged, closed = (kb.create_task(conn, title=t, assignee="test-worker") for t in ("merged", "closed"))
        for tid, number in ((merged, 1), (closed, 2)):
            assert kb.schedule_task(conn, tid, reason="waiting for the merge",
                                    wait_for={"kind": "merged", "repo": "example/repo", "number": number})
        states = {1: "OPEN", 2: "CLOSED"}

        def read(wait_for, assignee):
            if states[wait_for["number"]] == "CLOSED":
                raise kw.WaitGone("example/repo#2 was closed without merging")
            return states[wait_for["number"]] == "MERGED"

        assert kw.wake_waiting_tasks(conn, pr_merged_fn=read) == []
        assert (kb.get_task(conn, closed).status, kb.get_task(conn, closed).block_kind) == ("blocked", "needs_input")
        states[1] = "MERGED"
        assert kw.wake_waiting_tasks(conn, pr_merged_fn=read) == [merged]


def _overdue_diagnostics(tid: str, now: int | None = None) -> list:
    with kbc.connect_closing() as conn:
        task = kb.get_task(conn, tid)
        return [d for d in kd.compute_task_diagnostics(task, kb.list_events(conn, tid), kb.list_runs(conn, tid),
                                                       now=now) if d.kind == "wake_overdue"]


def test_a_park_on_a_worker_run_with_no_record_shows_wake_overdue(board, monkeypatch, capsys):
    tid = _worker(monkeypatch)
    assert _schedule(reason=f"waiting on lina worker {RUN}")["ok"] is True

    found = _overdue_diagnostics(tid)
    list_args = argparse.Namespace(assignee=None, mine=False, status=None, tenant=None, session=None,
                                   archived=False, sort=None, workflow_template_id=None,
                                   current_step_key=None, json=False)
    assert kanban_cli._cmd_list(list_args) == 0
    line = next(line for line in capsys.readouterr().out.splitlines() if tid in line)

    assert [d.severity for d in found] == ["error"]
    assert f"worker run {RUN} has no record" in found[0].title
    assert "wake overdue" in line


def test_a_park_on_a_live_worker_run_is_not_overdue(board, monkeypatch):
    jobs = board / "lina" / "worker" / "hub" / "jobs"
    jobs.mkdir(parents=True)
    (jobs / f"{RUN}.json").write_text(json.dumps({"id": RUN, "state": "running"}), encoding="utf-8")
    tid = _worker(monkeypatch)
    assert _schedule(wait_for=f"run:{RUN}")["ok"] is True

    assert _overdue_diagnostics(tid) == []


def test_a_time_wait_past_its_time_is_overdue_only_after_the_grace(board, monkeypatch):
    tid = _worker(monkeypatch)
    assert _schedule(wait_for="until:1900000000")["ok"] is True

    assert _overdue_diagnostics(tid, now=1_900_000_000 + park.OVERDUE_GRACE_S) == []
    assert "wake overdue: its time" in _overdue_diagnostics(tid, now=1_900_000_000 + park.OVERDUE_GRACE_S + 60)[0].title


def test_legacy_time_markers_on_a_fixture_board_are_recorded_once_or_listed(board):
    with kbc.connect_closing() as conn:
        cards = {name: kb.create_task(conn, title=name, assignee="test-worker")
                 for name in ("timed", "garbled", "on a run", "prose", "has a wait")}
        reasons = {"timed": "SCHEDULED_UNTIL=2030-01-01T00:00:00Z waiting for reconnect",
                   "garbled": "SCHEDULED_UNTIL=after-lunch waiting",
                   "on a run": f"SCHEDULED_UNTIL=2030-01-01T00:00:00Z waiting on lina worker {RUN}",
                   "prose": "waiting on CI"}
        for name, reason in reasons.items():   # parked the way the old tool parked: reason text only
            assert kb.schedule_task(conn, cards[name], reason=reason)
        assert kb.schedule_task(conn, cards["has a wait"], reason="SCHEDULED_UNTIL=2031-01-01T00:00:00Z",
                                wait_for={"kind": "card", "task_id": cards["prose"]})
        before = {name: [e.payload for e in kb.list_events(conn, tid) if e.kind == "scheduled"][-1]
                  for name, tid in cards.items()}

    for _ in range(2):   # the upgrade's migration pass, then a later start: still once
        kb._INITIALIZED_PATHS.clear()
        kb.init_db()

    with kbc.connect_closing() as conn:
        after = {name: [e.payload for e in kb.list_events(conn, tid) if e.kind == "scheduled"][-1]
                 for name, tid in cards.items()}
        kinds = {name: [e.kind for e in kb.list_events(conn, tid)] for name, tid in cards.items()}
        assert kw.recorded_wait(conn, cards["timed"], "scheduled") == {"kind": "until", "at": 1893456000}
        assert all(kb.get_task(conn, tid).status == "scheduled" for tid in cards.values())
    assert kinds["timed"].count("wait_recorded") == 1
    assert kinds["garbled"].count("wait_unparsed") == 1
    assert {name: after[name] for name in ("garbled", "on a run", "prose", "has a wait")} == \
        {name: before[name] for name in ("garbled", "on a run", "prose", "has a wait")}
    assert "nothing wakes this park" in _overdue_diagnostics(cards["garbled"])[0].title
    assert "nothing wakes this park" in _overdue_diagnostics(cards["prose"])[0].title
