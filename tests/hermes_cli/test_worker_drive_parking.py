"""Drive-owned waits never expose a ready card between worker runs (lina#461)."""

import json

import pytest

from tests.hermes_cli import test_worker_goal_drive as support
from tests.hermes_cli.test_worker_goal_drive import (
    BOARD, WORK, _aid, _end, _hub, _later, _ledger, _lose_node, _outage,
    _red_check, _submit, _tick,
)
from hermes_cli.worker import goal_drive, kanban_link


@pytest.fixture
def drive(tmp_path, monkeypatch):
    return support.drive.__wrapped__(tmp_path, monkeypatch)


def _states(drive):
    return [json.loads(line) for line in (drive.cards / "states.jsonl").read_text().splitlines()
            if json.loads(line)["task"] == WORK]


def test_red_check_keeps_card_parked_through_run_end_and_follow_up(drive):
    drive.mp.setenv("FAKE_NO_WAIT_FOR", "1")
    _red_check(drive)
    task = _submit(drive, repo="acme/app")
    kanban_link.park(_hub(drive).store, {"task": WORK, "board": BOARD}, [task])
    _end(drive, task, "done", report=True)
    _tick(drive)
    states = _states(drive)
    assert all(state["status"] == "scheduled" for state in states)
    assert f"WAIT_EVENT=omo-task:{task}/2" in states[-1]["body"]
    assert _hub(drive).store.load(task)["runs"] == 2
    # A duplicate old end cannot wake the card while the follow-up is live.
    kanban_link.wake(_hub(drive).store, {"task": WORK, "board": BOARD},
                     kanban_link.config(_hub(drive).worker), task)
    assert all(state["status"] == "scheduled" for state in _states(drive))


def test_resubmit_retargets_wait_without_a_ready_transition(drive):
    drive.mp.setenv("FAKE_NO_WAIT_FOR", "1")
    task = _submit(drive)
    _lose_node(drive)
    _outage(drive, False)
    (drive.root / "tasks" / task / "rc.1").write_text("1\n")
    (drive.root / "tasks" / task / "state").write_text("failed\n")
    _tick(drive)
    moved = _ledger(drive)["moved"][task]
    states = _states(drive)
    assert all(state["status"] == "scheduled" for state in states)
    assert f"WAIT_EVENT=omo-task:{moved}/1" in states[-1]["body"]


def test_pending_checks_turning_red_retarget_without_waking(drive):
    drive.mp.setenv("FAKE_NO_WAIT_FOR", "1")
    _red_check(drive, conclusion=None, status="in_progress")
    task = _submit(drive, repo="acme/app")
    _end(drive, task, "done", report=True)
    _tick(drive)
    _red_check(drive)
    _later(drive, goal_drive.CHECK_CACHE_S + 1)
    _tick(drive)
    assert _hub(drive).store.load(task)["runs"] == 2
    assert all(state["status"] == "scheduled" for state in _states(drive))
    assert f"WAIT_EVENT=omo-task:{task}/2" in _states(drive)[-1]["body"]


def test_legacy_park_wake_preserves_new_session_read_context(drive):
    drive.mp.setenv("FAKE_NO_WAIT_FOR", "1")
    _red_check(drive, conclusion=None, status="in_progress")
    task = _submit(drive, repo="acme/app")
    _end(drive, task, "done", report=True)
    _tick(drive)
    assert all(state["status"] == "scheduled" for state in _states(drive))
    _red_check(drive, conclusion="success")
    _later(drive, goal_drive.CHECK_CACHE_S + 1)
    _tick(drive)
    view = goal_drive.Board(_hub(drive).worker).show({"task": WORK, "board": BOARD, "kind": "hermes"})
    assert view["status"] == "ready"
    # The external session allocator is not in LINA. Its new session receives this persisted body.
    assert goal_drive.goal_of(view["body"]) == "t_goal"
    assert "acme/app#1" in view["body"]
    assert "run-end comments and attached reports" in view["body"]
    assert "worker result / pull request" in view["body"]
    assert _aid(task, 1, "check-wake") in _ledger(drive)["actions"]


def test_native_capable_board_does_not_keep_an_expired_wait_after_continue(drive):
    task = _submit(drive)
    support._usage_end(drive, task)
    support._set_status(drive, WORK, "ready")
    _tick(drive)
    _later(drive, 1801)
    _tick(drive)
    assert _hub(drive).store.load(task)["runs"] == 2
    drive.mp.setenv("FAKE_NATIVE_WAIT_MET", "1")
    card = {"task": WORK, "board": BOARD, "kind": "hermes"}
    kanban_link._kanban(kanban_link.config(_hub(drive).worker)["command"], card, "dispatch-native")
    view = goal_drive.Board(_hub(drive).worker).show(card)
    assert view["status"] == "scheduled"
    assert f"WAIT_EVENT=omo-task:{task}/2" in view["body"]


def test_added_goal_card_keeps_its_run_end_wake(drive):
    task = _submit(drive)
    extra = "t_extra"
    support._card(drive.cards, extra, "Extra goal card", "scheduled", "## Goal\n\nt_goal — Ship it\n")
    hub = _hub(drive)
    kanban_link.change_card(hub, task, extra, None, BOARD)
    card = {"task": extra, "board": BOARD, "kind": "hermes"}
    kanban_link.park(hub.store, card, [task])
    _end(drive, task, "done", report=True)
    _tick(drive)
    _tick(drive)
    assert goal_drive.Board(hub.worker).show(card)["status"] == "ready"
    assert len(support._writes(drive, "unblock", extra)) == 1


def test_new_drive_park_reclaims_released_ownership(drive):
    drive.mp.setenv("FAKE_NO_WAIT_FOR", "1")
    task = _submit(drive)
    support._usage_end(drive, task)
    _tick(drive)
    support._set_status(drive, WORK, "ready")
    _tick(drive)
    assert _ledger(drive)["notes"][WORK] == "park ownership released"
    _later(drive, 1801)
    _tick(drive)
    assert _hub(drive).store.load(task)["runs"] == 2
    _end(drive, task, "done", report=True)
    _tick(drive)
    assert json.loads((drive.cards / f"{WORK}.json").read_text())["status"] == "ready"
    assert _ledger(drive)["notes"].get(WORK) != "park ownership released"


def test_existing_native_wait_is_not_silently_retargeted(drive):
    task = _submit(drive)
    support._usage_end(drive, task)
    path = drive.cards / f"{WORK}.json"
    card = json.loads(path.read_text())
    card["events"] = [{"kind": "scheduled", "payload": {
        "reason": "old native park", "wait_for": {"kind": "until", "at": 1}}}]
    path.write_text(json.dumps(card))
    _tick(drive)
    _later(drive, 1801)
    _tick(drive)
    assert _hub(drive).store.load(task)["runs"] == 1
    assert "cannot atomically clear" in _ledger(drive)["notes"][WORK]
    assert not support._writes(drive, "edit", WORK)
