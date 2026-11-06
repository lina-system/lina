"""lina#295 on the worker's tool surface: a card woken only to judge a run calls ``kanban_complete``
once it has judged it, and goes back to what it waited on instead of finishing; while a run that
woke it has no verdict, the tool error names that run and nothing changes."""
import json
import time
from pathlib import Path

import pytest

from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli.worker import kanban_link, settings
from tools import kanban_tools as kt

RUN = "OT-AGT1107120000"


@pytest.fixture
def board(monkeypatch, tmp_path):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("HERMES_PROFILE", "test-worker")
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    for var in ("HERMES_SESSION_ID", "HERMES_KANBAN_TASK", "HERMES_KANBAN_RUN_ID", "HERMES_KANBAN_BOARD",
                "HERMES_KANBAN_DB", "LINA_HOME"):
        monkeypatch.delenv(var, raising=False)
    kb._INITIALIZED_PATHS.clear()
    kb.init_db()
    with kbc.connect_closing() as conn:
        yield conn


def _job(card: str, **fields) -> None:
    jobs = settings.hub_dir(settings.root()) / "jobs"
    jobs.mkdir(parents=True, exist_ok=True)
    job = {"id": RUN, "bot": "test-worker", "state": "done", "verdict": None, "card": {"task": card, "board": None},
           "created": time.time(), **fields}
    (jobs / f"{RUN}.json").write_text(json.dumps(job), encoding="utf-8")


def _woken_and_claimed(conn, monkeypatch, card: str) -> int:
    """The run starts and ends on *card*; the woken card is claimed by its worker."""
    _job(card, state="running")
    kanban_link.record_run({"task": card, "board": None}, {"id": RUN, "runner": "fakecli"}, {"kind": "lina"})
    _job(card)
    kanban_link.record_end({"task": card, "board": None}, {"id": RUN, "runner": "fakecli"}, "done", "1",
                           f"run {RUN} ended")
    assert kb.get_task(conn, card).status == "ready"
    claimed = kb.claim_task(conn, card, claimer="test-worker")
    assert claimed is not None
    monkeypatch.setenv("HERMES_KANBAN_TASK", card)
    monkeypatch.setenv("HERMES_KANBAN_RUN_ID", str(claimed.current_run_id))
    return claimed.current_run_id


def _complete(card: str) -> dict:
    return json.loads(kt._handle_complete({"task_id": card, "summary": "judged the run"}))


def test_a_card_behind_an_open_parent_goes_back_to_todo_after_its_verdict(board, monkeypatch):
    parent = kb.create_task(board, title="parent", assignee="op")
    card = kb.create_task(board, title="child", assignee="test-worker", parents=[parent])
    run_id = _woken_and_claimed(board, monkeypatch, card)

    refused = _complete(card)
    assert RUN in refused["error"] and "(done)" in refused["error"]
    assert kb.get_task(board, card).status == "running"

    _job(card, verdict="accepted")
    out = _complete(card)
    assert out.get("returned_to") == "todo", out
    assert kb.get_task(board, card).status == "todo"
    assert kb.get_run(board, run_id).outcome == "returned_to_wait"


def test_a_scheduled_card_goes_back_to_its_schedule_not_to_done(board, monkeypatch):
    card = kb.create_task(board, title="waits on a time", assignee="test-worker")
    assert kb.schedule_task(board, card, reason="until 2099-01-01", wait_for={"kind": "until", "at": 4070908800})
    _woken_and_claimed(board, monkeypatch, card)

    _job(card, verdict="rejected")
    out = _complete(card)
    assert out.get("returned_to") == "scheduled", out
    assert kb.get_task(board, card).status == "scheduled"
