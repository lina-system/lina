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
    """The board lines are a side feature: a raising start is logged and the cron and housekeeping start returns."""
    import asyncio

    from cron import scheduler_provider, scheduler_thread
    from gateway import run_bootstrap

    class _Thread:
        def __init__(self, *_args, **_kwargs):
            pass

        def start(self):
            pass

    def _boom(_stop):
        raise RuntimeError("no LINA root")

    monkeypatch.setattr(run_bootstrap, "_start_board_lines_writer", _boom)
    monkeypatch.setattr(scheduler_thread, "SupervisedTickerThread", _Thread)
    monkeypatch.setattr(scheduler_provider, "scheduler_for_profile_mode",
                        lambda _p, multiplex_profiles=False: scheduler_provider.InProcessCronScheduler())
    import gateway.run as gw_run
    from gateway.runtime_ownership import process_ownership
    monkeypatch.setattr(process_ownership, "start_writer", lambda _thread: None)  # nothing really starts
    monkeypatch.setattr(gw_run, "_cron_tick_profile_homes", lambda _cfg: [("default", tmp_path)])
    runner = SimpleNamespace(config={}, adapters={}, _profile_adapters=None, _primary_profile_name="default",
                             _draining=False, _external_drain_active=False)

    async def _go():
        return run_bootstrap._start_gateway_start_cron_and_housekeeping(runner)

    stop, _provider, _cron, _housekeeping = asyncio.run(_go())
    assert isinstance(stop, threading.Event)
