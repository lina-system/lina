"""The connected Hermes board's cards on the change stream: a board pass that finds a card changed since the last good
pass, and the board lines writer storing a changed line of such a card, each append one ``cards`` journal line, sent as
``source: kanban`` with the ``hermes:<id>`` the work API serves the card by, so an open app screen refetches it. The
Hermes CLI is the fake of ``test_lina_board_hermes``; the model is ``FakeModel``.
"""

from __future__ import annotations

import base64
import json
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from hermes_cli import lina_board_events, lina_board_hermes, lina_deps
from tests.hermes_cli._work_env import NOW, WorkEnv
from tests.hermes_cli.test_lina_board_events import _events, _next, _url
from tests.hermes_cli.test_lina_board_hermes import Hermes, _canned

pytestmark = pytest.mark.platforms("posix")


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    monkeypatch.setattr(lina_deps, "lina_root", lambda: env.root)
    env.goal_and_todos(todos=1)
    yield env
    lina_board_hermes.stop()


@pytest.fixture
def hermes(tmp_path, work):
    return Hermes(tmp_path, work)


@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


@pytest.fixture
def pool():
    with ThreadPoolExecutor(max_workers=1) as executor:
        yield executor


def _journal(work: WorkEnv) -> list[tuple[str, str]]:
    path = lina_board_events.journal(work.root, "cards")
    if not path.is_file():
        return []
    return [(rec["kind"], rec["id"]) for rec in map(json.loads, path.read_text(encoding="utf-8").splitlines())]


def _next_pass(running) -> None:
    """Wait for a whole pass that started after now."""
    assert running.wait(running.passes + 2, timeout=30)


def _next_events(ws, pool) -> list[tuple[str, str, str]]:
    message = _next(ws, pool)
    while not message.get("events"):  # a keepalive may land before the batch
        message = _next(ws, pool)
    return _events(message)


def test_a_comment_on_a_hermes_card_between_two_passes_is_one_kanban_event(hermes, client, pool, work, monkeypatch):
    monkeypatch.setattr(lina_board_hermes, "PASS_EVERY_S", 0.05)
    hermes.on()
    running = lina_board_hermes.reader()
    assert _journal(work) == []  # the first pass takes the baseline only

    _next_pass(running)
    assert _journal(work) == []  # nothing changed: nothing written

    data = _canned()
    with client.websocket_connect(_url()) as ws:
        _next(ws, pool)
        data["show"]["t_a1"]["comments"].append({"author": "alpha", "body": "halfway there", "created_at": NOW})
        hermes.canned(data)
        hermes.poke("t_a1")
        assert _next_events(ws, pool) == [("kanban", "changed", "hermes:t_a1")]
    _next_pass(running)
    assert _journal(work) == [("changed", "hermes:t_a1")]


def test_a_pass_announces_at_most_its_cap_of_changed_cards(hermes, work, monkeypatch):
    monkeypatch.setattr(lina_board_hermes, "ANNOUNCE_MAX", 2)
    hermes.on()
    data = _canned()
    for show in data["show"].values():
        show["task"] = dict(show["task"], priority=5)
    data["list"] = [dict(row, priority=5) for row in data["list"]]
    hermes.canned(data)
    hermes.poke()
    _next_pass(lina_board_hermes.reader())
    assert _journal(work) == [("changed", "hermes:t_a1"), ("changed", "hermes:t_g1")]


@pytest.fixture
def model(monkeypatch):
    from agent import auxiliary_client
    from tests.hermes_cli.test_lina_board_plain import FakeModel
    fake = FakeModel()
    monkeypatch.setattr(auxiliary_client, "call_llm", fake)
    return fake


def test_a_line_the_writer_stores_for_a_hermes_card_is_one_event_and_an_own_card_s_none(work, model):
    from hermes_cli.lina_board_plain import Source, write
    sources = [Source("card", "hermes:t_run", "now", "loading the second batch", "default", True),
               Source("card", "t_0000000a", "now", "checking the backup", "default", True)]
    assert write(sources, work.root, NOW)["called"] == 2
    assert _journal(work) == [("line", "hermes:t_run")]

    assert write(sources, work.root, NOW + 60)["called"] == 0  # nothing due: nothing stored, nothing announced
    model.answers["now"] = "사진 크기를 맞추는 중"
    write([sources[0]._replace(text="resizing the photos")], work.root, NOW + 120)
    assert _journal(work) == [("line", "hermes:t_run")] * 2


def _old_cursor() -> str:
    """A cursor of the format before the cards source: seven positions and seven identities."""
    return "c5." + base64.urlsafe_b64encode(b"1.0.0.0.0.0.0.-.-.-.-.-.-.-").decode().rstrip("=")


def test_a_cursor_from_before_the_cards_source_gets_one_resync_and_then_card_events(work, client, pool):
    with client.websocket_connect(_url(_old_cursor())) as ws:
        assert _next(ws, pool)["resync"] is True
        lina_board_events.announce(work.root, "cards", "changed", "hermes:t_a1")
        assert _next_events(ws, pool) == [("kanban", "changed", "hermes:t_a1")]


def test_the_cards_journal_is_cut_and_a_live_socket_past_its_end_resyncs(work, client, pool, monkeypatch):
    monkeypatch.setattr(lina_board_events, "CUT_BYTES", 200)
    path: Path = lina_board_events.journal(work.root, "cards")
    with client.websocket_connect(_url()) as ws:
        _next(ws, pool)
        for n in range(4):
            lina_board_events.announce(work.root, "cards", "changed", f"hermes:t_{n}")
        assert len(_next_events(ws, pool)) == 4
        lina_board_events.announce(work.root, "cards", "changed", "hermes:t_4")  # past the limit: cut first
        message = _next(ws, pool)
        while not message.get("resync"):
            message = _next(ws, pool)
        lina_board_events.announce(work.root, "cards", "changed", "hermes:t_5")
        assert _next_events(ws, pool) == [("kanban", "changed", "hermes:t_5")]
    assert path.with_name("cards.jsonl.1").is_file() and path.stat().st_size < 200
