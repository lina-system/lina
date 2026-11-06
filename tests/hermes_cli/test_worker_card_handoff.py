"""Hermes terminals hand writes to the hub and scheduling to the model (lina#270)."""

import json
import sys

import pytest

from hermes_cli.worker import kanban_link
from hermes_cli.worker.hub import Hub
from tests.hermes_cli.test_worker_hermes_card import _cli, _config, FAKE_KANBAN


@pytest.fixture
def handoff(tmp_path, monkeypatch):
    base = tmp_path / "lina"
    fake = tmp_path / "hermes.py"
    fake.write_text(FAKE_KANBAN, encoding="utf-8")
    log = tmp_path / "kanban.jsonl"
    _config(base, [sys.executable, str(fake)])
    brief = tmp_path / "brief.md"
    brief.write_text("Do it", encoding="utf-8")
    monkeypatch.setenv("LINA_HOME", str(base))
    monkeypatch.setenv("HERMES_HOME", str(base))
    monkeypatch.setenv("FAKE_KANBAN_LOG", str(log))
    monkeypatch.setenv("HERMES_DELEGATED_CHILD_CONTEXT", str(tmp_path / "board"))
    monkeypatch.delenv("HERMES_KANBAN_TASK", raising=False)
    card = {"task": "t_card", "board": "ops", "kind": "hermes"}
    hub = Hub(base / "worker")
    assert _cli(["submit", "--brief", str(brief), "--card", card["task"], "--board", card["board"]]) == 0
    job, = hub.store.all()

    def calls():
        return [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []

    return hub, job["id"], card, calls


def test_start_is_only_written_by_hub_tick_and_retried_once(handoff, tmp_path, monkeypatch):
    hub, run, _, calls = handoff
    assert calls() == []
    hub.tick()
    assert calls() == []
    monkeypatch.delenv("HERMES_DELEGATED_CHILD_CONTEXT")  # separate hub environment
    fail = tmp_path / "fail"
    fail.mkdir()
    (fail / "comment").touch()
    monkeypatch.setenv("FAKE_KANBAN_FAIL_ONCE", str(fail))
    hub.tick()
    assert hub.store.load(run).get("card_start_pending")
    hub.tick()
    hub.tick()
    assert [c["ok"] for c in calls()] == [False, True]
    assert not hub.store.load(run).get("card_start_pending")


def test_wait_only_records_runs_and_instructs_model_schedule(handoff, capsys):
    hub, run, card, calls = handoff
    capsys.readouterr()
    before = calls()
    assert _cli(["wait", "--card", card["task"], "--board", card["board"]]) == 0
    assert calls() == before
    assert kanban_link._parked_path(hub.store, card).read_text().splitlines()[0] == run
    instruction = capsys.readouterr().out
    assert "kanban_schedule" in instruction and f'workers=["{run}"]' in instruction
    assert f"waiting on lina worker {run}" in instruction


@pytest.mark.parametrize("status", ["running", "ready", "done", "archived", "blocked"])
def test_wake_waits_for_scheduled_and_never_unblocks_terminal_card(handoff, monkeypatch, status):
    hub, run, card, calls = handoff
    mark = kanban_link._parked_path(hub.store, card)
    mark.parent.mkdir(parents=True, exist_ok=True)
    mark.write_text(run + "\n0\n", encoding="utf-8")
    monkeypatch.delenv("HERMES_DELEGATED_CHILD_CONTEXT")  # hub report/tick, not the terminal
    monkeypatch.setenv("FAKE_KANBAN_STATUS", status)
    hub.notify(run, "done", "1")
    assert not any(c["argv"][3] == "unblock" for c in calls())
    if status in ("running", "ready"):
        assert mark.exists() and hub.store.load(run).get("card_pending")
        monkeypatch.setenv("FAKE_KANBAN_STATUS", "scheduled")
        hub.tick()
        hub.tick()
        hub.notify(run, "done", "1")
        assert sum(c["argv"][3] == "unblock" for c in calls()) == 1
    assert not mark.exists()
    assert not hub.store.load(run).get("card_pending")
