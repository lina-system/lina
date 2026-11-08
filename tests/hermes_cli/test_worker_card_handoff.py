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


def test_card_add_keeps_submit_record_and_adds_one_receipt(handoff, capsys):
    hub, run, card, calls = handoff
    before = hub.store.receipts_of(run)
    assert _cli(["card", run, "t_second", "--board", "other"]) == 0
    job = hub.store.load(run)
    assert job["cards"] == [{"id": card["task"], "board": "ops"}, {"id": "t_second", "board": "other"}]
    added, = hub.store.receipts_of(run)[len(before):]
    assert (added["kind"], added["card"]) == ("card_linked", {"task": "t_second", "board": "other", "kind": "hermes"})
    capsys.readouterr()
    assert _cli(["card", run]) == 0
    assert [line.split() for line in capsys.readouterr().out.splitlines()] == [
        [card["task"], "ops", "(hermes)"], ["t_second", "other", "(hermes)"]]
    assert calls() == []  # linking in a caller never writes to the external board


def test_card_drop_removes_the_last_card_and_records_once(handoff):
    hub, run, card, _ = handoff
    before = hub.store.receipts_of(run)
    assert _cli(["card", run, "--drop", card["task"]]) == 0
    job = hub.store.load(run)
    assert job["card"] is None and job["cards"] == []
    assert not job.get("card_start_pending")
    dropped, = hub.store.receipts_of(run)[len(before):]
    assert (dropped["kind"], dropped["card"]) == ("card_dropped", card)


@pytest.mark.parametrize("args", [["t_card", "--board", "ops"], ["--drop", "t_absent"]])
def test_card_noop_preserves_job_and_receipts_byte_for_byte(handoff, capsys, args):
    hub, run, _, _ = handoff
    paths = [hub.store.path(run), hub.store.receipts]
    before = [(path.read_bytes(), path.stat().st_mtime_ns) for path in paths]
    capsys.readouterr()
    assert _cli(["card", run, *args]) == 0
    assert len(capsys.readouterr().out.splitlines()) == 1
    assert [(path.read_bytes(), path.stat().st_mtime_ns) for path in paths] == before


def test_card_end_after_drop_reaches_only_remaining_cards(handoff, monkeypatch):
    hub, run, card, calls = handoff
    assert _cli(["card", run, "t_second", "--board", "ops"]) == 0
    assert _cli(["card", run, "t_third", "--board", "ops"]) == 0
    assert _cli(["card", run, "--drop", card["task"]]) == 0
    monkeypatch.delenv("HERMES_DELEGATED_CHILD_CONTEXT")
    hub.notify(run, "done", "1")
    hub.notify(run, "done", "1")
    hub.tick()
    comments = [c["argv"][4] for c in calls() if c["argv"][3] == "comment"]
    assert comments == ["t_second", "t_third"]
    assert all(c["argv"][4] != card["task"] for c in calls())


def test_card_add_twice_does_not_add_another_link_or_receipt(handoff, capsys):
    hub, run, _, _ = handoff
    args = ["card", run, "t_second", "--board", "ops"]
    assert _cli(args) == 0
    before = hub.store.path(run).read_bytes(), hub.store.receipts.read_bytes()
    capsys.readouterr()
    assert _cli(args) == 0
    assert len(capsys.readouterr().out.splitlines()) == 1
    assert (hub.store.path(run).read_bytes(), hub.store.receipts.read_bytes()) == before


def test_card_drop_during_pending_end_does_not_retarget_old_work(handoff, monkeypatch):
    hub, run, card, calls = handoff
    assert _cli(["card", run, "t_second", "--board", "ops"]) == 0
    monkeypatch.delenv("HERMES_DELEGATED_CHILD_CONTEXT")
    monkeypatch.setenv("FAKE_KANBAN_STATUS", "running")
    hub.notify(run, "done", "1")  # first card's wake is pending after its comment
    assert hub.store.load(run).get("card_pending")
    assert [c["argv"][4] for c in calls() if c["argv"][3] == "comment"].count("t_second") == 1
    assert _cli(["card", run, "--drop", card["task"]]) == 0
    before = len(calls())
    monkeypatch.setenv("FAKE_KANBAN_STATUS", "scheduled")
    hub.tick()
    hub.tick()
    assert not [c for c in calls()[before:] if c["argv"][3] == "comment"]
    assert all(c["argv"][4] == "t_second" for c in calls()[before:])
    assert not hub.store.load(run).get("card_pending")


def test_card_add_after_empty_uses_submit_board_default(handoff, monkeypatch):
    hub, run, card, _ = handoff
    assert _cli(["card", run, "--drop", card["task"]]) == 0
    monkeypatch.setenv("HERMES_KANBAN_BOARD", "inherited")
    assert _cli(["card", run, "t_second"]) == 0
    assert hub.store.load(run)["card"] == {"task": "t_second", "board": "inherited", "kind": "hermes"}
    assert hub.store.load(run)["cards"] == [{"id": "t_second", "board": "inherited"}]


@pytest.mark.parametrize("failure", ["running", "ready", "comment"])
def test_card_targets_progress_independently_and_retry_only_pending(handoff, tmp_path, monkeypatch, failure):
    hub, run, first, calls = handoff
    second = {"task": "t_second", "board": "ops", "kind": "hermes"}
    assert _cli(["card", run, second["task"], "--board", second["board"]]) == 0
    monkeypatch.delenv("HERMES_DELEGATED_CHILD_CONTEXT")
    hub.tick()  # finish submit's start comment before testing end delivery
    for card in (first, second):
        kanban_link.park(hub.store, card, [run])
    fake = tmp_path / "hermes.py"
    fake.write_text(FAKE_KANBAN.replace(
        'os.environ.get("FAKE_KANBAN_STATUS", "scheduled")',
        'json.loads(os.environ.get("FAKE_CARD_STATUSES", "{}" )).get(rest[0], "scheduled")'), encoding="utf-8")
    monkeypatch.setenv("FAKE_CARD_STATUSES", json.dumps({first["task"]: failure if failure != "comment" else "scheduled"}))
    if failure == "comment":
        fail = tmp_path / "fail-comment"
        fail.mkdir()
        (fail / "comment").touch()
        monkeypatch.setenv("FAKE_KANBAN_FAIL_ONCE", str(fail))
    start = len(calls())

    result = hub.notify(run, "done", "1")

    assert "retried" in result
    entry, = hub.store.load(run)["card_pending"].values()
    first_state = entry.get("card_progress", {}).get(json.dumps(first, sort_keys=True), {"done": []})
    second_state = entry.get("card_progress", {}).get(json.dumps(second, sort_keys=True), {})
    assert not first_state.get("noticed")
    assert second_state.get("noticed"), "a pending first card must not block the second card"
    assert "wake" not in first_state["done"]
    assert ("comment" in first_state["done"]) == (failure != "comment")
    delivered = [c["argv"] for c in calls()[start:] if c["ok"]]
    assert sum(a[3] == "comment" and a[4] == second["task"] for a in delivered) == 1
    assert sum(a[3] == "unblock" and a[-1] == second["task"] for a in delivered) == 1
    assert all(f"delivery {run}:1:run.completed" in a[-1] for a in delivered if a[3] == "comment")

    monkeypatch.setenv("FAKE_CARD_STATUSES", "{}")
    Hub(hub.worker_root).tick()  # persisted progress survives a new hub instance
    hub.tick()

    assert not hub.store.load(run).get("card_pending")
    delivered = [c["argv"] for c in calls()[start:] if c["ok"]]
    for card in (first, second):
        assert sum(a[3] == "comment" and a[4] == card["task"] for a in delivered) == 1
        assert sum(a[3] == "unblock" and a[-1] == card["task"] for a in delivered) == 1
    noticed = [r["card"]["task"] for r in hub.store.receipts_of(run) if r["kind"] == "card_noticed"]
    assert noticed == [second["task"], first["task"]]
