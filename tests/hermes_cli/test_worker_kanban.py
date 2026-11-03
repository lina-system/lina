"""A ``lina worker`` run started from inside a work card (lina#35, #107): with ``HERMES_KANBAN_TASK`` set on a scratch
board, submit records the run ID and runner on that card as an event, the card's session reads the run back by that
ID through a fresh hub (a restart), and the end notice lands on the card as a comment."""

import json
from pathlib import Path

import pytest

from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli.worker import serve
from hermes_cli.worker.hub import Hub
from tests.hermes_cli import _worker_fakes as fakes


@pytest.fixture
def board(tmp_path, monkeypatch):
    home = tmp_path / "hermes_home"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    for var in ("HERMES_KANBAN_DB", "HERMES_KANBAN_WORKSPACES_ROOT", "HERMES_KANBAN_HOME", "HERMES_KANBAN_BOARD"):
        monkeypatch.delenv(var, raising=False)
    kb._INITIALIZED_PATHS.clear()
    conn = kbc.connect()
    card = kb.create_task(conn, title="work card: ship the worker")
    conn.close()
    monkeypatch.setenv("HERMES_KANBAN_TASK", card)
    return card


def test_a_run_from_a_card_is_recorded_on_it_and_its_end_notice_is_a_comment(board, tmp_path, monkeypatch):
    base, fake = tmp_path / "lina", tmp_path / "fake"
    argv = fakes.install_fake_cli(fake)
    monkeypatch.setenv("FAKE_CLI_LOG", str(fake / "cli.jsonl"))
    fakes.write_config(base, f"fakecli: {json.dumps({'type': 'command', 'argv': argv})}\n")
    root = base / "worker"
    launcher = fakes.ThreadLauncher().install(monkeypatch)
    serve.serve_once(root)
    lines: list[str] = []
    Hub(root, out=lines.append).submit({"briefs": [fakes.brief(tmp_path, "SCENARIO:done")], "mode": "ulw",
                                        "bot": "sion", "runner": "fakecli", "wait": None})
    task_id = lines[0].split()[0]

    with kbc.connect_closing() as conn:
        started = [e for e in kb.list_events(conn, board) if e.kind == "worker_run"]
    assert len(started) == 1 and started[0].payload["run_id"] == task_id
    assert started[0].payload["runner"] == "fakecli"

    serve.serve_once(root)
    restarted = Hub(root, out=lines.append)
    fakes.until(lambda: "done:1" in restarted.store.load(task_id)["notified"], what="the run ended")
    launcher.join()
    assert restarted.store.load(task_id)["topic"] == board
    before = len(lines)
    assert restarted.status(task_id) == 0
    assert lines[before].startswith(task_id) and " · done · run 1" in lines[before]

    with kbc.connect_closing() as conn:
        comments = kb.list_comments(conn, board)
        ended = [e for e in kb.list_events(conn, board) if e.kind == "worker_run_ended"]
    assert len(comments) == 1 and comments[0].author == "lina-worker"
    assert task_id in comments[0].body and "lina worker accept" in comments[0].body
    assert ended[0].payload == {**ended[0].payload, "run_id": task_id, "state": "done", "run": "1"}


def test_a_run_outside_a_card_writes_nothing_to_any_board(board, tmp_path, monkeypatch):
    monkeypatch.delenv("HERMES_KANBAN_TASK")
    base, fake = tmp_path / "lina", tmp_path / "fake"
    argv = fakes.install_fake_cli(fake)
    monkeypatch.setenv("FAKE_CLI_LOG", str(fake / "cli.jsonl"))
    fakes.write_config(base, f"fakecli: {json.dumps({'type': 'command', 'argv': argv})}\n")
    fakes.ThreadLauncher().install(monkeypatch)
    lines: list[str] = []
    Hub(base / "worker", out=lines.append).submit({"briefs": [fakes.brief(tmp_path, "SCENARIO:done")], "mode": "ulw",
                                                   "topic": "loose", "bot": "sion", "runner": "fakecli",
                                                   "wait": None})
    with kbc.connect_closing() as conn:
        assert not [e for e in kb.list_events(conn, board) if e.kind.startswith("worker_run")]
