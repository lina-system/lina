"""A run's end waits on a Hermes card that is not ready for its wake without flooding the hub (lina#473).

A schedule whose other open runs are all the hub's own is woken by the last of them, a judged or superseded run does
not wait on a running card, and a wait is said once per reason, retried at growing gaps and given up after 24 h.
Every Hermes command is the fake ``hermes`` of ``test_worker_callers``; time is the hub module's clock."""

from __future__ import annotations

import pytest

from hermes_cli.worker import hub as hub_module
from tests.hermes_cli import test_worker_callers as callers
from tests.hermes_cli import test_worker_kanban_link_legacy as legacy_tests
from tests.hermes_cli.test_worker_callers import _scheduled, _woke

kanban, board, legacy = callers.kanban, callers.board, legacy_tests.legacy

START = 1_800_000_000.0
GAP_S, EXPIRE_S = 120, 24 * 3600  # the issue's contract: retried every 120 s at most, given up after 24 h


@pytest.fixture
def clock(monkeypatch):
    now = [START]
    monkeypatch.setattr(hub_module.time, "time", lambda: now[0])
    return now


def _kinds(hub, run: str, *kinds: str) -> list[dict]:
    return [r for r in hub.store.receipts_of(run) if r["kind"] in kinds]


def _reads(calls) -> int:
    return sum(call["verb"] == "show" for call in calls())


def _log_lines(hub, run: str) -> int:
    log = hub.store.log_file
    return sum(run in line for line in log.read_text(encoding="utf-8").splitlines()) if log.is_file() else 0


@pytest.mark.platforms("posix")
def test_a_schedule_on_hub_runs_only_is_woken_by_the_last_end_and_the_first_end_retries_nothing(board, legacy,
                                                                                                 clock):
    # Given a legacy tool configured and a schedule naming two runs this hub owns.
    hub, submit, show, calls = board
    first, second = submit("a.md"), submit("b.md")
    show("scheduled", [_scheduled(f"workers={first},{second}")])

    # When the first ends and the hub ticks on.
    hub.notify(first, "done", "1")
    reads = _reads(calls)
    for _ in range(10):
        clock[0] += 30
        hub.tick()

    # Then that end is over without a wait or a retry, and the later end wakes the card once.
    assert not hub.store.load(first).get("card_pending")
    assert not _kinds(hub, first, "card_notice_failed", "card_notice_waiting")
    assert _reads(calls) == reads and _woke(calls) == 0
    hub.notify(second, "done", "1")
    assert _woke(calls) == 1


@pytest.mark.platforms("posix")
def test_a_legacy_run_still_open_on_the_schedule_holds_the_wake(board, legacy, clock):
    # Given a schedule naming two hub runs and one legacy run the old tool says is running.
    hub, submit, show, calls = board
    answer, _, _ = legacy
    first, second, old = submit("a.md"), submit("b.md"), "OT-OLD1106000000"
    show("scheduled", [_scheduled(f"workers={first},{second},{old}")])

    # When the hub runs end while the legacy one is open, then their ends wait for it.
    hub.notify(first, "done", "1")
    hub.notify(second, "done", "1")
    assert _woke(calls) == 0 and hub.store.load(second).get("card_pending")
    assert [r["reason"] for r in _kinds(hub, second, "card_notice_waiting")] == ["other runs open"]
    assert not _kinds(hub, second, "card_notice_failed")

    # And once the legacy run ends, the next try wakes the card.
    answer("done")
    clock[0] += 30
    hub.tick()
    assert _woke(calls) == 1 and not hub.store.load(second).get("card_pending")


@pytest.mark.parametrize("settle", ["verdict", "superseded"])
def test_a_judged_or_superseded_run_does_not_wait_on_a_running_card(board, clock, settle):
    # Given a run whose end waits on a card that has not scheduled itself.
    hub, submit, show, calls = board
    run = submit()
    show("running", [])
    hub.notify(run, "done", "1")
    assert hub.store.load(run).get("card_pending")

    # When the run gets its verdict, or a follow-up starts a later run.
    if settle == "verdict":
        hub.verdict(run, True, "checked")
    else:
        hub.store.update(run, runs=2)
    clock[0] += 30
    hub.tick()

    # Then the end is over, noticed without a wake.
    assert not hub.store.load(run).get("card_pending")
    assert _woke(calls) == 0
    assert _kinds(hub, run, "card_noticed")[-1]["woke"] is False


def test_a_card_scheduled_on_a_judged_run_is_still_woken(board, clock):
    hub, submit, show, calls = board
    run = submit()
    hub.verdict(run, False, "missing a test")
    show("scheduled", [_scheduled(f"workers={run}")])
    hub.notify(run, "done", "1")
    assert _woke(calls) == 1


def test_a_wait_across_an_hour_and_its_expiry_stays_within_a_fixed_bound(board, clock):
    # Given a run whose card stays running for a day.
    hub, submit, show, calls = board
    run = submit()
    show("running", [])
    reads = _reads(calls)
    hub.notify(run, "done", "1")

    # When the hub ticks every 10 s for an hour.
    ticks = 360
    for _ in range(ticks):
        clock[0] += 10
        hub.tick()

    # Then the card reads and log lines follow the gaps, not the ticks, and the wait is said once.
    tries = 2 + 3600 // GAP_S
    assert _reads(calls) - reads <= tries < ticks
    assert _log_lines(hub, run) <= tries + 3
    assert len(_kinds(hub, run, "card_notice_waiting")) == 1
    assert not _kinds(hub, run, "card_notice_failed")

    # When the day is over, then one expiry receipt, one comment on the card, and the end is noticed unwoken.
    clock[0] = START + EXPIRE_S
    hub.tick()
    after = len(calls())
    for _ in range(ticks):
        clock[0] += 10
        hub.tick()
    assert len(_kinds(hub, run, "card_wake_expired")) == 1
    said = [call for call in calls() if call["verb"] == "comment" and "the wake was not sent" in call["argv"][-1]]
    assert len(said) == 1
    assert _kinds(hub, run, "card_noticed")[-1]["woke"] is False
    assert not hub.store.load(run).get("card_pending")
    assert len(calls()) == after and _woke(calls) == 0


def test_an_end_noticed_on_a_card_is_never_tried_on_that_card_again(board, clock):
    # Given a run whose end was noticed on its card (the card was woken).
    hub, submit, show, calls = board
    run = submit()
    show("scheduled", [_scheduled(f"workers={run}")])
    hub.notify(run, "done", "1")
    assert _woke(calls) == 1
    seen = len(calls())

    # When the same end is reported again and the card goes back to running across many ticks.
    show("running", [])
    for _ in range(20):
        hub.notify(run, "done", "1")
        clock[0] += 30
        hub.tick()

    # Then the card is not read or written for that end again.
    assert len(calls()) == seen
    assert len(_kinds(hub, run, "card_noticed")) == 1
    assert not _kinds(hub, run, "card_notice_waiting", "card_notice_failed")
