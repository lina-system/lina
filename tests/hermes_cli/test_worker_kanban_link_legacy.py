"""Mixed hub/legacy schedules wake after the last end, even across hub restarts (lina#390)."""

import json
import sys

import pytest

from hermes_cli.worker import kanban_link
from hermes_cli.worker.hub import Hub
from tests.hermes_cli.test_worker_callers import (
    BOARD, CARD, _config, _scheduled, _woke, board, kanban,
)


@pytest.fixture
def legacy(board, tmp_path):
    """A real executable with an explicit status protocol, not a subprocess mock."""
    hub, _, _, _ = board
    executable = tmp_path / "old omo-task"
    reply = tmp_path / "legacy.json"
    calls = tmp_path / "legacy.calls"
    executable.write_text(
        f"#!{sys.executable}\n"
        "import json, pathlib, signal, sys\n"
        "assert sys.argv[1:3] == ['status', '--json'] and len(sys.argv) == 4\n"
        f"with open({str(calls)!r}, 'a') as log:\n"
        "    log.write(sys.argv[3] + '\\n')\n"
        f"reply = json.loads(pathlib.Path({str(reply)!r}).read_text())\n"
        "if reply.get('hang'):\n"
        "    signal.pause()\n"
        "print(reply['stdout'])\n"
        "sys.exit(reply.get('code', 0))\n",
        encoding="utf-8",
    )
    executable.chmod(0o755)
    _config(hub.base, f"  card_link: {json.dumps(hub._link())}\n  legacy_omo_task: {json.dumps(str(executable))}\n")

    def answer(state="running", *, stdout=None, code=0, hang=False):
        reply.write_text(json.dumps({"stdout": json.dumps({"state": state}) if stdout is None else stdout,
                                     "code": code, "hang": hang}), encoding="utf-8")

    answer()
    return answer, executable, calls


@pytest.mark.platforms("posix")
@pytest.mark.parametrize("prefix", ["OT", "OG"])
@pytest.mark.parametrize("last", ["hub", "legacy"])
def test_mixed_schedule_wakes_once_after_later_end(board, legacy, prefix, last):
    # Given one hub run and one legacy run named by the same schedule.
    hub, submit, show, calls = board
    answer, _, _ = legacy
    run, old = submit(), f"{prefix}-OLD1106000000"
    show("scheduled", [_scheduled(f"workers={run},{old}")])

    # When each side ends, including a hub restart before a legacy-last end.
    if last == "hub":
        answer("done")
        hub.tick()
        assert _woke(calls) == 0
        hub.notify(run, "done", "1")
    else:
        hub.notify(run, "done", "1")
        hub.tick()
        assert _woke(calls) == 0
        assert hub.store.load(run).get("card_pending")
        answer("done")
        hub = Hub(hub.worker_root, out=lambda *_: None)
        hub.tick()
    hub.tick()
    hub.notify(run, "done", "1")

    # Then the card wakes once, without repeating its start/end comments.
    assert _woke(calls) == 1
    assert sum(call["verb"] == "comment" for call in calls()) == 2
    assert not hub.store.load(run).get("card_pending")


@pytest.mark.platforms("posix")
@pytest.mark.parametrize("prefix", ["OT", "OG"])
@pytest.mark.parametrize("state", ["done", "review", "done_empty", "stalled", "failed", "cancelled", "unknown"])
def test_legacy_terminal_state_is_not_open(board, legacy, prefix, state):
    # Given an explicit terminal status from the configured legacy executable.
    hub, _, _, _ = board
    answer, _, calls = legacy
    answer(state)
    old = f"{prefix}-OLD1106000000"
    # When the hub checks the foreign run, then the reported end is respected.
    assert kanban_link._still_open(hub.store, old) is False
    assert calls.read_text().splitlines() == [old]


@pytest.mark.platforms("posix")
@pytest.mark.parametrize("failure", ["running", "missing", "timeout", "empty", "malformed", "unknown_id",
                                   "wrong_shape", "missing_state", "future_state"])
def test_unanswered_or_open_legacy_run_keeps_card_parked(board, legacy, monkeypatch, failure):
    # Given a scheduled mixed run whose legacy side cannot be confirmed ended.
    hub, submit, show, calls = board
    answer, executable, _ = legacy
    run, old = submit(), "OT-OLD1106000000"
    show("scheduled", [_scheduled(f"workers={run},{old}")])
    if failure == "missing":
        executable.unlink()
    elif failure == "timeout":
        answer(hang=True)
        monkeypatch.setattr(kanban_link, "LEGACY_STATUS_TIMEOUT_S", 0.2, raising=False)
    elif failure == "empty":
        answer(stdout="")
    elif failure == "malformed":
        answer(stdout="not JSON")
    elif failure == "unknown_id":
        answer("unknown", code=2)
    elif failure == "wrong_shape":
        answer(stdout="[]")
    elif failure == "missing_state":
        answer(stdout="{}")
    elif failure == "future_state":
        answer("not_a_known_end")
    before = hub.store.log_file.read_text().splitlines()

    # When the hub side ends, then no wake is sent and its wake stays pending.
    hub.notify(run, "done", "1")
    assert _woke(calls) == 0
    assert hub.store.load(run).get("card_pending")
    if failure in ("missing", "timeout"):
        lines = hub.store.log_file.read_text().splitlines()[len(before):]
        assert len([line for line in lines if old in line and "legacy" in line]) == 1


@pytest.mark.platforms("posix")
@pytest.mark.parametrize("group", [False, True])
def test_hub_owned_run_or_group_never_queries_legacy(board, legacy, group):
    # Given a hub-owned run or batch and a legacy tool claiming it ended.
    hub, submit, _, _ = board
    answer, _, calls = legacy
    run = submit()
    name = "OG-NEW1106000000" if group else run
    if group:
        hub.store.update(run, group=name)
    answer("done")
    # When checked before and after its local end, only hub records decide.
    assert kanban_link._still_open(hub.store, name) is True
    hub.store.update(run, state="done")
    assert kanban_link._still_open(hub.store, name) is False
    assert not calls.exists()
