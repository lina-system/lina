"""The gateway starts the LINA board-lines writer (lina#151) on every install: kanban work cards need no worker
device, so an install with goal-mode cards and no ``devices.workers`` still gets its card lines, from the start path
the gateway bootstrap runs (``_start_board_lines_writer``), on a thread of its own."""

from __future__ import annotations

import sqlite3
import threading
from types import SimpleNamespace

import pytest

from tests.hermes_cli._work_env import NOW, WorkEnv

pytestmark = pytest.mark.platforms("posix")
TITLE = "사진 보관함 백업 점검"


def _answer(task=None, *, messages, **_kwargs):
    from hermes_cli.lina_board_plain import _PROMPTS
    field = next(name for name, prompt in _PROMPTS.items() if prompt in messages[0]["content"])
    text = {"title": TITLE, "now": "지난주 백업을 확인하는 중", "result": "백업을 확인했다."}[field]
    return SimpleNamespace(choices=[SimpleNamespace(message=SimpleNamespace(content=text))])


def test_an_install_without_worker_devices_gets_card_lines_from_the_gateway_start(tmp_path, monkeypatch):
    from agent import auxiliary_client
    from gateway.run_bootstrap import _start_board_lines_writer
    from hermes_cli import kanban_db as kb
    from hermes_cli import kanban_db_connect as kbc
    from hermes_cli.lina_board_plain import writer_state
    env = WorkEnv(tmp_path, monkeypatch)  # no devices: config
    monkeypatch.setattr(auxiliary_client, "call_llm", _answer)
    kb.init_db()
    conn = kbc.connect()
    try:
        card = kb.create_task(conn, title="Weekly photo backup check", goal_mode=True, assignee="alpha")
    finally:
        conn.close()

    stop = threading.Event()
    stop.set()  # the gateway is already stopping: the thread makes its one pass and ends
    thread = _start_board_lines_writer(stop)
    thread.join(30)

    assert thread.name == "lina-board-lines" and not thread.is_alive()
    with sqlite3.connect(env.root / "board" / "plain.db") as db:
        assert db.execute("SELECT text FROM line WHERE kind = 'card' AND id = ? AND field = 'title'",
                          (card,)).fetchone() == (TITLE,)
    assert writer_state(env.root, NOW)["state"] == "ok"


def test_a_writer_that_cannot_start_leaves_the_gateway_starting(tmp_path, monkeypatch):
    """The board lines are a side feature: a raising start is logged and returns None, so the bootstrap goes on."""
    from gateway.run_bootstrap import _start_board_lines_writer
    from hermes_cli import lina_board_plain

    def _boom(_root, _stop):
        raise RuntimeError("no LINA root")

    monkeypatch.setattr(lina_board_plain, "writer_thread", _boom)
    assert _start_board_lines_writer(threading.Event()) is None


def test_the_gateway_s_board_lines_writer_never_holds_the_profile_lock(tmp_path, monkeypatch):
    """A live writer pass must not keep the gateway's profile lock after shutdown cleanup (the runtime writer drain)."""
    from gateway.run_bootstrap import _start_board_lines_writer
    from gateway.runtime_ownership import process_ownership
    from hermes_cli import lina_board_plain
    WorkEnv(tmp_path, monkeypatch)
    in_pass, release = threading.Event(), threading.Event()
    monkeypatch.setattr(lina_board_plain, "write_board", lambda: (in_pass.set(), release.wait(20)))
    home = tmp_path / "profile"
    stop = threading.Event()
    thread = _start_board_lines_writer(stop)
    try:
        assert in_pass.wait(10)
        assert thread.name == "lina-board-lines"
        assert not any(t.name == "lina-board-lines" for t in process_ownership._writers)
        process_ownership.reserve([home])
        process_ownership.close()
        assert not process_ownership.owns(home)
    finally:
        stop.set()
        release.set()
        thread.join(10)
    assert not thread.is_alive()
