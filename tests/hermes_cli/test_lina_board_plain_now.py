"""The source of a work card's ``now`` line (G138): a blocked card's is its standing block reason, what it waits for,
not its latest comment, which often reports something already finished; a reason that says nothing (a card created
blocked) falls back to the comments. Ids, issue numbers, URLs, hashes and long numbers are taken out before the model
sees the source, since a model shown them copies them into a line its check refuses. The model is a fake.
"""

from __future__ import annotations

import pytest

from tests.hermes_cli._work_env import NOW, WorkEnv
from tests.hermes_cli.test_lina_board_plain import GOOD, FakeModel, _answering, _rows

pytestmark = pytest.mark.platforms("posix")

WAITING = "현장 복구자가 가능한 때 시험을 이어갈지 정할 결정을 기다린다"
FINISHED = "삭제한 폴더 정리 끝, 디스크 여유 공간 늘어남"
TOKENS = ("lina#235", "9120", "t_6e6510d5", "https://example.com/a", "3f2a9c1e")
WITH_TOKENS = "lina#235 이어 가기 전 9120 포트를 UPS 뒤에서 볼지 결정 (t_6e6510d5, https://example.com/a, 3f2a9c1e)"


@pytest.fixture
def work(tmp_path, monkeypatch):
    return WorkEnv(tmp_path, monkeypatch)


@pytest.fixture
def model(monkeypatch):
    from agent import auxiliary_client
    fake = FakeModel()
    monkeypatch.setattr(auxiliary_client, "call_llm", fake)
    return fake


def _blocked_card(reason: str | None, *comments: str, created_blocked: bool = False) -> str:
    """A goal card blocked with *reason* (or created blocked), then *comments* added after the block."""
    from hermes_cli import kanban_db as kb
    from hermes_cli import kanban_db_connect as kbc
    kb.init_db()
    with kbc.connect_closing() as conn:
        tid = kb.create_task(conn, title="Restore the field machine", goal_mode=True, assignee="alpha",
                             initial_status="blocked" if created_blocked else "running")
        if not created_blocked:
            assert kb.block_task(conn, tid, reason=reason)
        for body in comments:
            kb.add_comment(conn, tid, "alpha", body)
    return tid


def _now_sources(model: FakeModel) -> list[str]:
    return [source for field, source in model.calls if field == "now"]


def test_a_blocked_card_s_now_source_is_its_block_reason_not_a_later_comment(work, model):
    from hermes_cli.lina_board_plain import write_board
    _blocked_card(WAITING, FINISHED)
    write_board(work.root, NOW)

    assert _now_sources(model) == ["원문: " + WAITING]


def test_a_card_created_blocked_takes_its_now_source_from_its_comments(work, model):
    from hermes_cli.lina_board_plain import write_board
    _blocked_card(None, "복구 순서를 정리하는 중", created_blocked=True)
    write_board(work.root, NOW)

    assert _now_sources(model) == ["원문: 복구 순서를 정리하는 중"]


def test_ids_numbers_urls_and_hashes_never_reach_the_model_and_latin_words_are_asked_in_korean(work, monkeypatch):
    from hermes_cli.lina_board_plain import write_board
    tid = _blocked_card(WITH_TOKENS, WITH_TOKENS)
    sent = _answering(monkeypatch, [GOOD["title"], "복구 결정을 기다리는 중"])
    assert write_board(work.root, NOW) == {"called": 2, "failed": 0, "pending": 0}

    assert _rows(work)[("card", tid, "now")] == ("복구 결정을 기다리는 중", None)

    now = next(messages for messages in sent if "UPS" in messages[1]["content"])
    assert not [token for token in TOKENS if token in now[1]["content"]]
    assert "UPS" in now[0]["content"]  # the rule that has an abbreviation written in Korean


def test_an_answer_refused_for_an_id_is_sent_back_and_the_korean_rewrite_is_stored(work, monkeypatch):
    from hermes_cli.lina_board_plain import Source, write
    copied, fixed = "t_6e6510d5 복구 결정을 기다리는 중", "현장 복구 결정을 기다리는 중"
    sent = _answering(monkeypatch, [copied, fixed])

    write([Source("card", "t_0000000a", "now", WAITING, "default", True)], work.root, NOW)

    assert _rows(work)[("card", "t_0000000a", "now")] == (fixed, None)
    assert sent[1][:2] == sent[0][:2] and sent[1][2] == {"role": "assistant", "content": copied}
    assert "contains an id" in sent[1][3]["content"]


@pytest.mark.parametrize(("events", "reason"), [
    ([("blocked", {"reason": "initial_status"})], "initial_status"),
    ([("blocked", {"reason": WAITING}), ("unblocked", None)], ""),
    ([("blocked", {"reason": "old"}), ("unblocked", None), ("blocked", {"reason": WAITING})], WAITING),
    ([("commented", None)], ""),
])
def test_the_block_reason_is_that_of_the_standing_block(events, reason):
    from hermes_cli.lina_board_plain import block_reason
    assert block_reason(events) == reason


def test_a_running_card_s_now_source_is_still_its_latest_comment():
    from hermes_cli.lina_board_plain import now_source
    comments = [("alpha", "복구 순서를 정리하는 중"), ("alpha", "백업 사진을 크기별로 나누는 중")]
    assert now_source(comments, "running", WAITING) == "백업 사진을 크기별로 나누는 중"
    assert now_source(comments, "blocked", "initial_status") == "백업 사진을 크기별로 나누는 중"
