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


@pytest.mark.parametrize("interrupted", [False, True])
def test_usage_resume_recovers_its_park_after_send_committed(drive, interrupted):
    task = _submit(drive)
    support._usage_end(drive, task)
    _tick(drive)
    original = goal_drive.Board.schedule

    def refuse(board, card, event, reason, status, aid):
        if "usage-resume-park" in aid:
            if interrupted:
                raise OSError("hub stopped after send")
            return "board temporarily refused"
        return original(board, card, event, reason, status, aid)

    drive.mp.setattr(goal_drive.Board, "schedule", refuse)
    _later(drive, 1801)
    _tick(drive)
    assert _hub(drive).store.load(task)["runs"] == 2
    drive.mp.setattr(goal_drive.Board, "schedule", original)
    _tick(drive)  # a fresh Hub reads run 2, not the old usage wait's run 1
    body = json.loads((drive.cards / f"{WORK}.json").read_text())["body"]
    assert f"WAIT_EVENT=omo-task:{task}/2" in body
    assert support._requests(drive, task) == ["request.1", "request.2"]


def test_one_todo_worker_end_does_not_discard_another_todos_checks(drive):
    _red_check(drive, conclusion=None, status="in_progress")
    first = _submit(drive, repo="acme/app")
    _end(drive, first, "done", report=True)
    _tick(drive)
    second = _submit(drive, todo="acme/app#2")
    _end(drive, second, "done")
    _tick(drive)
    assert _hub(drive).store.load(second)["runs"] == 2
    _end(drive, second, "done", report=True)
    _tick(drive)
    assert json.loads((drive.cards / f"{WORK}.json").read_text())["status"] == "scheduled"
    _red_check(drive, conclusion="success")
    _later(drive, goal_drive.CHECK_CACHE_S + 1)
    _tick(drive)
    assert json.loads((drive.cards / f"{WORK}.json").read_text())["status"] == "ready"


def test_usage_resume_recovers_when_hub_stops_before_recording_park(drive):
    task = _submit(drive)
    support._usage_end(drive, task)
    _tick(drive)
    original = goal_drive.Drive.started

    def stop_after_send(self, aid, send):
        original(self, aid, send)
        if aid.endswith(":usage-resume"):
            raise OSError("hub stopped before park intent")

    drive.mp.setattr(goal_drive.Drive, "started", stop_after_send)
    _later(drive, 1801)
    _tick(drive)
    assert _hub(drive).store.load(task)["runs"] == 2
    drive.mp.setattr(goal_drive.Drive, "started", original)
    _tick(drive)
    body = json.loads((drive.cards / f"{WORK}.json").read_text())["body"]
    assert f"WAIT_EVENT=omo-task:{task}/2" in body
    assert support._requests(drive, task) == ["request.1", "request.2"]


def test_checks_finishing_first_do_not_wake_another_todos_worker(drive):
    _red_check(drive, conclusion=None, status="in_progress")
    first = _submit(drive, repo="acme/app")
    _end(drive, first, "done", report=True)
    _tick(drive)
    second = _submit(drive, todo="acme/app#2")
    _end(drive, second, "done")
    _tick(drive)
    _red_check(drive, conclusion="success")
    _later(drive, goal_drive.CHECK_CACHE_S + 1)
    _tick(drive)
    assert json.loads((drive.cards / f"{WORK}.json").read_text())["status"] == "scheduled"
    _end(drive, second, "done", report=True)
    _tick(drive)
    assert json.loads((drive.cards / f"{WORK}.json").read_text())["status"] == "ready"


@pytest.mark.parametrize("after_intent", [False, True])
def test_resubmit_park_recovers_from_committed_receipt(drive, after_intent):
    task = _submit(drive)
    _lose_node(drive)
    _outage(drive, False)
    (drive.root / "tasks" / task / "rc.1").write_text("1\n")
    (drive.root / "tasks" / task / "state").write_text("failed\n")
    original_park, original_schedule = goal_drive.Drive.park_run, goal_drive.Board.schedule

    def stop_before(self, card, job, aid, target):
        if aid.endswith(":resubmit-park"):
            raise OSError("stopped before intent")
        return original_park(self, card, job, aid, target)

    def stop_after(self, card, event, reason, status, aid):
        if aid.endswith(":resubmit-park"):
            raise OSError("stopped after intent")
        return original_schedule(self, card, event, reason, status, aid)

    drive.mp.setattr(goal_drive.Board, "schedule", stop_after if after_intent else original_schedule)
    drive.mp.setattr(goal_drive.Drive, "park_run", original_park if after_intent else stop_before)
    _tick(drive)
    moved = _ledger(drive)["moved"][task]
    drive.mp.setattr(goal_drive.Board, "schedule", original_schedule)
    drive.mp.setattr(goal_drive.Drive, "park_run", original_park)
    _tick(drive)
    assert len(_hub(drive).store.all()) == 2
    assert f"WAIT_EVENT=omo-task:{moved}/1" in json.loads((drive.cards / f"{WORK}.json").read_text())["body"]
    _end(drive, moved, "done", report=True)
    _tick(drive)
    assert json.loads((drive.cards / f"{WORK}.json").read_text())["status"] == "ready"


@pytest.mark.parametrize("two_workers", [False, True])
def test_disabled_drive_releases_ledger_wait_on_worker_end(drive, two_workers):
    tasks = [_submit(drive)]
    if two_workers:
        tasks.append(_submit(drive, todo="acme/app#2"))
    for task in tasks:
        _end(drive, task, "done")
    _tick(drive)
    assert all(_hub(drive).store.load(task)["runs"] == 2 for task in tasks)
    drive.configure("  goal_drive: false\n")
    for i, task in enumerate(tasks):
        _end(drive, task, "done", report=True)
        expected = "ready" if i == len(tasks) - 1 else "scheduled"
        assert json.loads((drive.cards / f"{WORK}.json").read_text())["status"] == expected
    _tick(drive)
    assert len(support._writes(drive, "unblock", WORK)) == 1


@pytest.mark.parametrize("rule", ["deliverable", "red-check", "usage-move", "node-continued"])
def test_committed_worker_actions_share_park_recovery(drive, rule):
    if rule == "usage-move":
        drive.configure("  goal_limits: {usage_move: true}\n")
    if rule == "red-check":
        _red_check(drive)
    task = _submit(drive, **({"repo": "acme/app"} if rule == "red-check" else {}))
    if rule == "node-continued":
        _lose_node(drive)
        _outage(drive, False)
        (drive.root / "tasks" / task / "state").write_text("running\n")
    elif rule == "usage-move":
        support._usage_end(drive, task)
    else:
        _end(drive, task, "done", report=rule == "red-check")
    original = goal_drive.Drive.park_run

    def stop(self, card, job, aid, target):
        if aid.endswith(f":{rule}-park"):
            raise OSError("stopped after committed action")
        return original(self, card, job, aid, target)

    drive.mp.setattr(goal_drive.Drive, "park_run", stop)
    _tick(drive)
    drive.mp.setattr(goal_drive.Drive, "park_run", original)
    target = _ledger(drive)["moved"].get(task, task)
    run = _hub(drive).store.load(target)["runs"]
    _tick(drive)
    body = json.loads((drive.cards / f"{WORK}.json").read_text())["body"]
    assert f"WAIT_EVENT=omo-task:{target}/{run}" in body
    _tick(drive)
    assert _hub(drive).store.load(target)["runs"] == run
