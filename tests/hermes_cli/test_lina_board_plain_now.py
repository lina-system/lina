"""The source of a work card's ``now`` line (G138): a blocked card's is its standing block reason, what it waits for,
not its latest comment, which often reports something already finished; a reason that says nothing (a card created
blocked) falls back to the comments. Ids, issue numbers, URLs, hashes and long numbers are taken out before the model
sees the source, since a model shown them copies them into a line its check refuses. The model is a fake.
"""

from __future__ import annotations

import sqlite3

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


# Lines the app's now gate (lina-app src/model/fold.ts nowMayShow, at lina-app 4737d8258c) shows, and lines it hides with
# a word of the reason the core gives the model; each verdict was read off nowMayShow itself.
APP_SHOWS = ("워치독 시험 복구 대기", "허브 기기에서 매매일지 설치 명령 실행 결과를 기다리는 중", "시험 묶음을 돌리는 중",
             "사진 크기 일괄 변환 대기", "먼저 비울 기기를 묻는 중", "12:50에 시작할 배포를 기다리는 중", "백업 사진 1234567건을 옮기는 중",
             "카드 3장을 정리하는 중", "빌드 기기(두 대) 디스크를 비우는 중", "1) 순서를 정하는 중", "토스 앱 승인을 기다리는 중",
             "가" * 40)
APP_HIDES = (
    ("토스 QR 로그인을 폰에서 승인하기를 기다리는 중", "QR"),
    ("현장 실사에서 LINA 웹 서버 기동 단계 알림 대기 중", "LINA"),
    ("준비됨 → 승인·거절 알림과 거래 중지 알림을 기다리는 중", "→"),
    ("S2 단계를 기다리는 중", "S2"),
    ("12:50 배포 시작을 기다리는 중", "time of day"),
    ("9:05:33부터 기다리는 중", "time of day"),
    ("사진을 옮기는 중…", "…"),
    ("사진을 옮기는 중...", "..."),
    ("커밋 1234567을 보는 중", "seven digits"),
    ("카드 3 정리를 기다리는 중", "numbers a card"),
    ("기기 두 대의 디스크를", "cut off"),
    ("설정을 화면에서", "cut off"),
    (", 기다리는 중", "starts inside"),
    ("기다리는 중:", "ends open"),
    ("괄호 () 안을 비우는 중", "empty bracket"),
    ("파일 를 고치는 중", "particle apart"),
    ("설정은 노출된", "missing noun"),
    ("빌드 기기(두 대 디스크를 비우는 중", "open"),
    ("순서는 1.", "list's mark"),
    ("알림 두 개를 && 묶는 중", "command"),
)


@pytest.mark.parametrize("line", APP_SHOWS)
def test_a_now_line_the_app_shows_passes_its_check(line):
    from hermes_cli.lina_board_plain import check_now
    assert check_now(line) is None


@pytest.mark.parametrize(("line", "word"), APP_HIDES)
def test_a_now_line_the_app_hides_is_refused_with_a_reason_the_model_can_act_on(line, word):
    from hermes_cli.lina_board_plain import check_now
    reason = check_now(line)
    assert reason is not None and word in reason


@pytest.mark.parametrize("line", ("시온 · 토스 로그인 승인 대기 중", "리나 · 웹 서버 기동 알림 대기"))
def test_a_now_line_that_starts_with_the_owner_s_name_is_refused_though_the_app_would_show_it(line):
    """The card shows who holds it on its own; a name before the work in the line is a header, not the work."""
    from hermes_cli.lina_board_plain import check_now
    reason = check_now(line)
    assert reason is not None and "name" in reason
    assert check_now(line.split(" · ", 1)[1]) is None


def _stored(work, field: str, text: str) -> None:
    """What a cache written under an older ``check_now`` holds: *text* stored as the passing line of *field*."""
    with sqlite3.connect(work.root / "board" / "plain.db") as db:
        db.execute("UPDATE line SET text = ?, error = NULL WHERE field = ?", (text, field))


def _sources():
    from hermes_cli.lina_board_plain import Source
    return [Source("card", "t_0000000a", field, text, "default", True)
            for field, text in (("title", "Toss login"), ("now", WAITING), ("result", "The login was approved."))]


def test_a_stored_now_line_the_check_now_refuses_is_asked_again_once_and_the_rewrite_is_stored(work, monkeypatch):
    from hermes_cli.lina_board_plain import write
    hidden, fixed = "토스 QR 로그인을 승인하기를 기다리는 중", "토스 로그인 승인을 기다리는 중"
    sent = _answering(monkeypatch, [GOOD["title"], GOOD["now"], GOOD["result"], fixed])
    write(_sources(), work.root, NOW)
    _stored(work, "now", hidden)
    _stored(work, "title", "LINA 알림 정리")
    _stored(work, "result", "GitHub 알림을 맞췄다.")

    assert write(_sources(), work.root, NOW + 60) == {"called": 1, "failed": 0, "pending": 0}

    assert len(sent) == 4 and WAITING in sent[3][1]["content"]
    rows = _rows(work)
    assert rows[("card", "t_0000000a", "now")] == (fixed, None)
    assert rows[("card", "t_0000000a", "title")] == ("LINA 알림 정리", None)
    assert rows[("card", "t_0000000a", "result")] == ("GitHub 알림을 맞췄다.", None)
    assert write(_sources(), work.root, NOW + 120) == {"called": 0, "failed": 0, "pending": 0}


def test_a_stored_now_line_whose_rewrites_are_refused_too_is_stored_refused_and_not_asked_again(work, monkeypatch):
    from hermes_cli.lina_board_plain import RETRIES, write
    hidden = "현장 실사에서 LINA 웹 서버 기동 알림 대기 중"
    sent = _answering(monkeypatch, [GOOD["title"], GOOD["now"], GOOD["result"]] + [hidden] * (RETRIES + 1))
    write(_sources(), work.root, NOW)
    _stored(work, "now", hidden)

    assert write(_sources(), work.root, NOW + 60) == {"called": 1, "failed": 0, "pending": 0}

    assert len(sent) == 3 + RETRIES + 1
    assert _rows(work)[("card", "t_0000000a", "now")] == (None, "contains the Latin letters LINA: write it in Korean")
    assert write(_sources(), work.root, NOW + 120) == {"called": 0, "failed": 0, "pending": 0}
    assert len(sent) == 3 + RETRIES + 1


def test_a_running_card_s_now_source_is_still_its_latest_comment():
    from hermes_cli.lina_board_plain import now_source
    comments = [("alpha", "복구 순서를 정리하는 중"), ("alpha", "백업 사진을 크기별로 나누는 중")]
    assert now_source(comments, "running", WAITING) == "백업 사진을 크기별로 나누는 중"
    assert now_source(comments, "blocked", "initial_status") == "백업 사진을 크기별로 나누는 중"
