"""Checked card lines (lina#151) of the connected Hermes board's work cards: the writer reads the last good pass of
the reader in its own process (``lina_board_hermes.last_cards``) and keeps each line under the id and board the work
API serves the card by, so ``work_api_hermes.cards`` and ``card`` return them as ``plain``. The reader is a stand-in
holding one pass, so no Hermes CLI runs; the model is ``FakeModel``.
"""

from __future__ import annotations

import threading
from types import SimpleNamespace

import pytest

from hermes_cli import lina_board_hermes
from tests.hermes_cli._work_env import NOW, WorkEnv
from tests.hermes_cli.test_lina_board_plain import GOOD, FakeModel, _card

pytestmark = pytest.mark.platforms("posix")

NOTICE = "lina worker: 작업 OT-SIO0000000001 을 맡겼습니다"


def _task(task_id: str, status: str, body: str = "## To-dos\n- [ ] load\n", **extra) -> dict:
    return {"id": task_id, "title": "Ship the importer", "body": body, "assignee": "alpha", "status": status,
            "priority": 0, "created_by": "owner", "created_at": NOW - 9000, "started_at": NOW - 7200,
            "completed_at": None, "result": None, **extra}


def _show(task: dict, *comments: tuple[str, str], children: tuple[str, ...] = ()) -> dict:
    return {"task": task, "parents": [], "children": list(children), "events": [], "runs": [],
            "comments": [{"author": a, "body": b, "created_at": NOW - 60 + n} for n, (a, b) in enumerate(comments)]}


def _pass() -> list[dict]:
    """t_run is running: a person's comment, then the hub's notices after it. t_auto has notices only. t_done is
    done with a result. t_child is a link-child with no to-dos, which the work API does not serve."""
    return [
        _show(_task("t_run", "running"), ("alpha", "loading the second batch of photos"),
              ("lina-worker", NOTICE), ("beta", "[받음] 작업을 받았습니다")),
        _show(_task("t_auto", "running"), ("lina-worker", NOTICE), ("alpha", "  [받음] OT-SIO0000000002")),
        _show(_task("t_done", "done", completed_at=NOW - 30, result="Loaded both batches."),
              ("alpha", "loading the last batch"), children=("t_child",)),
        _show(_task("t_child", "todo", body="")),
    ]


@pytest.fixture
def work(tmp_path, monkeypatch):
    return WorkEnv(tmp_path, monkeypatch)


@pytest.fixture
def model(monkeypatch):
    from agent import auxiliary_client
    fake = FakeModel()
    monkeypatch.setattr(auxiliary_client, "call_llm", fake)
    return fake


@pytest.fixture
def board(monkeypatch):
    shows = _pass()
    src = lina_board_hermes.Source(("hermes",), None, None, None, lina_board_hermes.DEFAULT_TIMEOUT_S)
    reader = SimpleNamespace(stopped=False, src=src, part=lambda name: lina_board_hermes.Part(shows, NOW, None))
    monkeypatch.setattr(lina_board_hermes, "_READER", reader)
    return shows


def _served(shows: list[dict]) -> dict[str, dict]:
    from plugins.kanban.dashboard import work_api_hermes
    rows = {c["id"]: c["plain"] for c in work_api_hermes.cards(shows, NOW, None, None)["cards"]}
    for card_id, plain in rows.items():
        detail = work_api_hermes.card(shows, card_id, NOW)["plain"]
        assert detail == {**plain, "history": detail["history"]}
    return rows


def test_hermes_work_cards_get_the_lines_the_work_api_serves(work, model, board):
    from hermes_cli.lina_board_plain import write_board
    write_board(work.root, NOW)

    served = _served(board)
    assert set(served) == {"hermes:t_run", "hermes:t_auto", "hermes:t_done"}
    assert served["hermes:t_run"] == {"title": GOOD["title"], "now": GOOD["now"], "result": None}
    assert served["hermes:t_auto"]["now"] is None
    assert served["hermes:t_done"] == {"title": GOOD["title"], "now": None, "result": GOOD["result"]}
    now_sent = [source for field, source in model.calls if field == "now"]
    assert now_sent == ["원문: loading the second batch of photos"]


def test_a_now_answer_over_40_characters_is_refused_and_not_served(work, model, board):
    from hermes_cli.lina_board_plain import CHECKS, write_board
    long = "두 기기의 오래된 사진을 새 보관함으로 하나씩 옮기면서 빠진 것이 없는지 살피는 중"
    model.answers["now"] = long
    write_board(work.root, NOW)

    assert _served(board)["hermes:t_run"]["now"] is None
    assert CHECKS["now"](long) == "longer than 40 characters"


def test_the_standalone_web_server_s_writer_writes_the_lines_of_its_reader_s_cards(work, model, board):
    from hermes_cli.web_server_app import _start_board_lines
    stop = _start_board_lines()
    stop()  # the server is already stopping: the thread makes its one pass and ends
    for thread in [t for t in threading.enumerate() if t.name == "lina-board-lines"]:
        thread.join(30)
        assert not thread.is_alive()

    assert _served(board)["hermes:t_run"]["now"] == GOOD["now"]


def test_without_a_reader_in_this_process_no_hermes_card_is_written_and_none_is_started(work, model, tmp_path):
    from hermes_cli.lina_board_plain import hermes_card_sources
    work.config(extra=f"board:\n  hermes: {{command: [{tmp_path / 'hermes'}]}}\n")
    assert lina_board_hermes._READER is None
    assert hermes_card_sources() == []
    assert lina_board_hermes._READER is None


def test_an_own_board_card_s_now_skips_the_hub_s_notices_too(work, model):
    from hermes_cli import kanban_db as kb
    from hermes_cli import kanban_db_connect as kbc
    from hermes_cli.lina_board_plain import write_board
    tid = _card(work)
    with kbc.connect_closing() as conn:
        kb.add_comment(conn, tid, "lina-worker", NOTICE)
        kb.add_comment(conn, tid, "alpha", "[받음] 작업을 받았습니다")
    write_board(work.root, NOW)

    assert [source for field, source in model.calls if field == "now"] == [
        "원문: checking last week's backup on the photo server"]
