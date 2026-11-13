"""The daily runner sync and the hub itself (lina#238): a runner with no LINA copy on the hub is a notice and an
issue but no hold; a day that found the hub busy gets one more run 30 minutes later for those runners only, never a
third; the hub's sign-in question names the hub's own device.

The retry cases drive the serve tick (``maybe_start``) whose start runs the real sync in-process against the fake
Grok source and a hub slot held by a task folder, on one clock.
"""

from __future__ import annotations

import json
import shutil
import time

import pytest

from hermes_cli import lina_deps, lina_runner_sync, lina_runner_sync_daily, lina_runners
from hermes_cli import lina_runner_sync_state as state
from tests.hermes_cli.test_lina_runner_sync import _hold_a_hub_slot, current, make_hub, release, run
from tests.hermes_cli.test_lina_runner_sync_node import _Ended, _local

PREPARE_LINE = "- prepare: grok is not installed on the hub; run `lina runners install grok`"


@pytest.fixture
def hub(tmp_path, monkeypatch):
    return make_hub(tmp_path, monkeypatch)


@pytest.fixture(params=["Asia/Seoul", "UTC"])
def zone(request, monkeypatch):
    monkeypatch.setenv("TZ", request.param)
    time.tzset()
    monkeypatch.setattr(lina_runner_sync_daily, "_child", None)
    yield request.param
    monkeypatch.undo()
    time.tzset()


def _install_grok(root) -> None:
    vendor = lina_runners.vendor_dir(root, "grok")
    (vendor / "1.0.70").mkdir(parents=True)
    entry = vendor / "1.0.70" / "grok"
    entry.write_bytes(release("1.0.70"))
    entry.chmod(0o755)
    lina_deps.set_current(root, "1.0.70", vendor)


def _record(root) -> dict:
    return json.loads(state.record_path(root, state.last(root)["started"]).read_text())


def test_a_runner_not_installed_on_the_hub_is_a_notice_and_an_issue_but_no_hold(hub):
    root = hub["root"]
    shutil.rmtree(lina_runners.vendor_dir(root, "grok"))
    hub["http"].publish("1.0.71", release("1.0.71"))

    code, _ = run(hub)

    assert code == 0
    assert _record(root)["runners"]["grok"]["hub"]["state"] == "hub prepare needed"
    assert state.holds(root)["holds"] == []
    assert len(hub["notices"]) == 1 and "grok" in hub["notices"][0]
    assert lina_runner_sync.issue_lines(root)[0] == [PREPARE_LINE]

    _install_grok(root)
    assert run(hub)[0] == 0 and current(root) == "1.0.71"
    assert lina_runner_sync.issue_lines(root)[0] == []


@pytest.mark.parametrize(("workers", "named"), [
    ("    home-hub: {host: local}\n    build-a: {host: build-a}\n", "home-hub"),
    ("    build-a: {host: build-a}\n", "hub"),
], ids=["local-device", "no-local-device"])
def test_the_hub_sign_in_question_names_the_hub_device(hub, workers, named):
    root = hub["root"]
    (root / "config.yaml").write_text("devices:\n  workers:\n" + workers, encoding="utf-8")
    hub["http"].publish("1.0.71", release("1.0.71"))
    hub["env"].sign_in = lambda runner: "signed_out"

    run(hub)

    assert lina_runner_sync.issue_lines(root)[0] == [f"- sign-in: Sign in to grok on 1 node(s): {named}"]
    ask = _record(root)["questions"][0]
    assert [node["node"] for node in ask["nodes"]] == [named]
    assert f"lina devices sign-in {named} grok" in ask["nodes"][0]["how"]


@pytest.fixture
def daily(hub, zone):
    root = hub["root"]
    (root / "config.yaml").write_text('worker:\n  runner_sync: {at: "04:00"}\n', encoding="utf-8")
    hub["http"].publish("1.0.71", release("1.0.71"))
    clock = {"now": 0.0}
    hub["env"].clock = lambda: clock["now"]
    starts: list[list[str] | None] = []

    def spawn(base, only=None):
        starts.append(only)
        lina_runner_sync.sync(base, only=only or ["grok"], env=hub["env"], out=lambda line: None)
        return _Ended()

    def tick(at: float) -> bool:
        clock["now"] = at
        return lina_runner_sync_daily.maybe_start(root / "worker", now=at, spawn=spawn)
    return root, tick, starts


def test_a_busy_hub_is_tried_once_more_after_30_minutes_for_those_runners(daily):
    root, tick, starts = daily
    slot = _hold_a_hub_slot(root)

    assert tick(_local(0, 4, 0)) is True
    last = state.last(root)
    assert (last["retry"], last["busy"]) == (True, ["grok"])
    assert tick(_local(0, 4, 29)) is False, "not before 30 minutes"

    shutil.rmtree(slot)
    assert tick(_local(0, 4, 30)) is True
    assert starts == [None, ["grok"]]
    assert current(root) == "1.0.71"
    assert _record(root)["runners"]["grok"]["hub"]["state"] == "moved"
    assert "retry" not in state.last(root)


def test_a_hub_still_busy_on_the_retry_is_left_for_the_next_day(daily):
    root, tick, starts = daily
    _hold_a_hub_slot(root)

    assert tick(_local(0, 4, 0)) is True
    assert tick(_local(0, 4, 30)) is True
    assert state.last(root).get("retry") is None and state.last(root)["retried"] is True
    for later in (_local(0, 5, 0), _local(0, 6, 0), _local(0, 23, 59)):
        assert tick(later) is False
    assert starts == [None, ["grok"]], "never a third run on one day"
    assert current(root) == "1.0.70"


def test_a_day_without_a_busy_hub_has_no_retry(daily):
    root, tick, starts = daily

    assert tick(_local(0, 4, 0)) is True
    assert "retry" not in state.last(root)
    assert tick(_local(0, 4, 30)) is False and tick(_local(0, 6, 0)) is False
    assert starts == [None]


def test_a_retry_left_over_from_yesterday_does_not_run_today(daily):
    root, tick, starts = daily
    slot = _hold_a_hub_slot(root)

    assert tick(_local(0, 4, 0)) is True
    shutil.rmtree(slot)
    assert tick(_local(1, 0, 10)) is False, "a new day: yesterday's retry is gone"
    assert tick(_local(1, 4, 0)) is True
    assert starts == [None, None]
    assert "retry" not in state.last(root) and current(root) == "1.0.71"
