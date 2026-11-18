"""A work card's step record (``plain.history``, G138) over a scratch LINA root with a fake model: a now line replaced
by a new sentence is recorded in the past tense the model wrote with it, a repeated sentence is one step, a refused
past tense is asked again and never recorded, an ended card records its last line, earlier steps are backfilled once
at their sources' times, an older cache keeps its lines, and the card detail serves the record.
"""

from __future__ import annotations

import json
import sqlite3
from pathlib import Path
from types import SimpleNamespace

import pytest

from tests.hermes_cli import test_lina_board_plain as plain_tests
from tests.hermes_cli._work_env import NOW, WorkEnv
from tests.hermes_cli.test_lina_board_plain import GOOD, _answering, _card, _rows, _valid, said

pytestmark = pytest.mark.platforms("posix")

CARD = "t_0000000a"
client = plain_tests.client
A, A_PAST = "사진 서버 백업을 확인하는 중", "사진 서버 백업을 확인함"
B, B_PAST = "빌드 기기 디스크를 비우는 중", "빌드 기기 디스크를 비움"


@pytest.fixture
def work(tmp_path, monkeypatch):
    return WorkEnv(tmp_path, monkeypatch)


def _now(source: str):
    from hermes_cli.lina_board_plain import Source
    return Source("card", CARD, "now", source, "default", True)


def _history(work) -> list[dict]:
    from hermes_cli.lina_board_plain_history import lookup
    return lookup("card", CARD, work.root, board="default")


def test_a_replaced_now_line_is_recorded_from_when_it_was_stored_to_when_it_was_replaced(work, monkeypatch):
    from hermes_cli.lina_board_plain import write
    _answering(monkeypatch, [f"{A}\n{A_PAST}", f"{B}\n{B_PAST}"])
    write([_now("checking last week's backup")], work.root, NOW)
    assert _history(work) == []

    write([_now("clearing the build machine disks")], work.root, NOW + 60)

    assert _history(work) == [{"at": NOW, "until": NOW + 60, "text": A_PAST}]
    assert _rows(work)[("card", CARD, "now")] == (B, None)


def test_the_same_sentence_again_is_one_step_that_lasts_longer(work, monkeypatch):
    from hermes_cli.lina_board_plain import write
    reworded = "사진 서버의 백업을 확인하는 중"  # another sentence whose past tense is the same step
    _answering(monkeypatch, [f"{A}\n{A_PAST}", f"{A}\n{A_PAST}", f"{reworded}\n{A_PAST}", f"{B}\n{B_PAST}"])
    write([_now("checking last week's backup")], work.root, NOW)
    write([_now("still checking last week's backup")], work.root, NOW + 60)
    assert _history(work) == []
    write([_now("checking the backup of the photo server")], work.root, NOW + 120)
    assert _history(work) == [{"at": NOW, "until": NOW + 120, "text": A_PAST}]

    write([_now("clearing the build machine disks")], work.root, NOW + 180)

    assert _history(work) == [{"at": NOW, "until": NOW + 180, "text": A_PAST}]


@pytest.mark.parametrize("past", ["Checked the photo backup", "사진 서버 백업을 " + "가" * 30 + "확인함",
                                  "사진 서버 백업을 확인하는 중", "알파가 사진 서버 백업을 확인함"])
def test_a_refused_past_tense_is_asked_again_and_never_recorded_but_the_now_line_is_stored(work, monkeypatch, past):
    from hermes_cli.lina_board_plain import RETRIES, check_past, write
    names = frozenset({"알파"})
    assert check_past(past, names) is not None
    sent = _answering(monkeypatch, [f"{A}\n{past}"] * (RETRIES + 1) + [f"{B}\n{B_PAST}"])

    assert write([_now("checking last week's backup")], work.root, NOW, names=names) == \
        {"called": 1, "failed": 0, "pending": 0}

    assert len(sent) == RETRIES + 1 and "과거형" in sent[1][-1]["content"]
    assert _rows(work)[("card", CARD, "now")] == (A, None)
    write([_now("clearing the build machine disks")], work.root, NOW + 60, names=names)
    assert _history(work) == []


def test_a_past_tense_rewritten_after_its_refusal_is_recorded(work, monkeypatch):
    from hermes_cli.lina_board_plain import write
    _answering(monkeypatch, [f"{A}\n{A}", f"{A}\n{A_PAST}", f"{B}\n{B_PAST}"])
    write([_now("checking last week's backup")], work.root, NOW)
    write([_now("clearing the build machine disks")], work.root, NOW + 60)
    assert _history(work) == [{"at": NOW, "until": NOW + 60, "text": A_PAST}]


def test_an_ended_card_records_its_last_now_line_and_shows_none(work, monkeypatch):
    from hermes_cli.lina_board_plain import lookup, write
    _answering(monkeypatch, [f"{A}\n{A_PAST}"])
    write([_now("checking last week's backup")], work.root, NOW)

    write([_now("")], work.root, NOW + 60)

    assert _history(work) == [{"at": NOW, "until": NOW + 60, "text": A_PAST}]
    assert lookup("card", [CARD], work.root, board="default")[CARD]["now"] is None
    write([_now("")], work.root, NOW + 120)
    assert len(_history(work)) == 1


def _commented_card(work, *comments: tuple[int, str]) -> str:
    from hermes_cli import kanban_db as kb
    from hermes_cli import kanban_db_connect as kbc
    kb.init_db()
    with kbc.connect_closing() as conn:
        tid = kb.create_task(conn, title="Weekly photo backup check", goal_mode=True, assignee="alpha")
        for at, body in comments:
            work.clock[0] = float(at)
            kb.add_comment(conn, tid, "alpha", body)
    work.clock[0] = float(NOW)
    return tid


def test_earlier_steps_are_backfilled_once_at_their_sources_times(work, monkeypatch):
    from hermes_cli import lina_board_plain_history as history
    from hermes_cli.lina_board_plain import write_board
    tid = _commented_card(work, (NOW - 300, "cleared the build disks"), (NOW - 200, "reviewed"),
                          (NOW - 100, "checked the photo backup"))
    sent = _answering(monkeypatch, [GOOD["title"], f"{A}\n{A_PAST}",
                                    "1. 빌드 기기 디스크를 비움\n2. Passed the review\n3. 사진 서버 백업을 확인함"])

    write_board(work.root, NOW)

    assert len(sent) == 3 and "1. cleared the build disks" in sent[2][1]["content"]
    assert history.lookup("card", tid, work.root, board="default") == [
        {"at": NOW - 100, "until": None, "text": A_PAST}, {"at": NOW - 300, "until": None, "text": "빌드 기기 디스크를 비움"}]
    write_board(work.root, NOW + 60)
    assert len(sent) == 3


def test_a_card_with_no_step_before_the_record_is_marked_without_a_call(work, monkeypatch):
    from hermes_cli.lina_board_plain import write_board
    _commented_card(work, (NOW, "checked the photo backup"))
    sent = _answering(monkeypatch, [GOOD["title"], f"{A}\n{A_PAST}"])
    write_board(work.root, NOW)
    write_board(work.root, NOW + 60)
    assert len(sent) == 2


def test_an_older_cache_gains_the_record_and_keeps_its_lines(work, monkeypatch):
    from hermes_cli.lina_board_plain import SCHEMA_VERSION, lookup, write
    path = work.root / "board" / "plain.db"
    path.parent.mkdir(parents=True)
    with sqlite3.connect(path) as db:
        db.execute("CREATE TABLE line (kind TEXT NOT NULL, board TEXT NOT NULL, id TEXT NOT NULL, field TEXT NOT NULL, "
                   "source_hash TEXT NOT NULL, text TEXT, error TEXT, at INTEGER NOT NULL, "
                   "PRIMARY KEY (kind, board, id, field))")
        db.execute("CREATE TABLE writer (key TEXT PRIMARY KEY, value INTEGER)")
        db.execute("INSERT INTO writer VALUES ('ask_revision', 1)")
        db.execute("INSERT INTO line VALUES ('card', 'default', 't_0000000b', 'now', 'h', ?, NULL, ?)", (B, NOW - 60))
        db.execute(f"PRAGMA user_version = {SCHEMA_VERSION}")
    assert _history(work) == []
    _answering(monkeypatch, [f"{A}\n{A_PAST}"])

    write([_now("checking last week's backup")], work.root, NOW)

    assert lookup("card", ["t_0000000b"], work.root, board="default")["t_0000000b"]["now"] == B
    assert _rows(work)[("card", "t_0000000b", "now")] == (B, None)
    with sqlite3.connect(path) as db:
        assert db.execute("SELECT now, past, at FROM history_open").fetchall() == [(A, A_PAST, NOW)]


def test_the_card_detail_serves_the_record_newest_first_and_the_list_does_not(work, monkeypatch, client):
    from hermes_cli import kanban_db as kb
    from hermes_cli import kanban_db_connect as kbc
    from hermes_cli.lina_board_plain import write_board
    tid = _card(work)
    _answering(monkeypatch, [GOOD["title"], f"{A}\n{A_PAST}", f"{B}\n{B_PAST}"])
    write_board(work.root, NOW)
    with kbc.connect_closing() as conn:
        kb.add_comment(conn, tid, "alpha", "clearing the build machine disks")
    write_board(work.root, NOW + 60)

    card = _valid("work-client.yaml", "WorkCardDetail", client.get(f"/api/plugins/kanban/work/cards/{tid}").json())

    assert card["plain"] == {"title": GOOD["title"], "now": B, "result": None,
                             "history": [{"at": NOW, "until": NOW + 60, "text": A_PAST}]}
    listed = _valid("work-client.yaml", "WorkCardList", client.get("/api/plugins/kanban/work/cards").json())["cards"]
    assert [c["plain"] for c in listed if c["id"] == tid] == [{"title": GOOD["title"], "now": B, "result": None}]


def _journal(work) -> list[tuple[str, str]]:
    from hermes_cli import lina_board_events
    path = lina_board_events.journal(work.root, "cards")
    return [(rec["kind"], rec["id"]) for rec in map(json.loads, path.read_text(encoding="utf-8").splitlines())]


def test_a_card_backfilled_in_a_later_pass_is_announced_once_only_when_it_gained_lines(work, monkeypatch):
    from hermes_cli import lina_board_plain_history as history
    from hermes_cli.lina_board_plain import Source, write
    filled = Source("card", "hermes:t_a1", "now", "checking the photo backup", "default", True,
                    ((NOW - 100, "cleared the build disks"),))
    refused = Source("card", "hermes:t_b1", "now", "rotating the disks", "default", True, ((NOW - 200, "reviewed"),))
    _answering(monkeypatch, [said(A), said(B), "1. 빌드 기기 디스크를 비움", "1. Passed the review"])
    monkeypatch.setattr(history, "BACKFILL_CALLS", 0)  # the pass's backfill calls went to other cards
    write([filled, refused], work.root, NOW)
    assert _journal(work) == [("line", "hermes:t_a1"), ("line", "hermes:t_b1")]
    monkeypatch.setattr(history, "BACKFILL_CALLS", 3)

    assert write([filled, refused], work.root, NOW + 60)["called"] == 2  # backfills only: the lines are unchanged

    assert history.lookup("card", "hermes:t_a1", work.root, board="default") == [
        {"at": NOW - 100, "until": None, "text": "빌드 기기 디스크를 비움"}]
    assert _journal(work) == [("line", "hermes:t_a1"), ("line", "hermes:t_b1"), ("line", "hermes:t_a1")]
    write([filled, refused], work.root, NOW + 120)
    assert len(_journal(work)) == 3


def test_a_now_line_kept_for_its_missing_past_tense_is_stored_when_the_retry_call_fails(work, monkeypatch, caplog):
    from agent import auxiliary_client
    from hermes_cli.lina_board_plain import write
    sent: list[list[dict]] = []

    def answer(task=None, *, messages, **_kwargs):
        sent.append(messages)
        if len(sent) > 1:
            raise ConnectionError("the model is not reachable")
        return SimpleNamespace(choices=[SimpleNamespace(message=SimpleNamespace(content=A))])  # no second line
    monkeypatch.setattr(auxiliary_client, "call_llm", answer)

    assert write([_now("checking last week's backup")], work.root, NOW) == {"called": 1, "failed": 0, "pending": 0}

    assert len(sent) == 2 and "the model is not reachable" in caplog.text
    assert _rows(work)[("card", CARD, "now")] == (A, None)
    with sqlite3.connect(work.root / "board" / "plain.db") as db:
        assert db.execute("SELECT now, past FROM history_open").fetchall() == [(A, None)]


NUMBERED, GONE = "34·36·37·39번 변경을 메인에 합침", "허브맥 재부팅 후 상태가 정상인 것을 확인함"  # lines the hub held


def _cache(work, rows: list[tuple], backfilled: list[str], shown: tuple = ()) -> Path:
    """A cache written under an older ``check_past``: *rows* ``(id, at, until, text)`` in the record, the cards of
    *backfilled* marked backfilled, *shown* ``(id, now, past)`` lines being shown, and no ``check_revision`` yet."""
    from hermes_cli.lina_board_plain import _open
    _open(work.root, write=True).close()
    path = work.root / "board" / "plain.db"
    with sqlite3.connect(path) as db:
        db.execute("INSERT INTO writer VALUES ('history_from', ?)", (NOW,))
        db.execute("DELETE FROM writer WHERE key = 'check_revision'")
        db.executemany("INSERT INTO history VALUES ('card', 'default', ?, ?, ?, ?)", rows)
        db.executemany("INSERT INTO history_backfill VALUES ('card', 'default', ?, ?)", [(i, NOW) for i in backfilled])
        db.executemany("INSERT INTO history_open VALUES ('card', 'default', ?, ?, ?, ?)", [(*s, NOW) for s in shown])
    return path


def _record(path: Path) -> list[tuple]:
    with sqlite3.connect(path) as db:
        return db.execute("SELECT id, at, until, text FROM history ORDER BY id, at").fetchall()


def test_a_stored_line_the_grown_check_past_refuses_is_not_served(work):
    _cache(work, [(CARD, NOW - 300, None, NUMBERED), (CARD, NOW - 200, None, B_PAST), (CARD, NOW + 10, NOW + 20, GONE)],
           [CARD])
    assert _history(work) == [{"at": NOW - 200, "until": None, "text": B_PAST}]


def test_a_cache_s_refused_record_lines_are_dropped_once_and_its_backfilled_cards_are_backfilled_again(work, monkeypatch):
    from hermes_cli.lina_board_plain import Source, write
    refilled, live, untouched = CARD, "t_0000000b", "t_0000000c"
    path = _cache(work, [(refilled, NOW - 300, None, NUMBERED), (refilled, NOW - 200, None, B_PAST),
                         (refilled, NOW + 10, NOW + 20, A_PAST), (live, NOW + 10, NOW + 20, GONE),
                         (live, NOW + 20, NOW + 30, B_PAST), (untouched, NOW - 100, None, A_PAST)],
                  [refilled, untouched], ((live, B, GONE),))
    sources = [Source("card", refilled, "now", "", "default", True,
                      ((NOW - 300, "merged the order export fix"), (NOW - 200, "cleared the build disks"))),
               Source("card", untouched, "now", "", "default", True, ((NOW - 100, "checked the photo backup"),))]
    sent = _answering(monkeypatch, ["1. 주문 내보내기 수정을 메인에 합침\n2. 빌드 기기 디스크를 비움"])

    assert write(sources, work.root, NOW + 60)["called"] == 1

    assert len(sent) == 1 and "1. merged the order export fix" in sent[0][1]["content"]
    assert _record(path) == [(refilled, NOW - 300, None, "주문 내보내기 수정을 메인에 합침"), (refilled, NOW - 200, None, B_PAST),
                             (refilled, NOW + 10, NOW + 20, A_PAST), (live, NOW + 20, NOW + 30, B_PAST),
                             (untouched, NOW - 100, None, A_PAST)]
    with sqlite3.connect(path) as db:
        assert db.execute("SELECT id, now, past FROM history_open").fetchall() == [(live, B, None)]
        db.execute("INSERT INTO history VALUES ('card', 'default', ?, ?, ?, ?)", (untouched, NOW + 40, NOW + 50, GONE))
    assert write(sources, work.root, NOW + 120)["called"] == 0  # checked once: a later open leaves the record be
    assert (untouched, NOW + 40, NOW + 50, GONE) in _record(path)
