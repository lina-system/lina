"""The daily runner sync keeps its own state apart from a person's runs (lina#238): a ``lina runners sync --only ...``
before ``at`` once stood in for the day's full run, because the serve tick read ``last.json``, which every run writes.

Each case drives the serve tick (``maybe_start``) whose start runs the real sync in-process with ``--daily-run`` against
the fake Grok source, beside a person's runs of the same sync, on one clock in two time zones.
"""

from __future__ import annotations

import json
import shutil
import time

import pytest

from hermes_cli import lina_runner_sync, lina_runner_sync_daily
from hermes_cli import lina_runner_sync_state as state
from tests.hermes_cli.test_lina_runner_sync import _hold_a_hub_slot, current, make_hub, release
from tests.hermes_cli.test_lina_runner_sync_node import _Ended, _local


@pytest.fixture(params=["Asia/Seoul", "UTC"])
def zone(request, monkeypatch):
    monkeypatch.setenv("TZ", request.param)
    time.tzset()
    monkeypatch.setattr(lina_runner_sync_daily, "_child", None)
    monkeypatch.setattr(lina_runner_sync_daily, "_person", None)
    yield request.param
    monkeypatch.undo()
    time.tzset()


@pytest.fixture
def daily(tmp_path, monkeypatch, zone):
    hub = make_hub(tmp_path, monkeypatch)
    root = hub["root"]
    (root / "config.yaml").write_text('worker:\n  runner_sync: {at: "04:00"}\n', encoding="utf-8")
    hub["http"].publish("1.0.71", release("1.0.71"))
    clock = {"now": 0.0}
    hub["env"].clock = lambda: clock["now"]
    starts: list[tuple[list[str] | None, float]] = []

    def spawn(base, only, run):
        starts.append((only, run))
        try:
            lina_runner_sync.sync(base, only=only or ["grok"], env=hub["env"], out=lambda line: None, daily=run)
        except RuntimeError:
            pass  # the child's traceback goes to its log; the tick only started it
        return _Ended()

    def tick(at: float, spawn=spawn, person_spawn=None) -> bool:
        clock["now"] = at
        return lina_runner_sync_daily.maybe_start(root / "worker", now=at, spawn=spawn, person_spawn=person_spawn)

    def manual(at: float, only: list[str] | None = None) -> int:
        clock["now"] = at
        return lina_runner_sync.sync(root, only=only or ["grok"], env=hub["env"], out=lambda line: None)

    return {"root": root, "hub": hub, "tick": tick, "manual": manual, "starts": starts, "spawn": spawn}


def test_a_persons_partial_run_before_at_does_not_stand_in_for_the_full_daily_run(daily):
    root, tick = daily["root"], daily["tick"]

    assert daily["manual"](_local(0, 2, 37), ["grok"]) == 0
    assert state.last(root)["started"] == _local(0, 2, 37)
    assert tick(_local(0, 3, 59)) is False
    assert tick(_local(0, 4, 0)) is True

    assert daily["starts"] == [(None, _local(0, 4, 0))], "the day's full run, not skipped for the manual one"
    record = state.daily(root)
    assert (record["started"], record["finished"], record["rc"]) == (_local(0, 4, 0), _local(0, 4, 0), 0)


def test_a_persons_run_after_the_daily_run_starts_no_second_one_and_the_next_day_runs_in_full(daily):
    root, tick = daily["root"], daily["tick"]
    assert tick(_local(0, 4, 0)) is True
    kept = state.daily(root)

    _hold_a_hub_slot(root)
    assert daily["manual"](_local(0, 5, 0)) == 0, "a person's run that found the hub busy"
    assert state.last(root)["started"] == _local(0, 5, 0)
    for later in (_local(0, 5, 1), _local(0, 5, 31), _local(0, 23, 59)):
        assert tick(later) is False
    assert state.daily(root) == kept, "a person's run, busy hub included, never writes the daily state"

    assert tick(_local(1, 3, 59)) is False
    assert tick(_local(1, 4, 0)) is True
    assert daily["starts"] == [(None, _local(0, 4, 0)), (None, _local(1, 4, 0))]


def test_a_restart_the_same_day_keeps_the_completed_daily_run(daily, monkeypatch):
    root, tick = daily["root"], daily["tick"]
    assert tick(_local(0, 4, 0)) is True
    written = state.daily_path(root).read_bytes()

    monkeypatch.setattr(lina_runner_sync_daily, "_child", None)  # the service restarted
    assert tick(_local(0, 4, 1)) is False and tick(_local(0, 12, 0)) is False
    assert state.daily_path(root).read_bytes() == written
    assert len(daily["starts"]) == 1


def test_a_busy_hub_retries_once_for_only_the_busy_runners_despite_a_persons_run_in_between(daily):
    root, tick = daily["root"], daily["tick"]
    slot = _hold_a_hub_slot(root)
    assert tick(_local(0, 4, 0)) is True
    assert (state.daily(root)["retry"], state.daily(root)["busy"]) == (True, ["grok"])

    assert daily["manual"](_local(0, 4, 10)) == 0, "a person's run while the hub is still busy"
    shutil.rmtree(slot)
    assert tick(_local(0, 4, 29)) is False, "not before 30 minutes after the daily run"
    assert tick(_local(0, 4, 30)) is True
    for later in (_local(0, 5, 0), _local(0, 23, 59)):
        assert tick(later) is False

    assert daily["starts"] == [(None, _local(0, 4, 0)), (["grok"], _local(0, 4, 30))]
    record = state.daily(root)
    assert (record["retried"], record["only"], record["retry_rc"]) == (True, ["grok"], 0)
    assert current(root) == "1.0.71"


def test_a_persons_run_holding_the_lock_delays_the_daily_run_to_the_next_tick(daily):
    root, tick = daily["root"], daily["tick"]

    with lina_runner_sync.run_lock(root):
        assert tick(_local(0, 4, 0)) is False
    assert daily["starts"] == [] and not state.daily_path(root).exists(), "nothing is spent while refused"
    assert tick(_local(0, 4, 1)) is True
    assert state.daily(root)["rc"] == 0


def test_a_daily_child_that_lost_the_lock_takes_its_start_back(daily):
    root, tick = daily["root"], daily["tick"]

    def raced(base, only, run):
        with lina_runner_sync.run_lock(base):  # a person's run started between the tick's check and the child
            return daily["spawn"](base, only, run)

    assert tick(_local(0, 4, 0), spawn=raced) is True
    assert "started" not in state.daily(root) and state.daily(root)["refused"] == _local(0, 4, 0)
    assert tick(_local(0, 4, 1)) is True
    assert state.daily(root)["started"] == _local(0, 4, 1) and state.daily(root)["rc"] == 0
    assert [run for _, run in daily["starts"]] == [_local(0, 4, 0), _local(0, 4, 1)]


def test_a_daily_run_that_failed_is_an_issue_and_a_persons_run_does_not_hide_it(daily):
    root, tick, hub = daily["root"], daily["tick"], daily["hub"]

    def broken(root):
        raise RuntimeError("node list unreadable")

    hub["env"].nodes = broken
    assert tick(_local(0, 4, 0)) is True
    record = state.daily(root)
    assert (record["rc"], record["error"]) == (1, "the run failed: RuntimeError: node list unreadable")

    hub["env"].nodes = None
    assert daily["manual"](_local(0, 6, 0)) == 0
    assert tick(_local(0, 6, 1)) is False
    assert lina_runner_sync.issue_lines(root)[0] == ["- daily: the run failed: RuntimeError: node list unreadable"]


def test_a_legacy_serve_record_becomes_the_daily_state_with_its_retry(daily):
    root, tick = daily["root"], daily["tick"]
    legacy = {"started": _local(0, 4, 0), "finished": _local(0, 4, 0), "rc": 0, "by": "serve", "retry": True,
              "busy": ["grok"]}
    state.write_json(state.last_path(root), legacy)
    before = state.last_path(root).read_bytes()

    assert tick(_local(0, 4, 10)) is False, "today's daily run already ran"
    assert json.loads(state.daily_path(root).read_text()) == {**{k: v for k, v in legacy.items() if k != "by"},
                                                               "from": "last.json"}
    assert state.last_path(root).read_bytes() == before
    assert tick(_local(0, 4, 30)) is True
    assert daily["starts"] == [(["grok"], _local(0, 4, 30))]


def test_a_legacy_serve_start_that_raised_stays_a_could_not_start_issue(daily):
    root, tick = daily["root"], daily["tick"]
    state.write_json(state.last_path(root), {"started": _local(0, 4, 0), "finished": _local(0, 4, 0), "rc": 1,
                                             "by": "serve", "error": "OSError: no fork"})

    assert tick(_local(0, 5, 0)) is False, "today's start is spent, as it was before"
    assert state.daily(root)["error"] == "could not start: OSError: no fork"
    assert lina_runner_sync.issue_lines(root)[0] == ["- daily: could not start: OSError: no fork"]


def test_a_sync_started_again_after_losing_the_lock_does_not_repeat_the_person_check(daily):
    root, tick = daily["root"], daily["tick"]
    (root / "config.yaml").write_text(json.dumps({"worker": {"runner_sync": {"at": "04:00"}},
                                                  "devices": {"workers": {"mac": {"host": "mac-alias",
                                                                                   "role": "person"}}}}),
                                      encoding="utf-8")
    checks: list[float] = []

    def person(base):
        checks.append(base)
        return _Ended()  # the check ended before the next tick: only daily.json stops a second one

    def raced(base, only, run):
        with lina_runner_sync.run_lock(base):
            return daily["spawn"](base, only, run)

    assert tick(_local(0, 4, 0), spawn=raced, person_spawn=person) is True
    assert "started" not in state.daily(root) and state.daily(root)["person_started"] == _local(0, 4, 0)
    assert tick(_local(0, 4, 1), person_spawn=person) is True
    assert state.daily(root)["rc"] == 0 and len(checks) == 1

    assert tick(_local(1, 4, 0), person_spawn=person) is True
    assert len(checks) == 2 and state.daily(root)["person_started"] == _local(1, 4, 0)


@pytest.mark.parametrize("legacy", [
    {"started": "today 02:37", "finished": "today 02:37", "rc": 0},
    {"started": "yesterday 04:00", "finished": "yesterday 04:00", "rc": 0, "by": "serve"},
], ids=["person-today", "serve-yesterday"])
def test_a_legacy_record_of_a_person_or_of_another_day_is_no_daily_run_today(daily, legacy):
    root, tick = daily["root"], daily["tick"]
    stamps = {"today 02:37": _local(0, 2, 37), "yesterday 04:00": _local(-1, 4, 0)}
    state.write_json(state.last_path(root), {key: stamps.get(value, value) for key, value in legacy.items()})

    assert tick(_local(0, 4, 0)) is True
    assert daily["starts"] == [(None, _local(0, 4, 0))]
