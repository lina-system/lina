"""Checked model-written card lines (lina#151): a short title, a "doing now" line and a result line for every
``lina worker`` run and every work card (``is_work_card``: a goal-mode card, or any card whose body has a
``## To-dos`` section), served as ``plain`` on run cards and work cards. The work cards are LINA's own boards' and
the connected Hermes board's (``lina_board_hermes``), the latter from the last good pass of the reader in the
writer's own process (the web server's), so the writer never runs the Hermes CLI.

The writer runs on its own daemon thread, started once by the hub's worker service (``start_writer``), never in the
service loop or the hub tick and never in a request: a slow model call holds back nothing but the next pass. For each (kind, id, field) it hashes the field's source text
and calls the model only when that hash changed, through Hermes' auxiliary client (``call_llm``, task
``board_lines``: LINA's model path), as the profile ``worker.board_lines_profile`` names (``_model_profile``).
Every answer passes its field check before it is stored; a failed check is
recorded with its reason and the field stays null, and the source text is never served in its place. Reads
(``lookup``, ``writer_state``) only open the cache, ``$LINA_HOME/board/plain.db``.
"""

from __future__ import annotations

import hashlib
import logging
import re
import sqlite3
import threading
import time
from contextlib import contextmanager
from contextvars import copy_context
from pathlib import Path
from typing import Callable, Iterator, NamedTuple

TASK = "board_lines"
PROFILE_SETTING = "worker.board_lines_profile"
FIELDS = ("title", "now", "result")
LIMITS = {"title": 25, "now": 40, "result": 80}  # the app shows a now line of at most 40 characters
EMPTY = dict.fromkeys(FIELDS)
STOP_AFTER_S = 30 * 60
MAX_CALLS = 20
RETRIES = 2  # more asks for one field whose answer failed its check, in the same slot, each told why
PASS_S = 90.0  # wall time one pass may spend calling the model; what is left stays pending for the next
EVERY_S = 60.0
_clock = time.monotonic
SOURCE_CHARS = 2000
ALLOWED_LATIN = frozenset({"LINA", "Honcho", "GitHub", "macOS", "iPhone", "Telegram", "Codex"})
# Words the app never draws (lina-app words.ts BANNED_WORDS): a now line holding one is dropped there, so refuse it here and let the model retry.
NOW_BANNED = ("세션", "일꾼", "회차", "판정", "장부")
# Automatic notices, which say nothing of what a card is doing, are skipped as a card's now source: by their author (the
# dispatcher's met waits, the hub's run notices) or their head (receipts, met waits, run notices, ledger links).
NOW_SKIP_AUTHORS = frozenset({"kanban-dispatcher", "lina-worker"})
NOW_SKIP_HEAD = re.compile(r"\[받음\]|\[대기 충족\]|\[lina worker|\[[^\]\n]*\]\s*장부 목표 G\d+.*붙였다", re.DOTALL)
ACTION_NOUNS = ("정리", "점검", "추가", "수정", "확인", "개선", "작성", "이전", "배포", "설정", "검토", "정비", "구축",
                "연결", "분리", "통합", "제거", "교체", "갱신", "복구", "조사", "분석", "준비", "도입", "적용", "전환",
                "등록", "보완", "마련", "구현", "변경", "해결", "대응", "측정", "비교", "보강", "이관", "검증", "표시",
                "저장", "전송", "기록", "관리", "지원", "처리", "반영", "공유", "요약", "조정", "설계", "정의", "개편",
                "백업", "복원", "업데이트", "시험", "실험", "학습", "번역", "정돈", "발행", "출시")
log = logging.getLogger(__name__)


class Source(NamedTuple):
    kind: str  # "run" (a lina worker job) or "card" (a work card, ``is_work_card``)
    id: str
    field: str
    text: str
    board: str = ""  # a card's board (``board_key``); "" for runs. Card ids repeat across boards (an imported board)
    unfinished: bool = False  # a card that is not done: its fields queue ahead of done cards' and runs'


def board_key(slug: str | None) -> str:
    """The board a work card's lines are kept under, on both sides (the writer and the work API): its board slug,
    the default board when none. The writer labels each kanban database file with the first listed board that
    resolves to it, and the default board is always listed first; so when ``HERMES_KANBAN_DB`` pins every board to
    one file, that file's cards are kept under the default board, which is what a request without ``?board=``
    resolves to and the pinned file it reads."""
    from hermes_cli import kanban_db
    return slug or kanban_db.DEFAULT_BOARD


# ---------------------------------------------------------------- field checks
_ID = re.compile(r"(?<![0-9A-Za-z])(?:[GTAMP]\d+|t_[0-9A-Za-z]+|O[TGS]-[\w-]+|#\d+)")
_PATH = re.compile(r"[/\\~]")
_FILE = re.compile(r"[\w-]\.[A-Za-z][A-Za-z0-9]{0,4}\b")
_COMMAND = re.compile(r"(?:^|\s)--?[A-Za-z]|[$|<>=]")
_HASH = re.compile(r"\b[0-9a-f]{7,40}\b")
_URL = re.compile(r"https?:|www\.", re.IGNORECASE)
_WORD = re.compile(r"[A-Za-z][A-Za-z0-9]*")
_ENDERS = ".!?。"


def _common(text: str, limit: int) -> str | None:
    """The rules every field shares; the reason a line is refused, else None."""
    if not text:
        return "empty"
    if len(text) > limit:
        return f"longer than {limit} characters"
    if "\n" in text:
        return "more than one line"
    for rule, reason in ((_URL, "a URL"), (_ID, "an id"), (_HASH, "a hash"), (_FILE, "a file name"),
                         (_PATH, "a path"), (_COMMAND, "a command")):
        if rule.search(text):
            return f"contains {reason}"
    if "`" in text:
        return "contains a backtick"
    latin = [w for w in _WORD.findall(text) if sum(c.isalpha() for c in w) >= 3 and w not in ALLOWED_LATIN]
    if latin:
        return f"contains the Latin word {latin[0]}"
    return None


def _one_sentence(text: str) -> bool:
    return not any(c in _ENDERS for c in text.rstrip(_ENDERS))


def check_title(text: str) -> str | None:
    """At most 25 characters, [대상] [무엇] [동작]: two words or more, no sentence, ending on an action noun or a
    ~기 form."""
    reason = _common(text, LIMITS["title"])
    if reason:
        return reason
    if any(c in _ENDERS for c in text) or text.endswith(("다", "요")):
        return "a sentence, not a title"
    words = text.split()
    if len(words) < 2:
        return "fewer than two words"
    if not (words[-1].endswith("기") or words[-1].endswith(ACTION_NOUNS)):
        return "does not end on an action noun or a ~기 form"
    return None


# The app's own gate for a now line (lina-app src/model/fold.ts ``nowMayShow``), which hides a line it fails: a line
# stored that it hides leaves a card's top line empty, so ``check_now`` refuses it too and the model writes it again.
# Rules the shared checks (``_common``) already hold are not repeated; its ``\p{Script=Hangul}`` is spelled out.
_H = "[\u1100-\u11ff\u3130-\u318f\ua960-\ua97f\uac00-\ud7a3\ud7b0-\ud7ff\uffa0-\uffdc]"
_PARTICLES = "으로|에서|까지|부터|로|를|을|이|가|은|는|와|과|의|에|도|만"
_LONE_PARTICLE = r"(?:으로|에서|까지|부터|이라|라고|가|은|는|을|를|라|로|와|과|도|의|만|에)(?=[\s.,!?。]|$)"
_MODIFIER = (r"(?:^|[\s(])(?:새|헌|옛|각|모든|온갖|여러|이|그|저|이런|그런|저런|어떤|무슨|다른|몇|"
             rf"{_H}*(?:된|될|던|하는|되는|있는|없는|같은))")
_LATIN_LETTERS = re.compile(r"[A-Za-z][A-Za-z0-9]*")
_NEVER_DRAWN = re.compile(r"[/\\`=|→←↔⇒⇄…]|->|\.\.\.")
_CLOCK = re.compile(r"(?<![0-9:.])[0-9]{1,2}:[0-9]{2}(?::[0-9]{2})?(?![0-9])")
_LIST_MARK = re.compile(r"^\(?[0-9]{1,2}[.)]\s+")
_LIST_PAREN = re.compile(r"((?<!\S)[0-9]{1,2}\)(?=\s))|[()]")
# Read on the line less a list's mark before it (``sentenceMayShow``).
_SENTENCE_RULES = tuple((re.compile(rule), reason) for rule, reason in (
    (r"^\(?[0-9]+(?:[.,][0-9]+)*\)?\s*" + _H + r"{0,2}[.!?。…]*$", "is a number alone: say what is being done"),
    (r"^(?:[.,:;·…|)\]}~\-\u2013\u2014→]|['\"\u2019\u201d](?:\s|$)|(?!만\s)" + _LONE_PARTICLE + ")",
     "starts inside another sentence: start on what is being done"),
    (r"(?:[|·,:;/+\-\u2013\u2014(\[{]|\s" + _LONE_PARTICLE + r")\s*(?:…|\.\.\.)?$",
     "ends open on a mark or a lone particle: finish the sentence"),
    (r"[(\[{][\W_]*[)\]}]", "contains an empty bracket: leave it out"),
    (r"[,:;][.,:;]|\.[,:;]|(?:^|\s)[.,:;|]+(?=\s|$)", "contains stray punctuation: leave it out"),
    (r"\s(?:(?:으로|에서|을|를|은|는|의|로|와|과)(?=[\s.,!?。:;)…]|$)|(?:이|가|도|만|에)(?=[.,!?。:;)…]|$))",
     "has a particle apart from its noun: write it glued to the noun"),
    (_MODIFIER + r"\s*(?:[.,!?。:;)…]|$)", "ends on a word that modifies a missing noun: finish the sentence"),
))
# Read on the whole line (``nowMayShow``, ``unitMayShow``).
_LINE_RULES = tuple((re.compile(rule), reason) for rule, reason in (
    (rf"(?<![0-9.,])[0-9]{{7,}}(?![0-9]|\.[0-9]|,[0-9]{{3}}(?![0-9])|(?!(?:{_PARTICLES})(?!{_H})){_H})",
     "contains a number of seven digits or more: leave it out"),
    (rf"(?:표지\s*)?카드\s*[0-9]+(?![0-9]|{_H})", "numbers a card: say what the card is about instead"),
    (r"\s&&\s", "contains a command"),
    (r"^\(?[0-9]{1,2}[.)]$|\s\(?1[.)]$", "ends on a list's mark: write the item itself"),
    (_H + r"(?:을|를|에서|으로|은|는|이)[.!?。]?[\"'\u2019\u201d)\]]*$",
     "ends on a particle, cut off before its verb: finish the sentence"),
))


def _log_time(text: str) -> bool:
    """Two times of day or more, one with seconds, or one with no Korean glued after it (``holdsNeverDrawn``): one time
    with a particle or a word glued after it (``12:50에``) is a plain fact."""
    times = list(_CLOCK.finditer(text))
    return bool(times) and (len(times) > 1 or times[0].group(0).count(":") > 1
                            or not re.match(_H, text[times[0].end():]))


def _closes(text: str) -> bool:
    """Every bracket and quote opened is closed; a list's mark (``1)``) closes no bracket it did not open."""
    opened = closed = 0
    for match in _LIST_PAREN.finditer(text):
        if match.group(0) == "(":
            opened += 1
        elif not match.group(1) or opened > closed:
            closed += 1
    return (opened == closed and text.count("[") == text.count("]") and text.count("\u201c") == text.count("\u201d")
            and text.count("\u2018") == text.count("\u2019") and text.count('"') % 2 == 0 and text.count("'") % 2 == 0)


def _app_hides(text: str) -> str | None:
    """Why the app's now gate would hide *text*, a line the shared checks passed; None when it shows it."""
    latin = _LATIN_LETTERS.search(text)
    if latin:
        return f"contains the Latin letters {latin.group(0)}: write it in Korean"
    mark = _NEVER_DRAWN.search(text)
    if mark:
        return f"contains {mark.group(0)}, a mark the app does not show: write it in words"
    if _log_time(text):
        return "contains a time of day the app reads as a log's: leave it out, or glue a word to one time (12:50에)"
    sentence = _LIST_MARK.sub("", text, count=1)
    if not re.search(_H, sentence):
        return "holds no Korean"
    for rules, read in ((_SENTENCE_RULES, sentence), (_LINE_RULES, text)):
        reason = next((reason for rule, reason in rules if rule.search(read)), None)
        if reason:
            return reason
    return None if _closes(sentence) else "leaves a bracket or a quote open: close it"


def check_now(text: str) -> str | None:
    """At most 40 characters, one sentence, in the present progressive (~하는 중, ~고 있다) or a short noun
    phrase, and a line the app shows (``_app_hides``)."""
    reason = _common(text, LIMITS["now"])
    if reason:
        return reason
    if not _one_sentence(text):
        return "more than one sentence"
    banned = next((w for w in NOW_BANNED if w in text), None)
    if banned:
        return f"contains the word {banned}, which the app does not show"
    body = text.rstrip(_ENDERS).rstrip()
    if body.endswith(("다", "요")) and not body.endswith(("중이다", "고 있다", "중입니다", "고 있습니다")):
        return "not the present progressive or a noun phrase"
    return _app_hides(text)


def check_result(text: str) -> str | None:
    """At most 80 characters, one sentence."""
    reason = _common(text, LIMITS["result"])
    if reason:
        return reason
    if not _one_sentence(text):
        return "more than one sentence"
    return None


CHECKS: dict[str, Callable[[str], str | None]] = {"title": check_title, "now": check_now, "result": check_result}


# ---------------------------------------------------------------- prompts
_SYSTEM = ("너는 작업 보드의 한 줄을 쓴다. 한국어로, 짧고 쉬운 업무 말투로 쓴다. 대상과 그 상태만 말하고 멈춘다. "
           "설명, 따옴표, 머리말 없이 한 줄만 답한다. id, 경로, 파일 이름, 명령, 영어 도구 이름, 번호는 쓰지 않는다. "
           "UPS, NAS 같은 영어 약어는 무엇인지 한글로 풀어 쓴다.")
_PROMPTS = {
    "title": ("작업의 제목을 25자 안으로 쓴다. [대상] [무엇] [동작] 순서로, 동작 명사나 ~기로 끝낸다.\n"
              "예) 원문: 'ulw # Rotate the build machine disks before they fill' -> 빌드 기기 디스크 교체\n"
              "예) 원문: '알림 문구를 앱 화면에 맞추기 lina-system/lina-app#9' -> 앱 알림 문구 정리\n"
              "예) 원문: 'Add weekly backup check for the photo library' -> 사진 보관함 주간 백업 점검"),
    "now": ("지금 하고 있는 일을 40자 안의 한 문장으로 쓴다. '~하는 중' 같은 진행형이나 짧은 명사구로 쓴다. '일꾼', '세션', '회차', '판정', '장부'라는 말은 쓰지 않는다.\n"
            "영문 글자는 하나도 쓰지 않고 한글로 쓴다(LINA는 '리나'). 화살표(→), 빗금(/), 말줄임(…)도 쓰지 않는다.\n"
            "멈춘 작업이면 무엇을 기다리는지 쓴다. 이미 끝난 일의 결과는 쓰지 않는다.\n"
            "예) 원문: 'running the test suite on build-a, 120 of 400 passed' -> 시험 묶음을 돌리는 중\n"
            "예) 원문: '질문: 두 기기 중 어느 쪽을 먼저 비울까요?' -> 먼저 비울 기기를 묻는 중\n"
            "예) 원문: 'waiting on background job: image resize batch' -> 사진 크기 일괄 변환 대기"),
    "result": ("끝난 작업의 결과를 80자 안의 한 문장으로 쓴다.\n"
               "예) 원문: '빌드 기기 두 대의 디스크를 비우고 경고를 없앴다.' -> 빌드 기기 두 대의 디스크를 비우고 경고를 없앴다.\n"
               "예) 원문: 'Added the weekly backup check; it passed on the first run.' -> 주간 백업 점검을 추가했고 첫 실행에서 통과했다.\n"
               "예) 원문: 'Could not reach the photo server, nothing changed.' -> 사진 서버에 닿지 못해 바뀐 것이 없다."),
}


def _clean(answer: str) -> str:
    lines = [line.strip() for line in str(answer or "").splitlines() if line.strip()]
    return lines[0].strip("\"'“”‘’ ") if len(lines) == 1 else "\n".join(lines)


def ask(field: str, source: str, refused: list[tuple[str, str]] | None = None) -> str:
    """One field from its source text through the auxiliary client; raises when the call fails. *refused* holds the
    earlier answers of this slot with the reason each failed its check, sent back so the model writes it again."""
    from agent.auxiliary_client import call_llm
    messages = [{"role": "system", "content": _SYSTEM + "\n\n" + _PROMPTS[field]},
                {"role": "user", "content": "원문: " + source[:SOURCE_CHARS]}]
    for answer, reason in refused or ():
        messages += [{"role": "assistant", "content": answer},
                     {"role": "user", "content": f"이 답은 쓸 수 없다({reason}). 같은 규칙으로 한 줄만 다시 쓴다."}]
    response = call_llm(task=TASK, max_tokens=120, temperature=0.2, messages=messages)
    return _clean(response.choices[0].message.content or "")


# ---------------------------------------------------------------- the cache
# Version 2 keys a line by its board as well. Version 3 holds only lines that passed the 40-character ``now`` limit (G138):
# a line is keyed by its source's hash, so one stored under the old 60-character limit would otherwise be served for as
# long as its source stays the same. The cache is regenerable, so an older ``line`` table is dropped (the next pass
# writes it again); the ``writer`` state is kept.
SCHEMA_VERSION = 3
_SCHEMA = ("CREATE TABLE IF NOT EXISTS line (kind TEXT NOT NULL, board TEXT NOT NULL, id TEXT NOT NULL, "
           "field TEXT NOT NULL, source_hash TEXT NOT NULL, text TEXT, error TEXT, at INTEGER NOT NULL, "
           "PRIMARY KEY (kind, board, id, field))",
           "CREATE TABLE IF NOT EXISTS writer (key TEXT PRIMARY KEY, value INTEGER)",
           # When each field's model call was last made, answered or not: the order of the next pass's queue.
           "CREATE TABLE IF NOT EXISTS attempt (kind TEXT NOT NULL, board TEXT NOT NULL, id TEXT NOT NULL, "
           "field TEXT NOT NULL, at INTEGER NOT NULL, PRIMARY KEY (kind, board, id, field))")
# How fields are asked, kept as the ``writer`` key ``ask_revision``. A refused line stays refused while its source
# hash is unchanged, so a cache from an older revision has its refused ``now`` lines (and their attempts) dropped once
# to be asked again. 1: a refused answer is asked again with its reason (lina#789); ``now`` lines refused before that
# left cards with an empty top line. Refused titles and results are kept: there are many and they were not the fault.
ASK_REVISION = 1


def _root(root: Path | None) -> Path:
    from hermes_cli.lina_deps import lina_root
    return lina_root() if root is None else root


def _path(root: Path | None) -> Path:
    return _root(root) / "board" / "plain.db"


def _open(root: Path | None, write: bool) -> sqlite3.Connection | None:
    path = _path(root)
    if not write:
        return sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True, timeout=5) if path.is_file() else None
    path.parent.mkdir(parents=True, exist_ok=True)
    db = sqlite3.connect(path, timeout=10)
    if db.execute("PRAGMA user_version").fetchone()[0] < SCHEMA_VERSION:
        db.execute("DROP TABLE IF EXISTS line")
        db.execute(f"PRAGMA user_version = {SCHEMA_VERSION}")
    for statement in _SCHEMA:
        db.execute(statement)
    revision = db.execute("SELECT value FROM writer WHERE key = 'ask_revision'").fetchone()
    if (revision[0] if revision else 0) < ASK_REVISION:
        db.execute("DELETE FROM attempt WHERE field = 'now' AND (kind, board, id) IN "
                   "(SELECT kind, board, id FROM line WHERE field = 'now' AND error IS NOT NULL)")
        db.execute("DELETE FROM line WHERE field = 'now' AND error IS NOT NULL")
        db.execute("INSERT OR REPLACE INTO writer (key, value) VALUES ('ask_revision', ?)", (ASK_REVISION,))
    db.commit()
    return db


def _hash(source: Source) -> str:
    return hashlib.sha256(f"{source.field}\0{source.text}".encode()).hexdigest()


def lookup(kind: str, ids: list[str], root: Path | None = None, board: str = "") -> dict[str, dict]:
    """``{id: {title, now, result}}`` of one board's cards (``board_key``) or of runs (``board`` ""), null where
    nothing passed its check, nothing was written yet, or the cache predates ``SCHEMA_VERSION``."""
    out = {i: dict(EMPTY) for i in ids}
    db = _open(root, write=False) if ids else None
    if db is None:
        return out
    try:
        if db.execute("PRAGMA user_version").fetchone()[0] < SCHEMA_VERSION:
            return out
        marks = ",".join("?" * len(ids))
        for item, field, text in db.execute(
                f"SELECT id, field, text FROM line WHERE kind = ? AND board = ? AND id IN ({marks})",
                [kind, board, *ids]):
            if field in EMPTY:
                out[item][field] = text
    except sqlite3.OperationalError:
        return out  # a cache from before its first write has no tables yet
    finally:
        db.close()
    return out


def _meta(db: sqlite3.Connection) -> dict[str, int | None]:
    return {k: v for k, v in db.execute("SELECT key, value FROM writer")}


def writer_state(root: Path | None = None, now: float | None = None) -> dict:
    """``{state: ok|stopped|never, lastSuccessAt, stoppedSince}``. Stopped when the last calls failed, or when work
    was left to write and nothing succeeded for 30 minutes."""
    now = time.time() if now is None else now
    db = _open(root, write=False)
    try:
        meta = _meta(db) if db is not None else {}
    except sqlite3.OperationalError:
        meta = {}
    finally:
        if db is not None:
            db.close()
    if not meta.get("first_run_at"):
        return {"state": "never", "lastSuccessAt": None, "stoppedSince": None}
    last, stopped = meta.get("last_success_at"), meta.get("stopped_since")
    if stopped is None and meta.get("pending"):
        quiet_from = last or meta["first_run_at"]
        if now - quiet_from > STOP_AFTER_S:
            stopped = int(quiet_from + STOP_AFTER_S)
    return {"state": "stopped" if stopped else "ok", "lastSuccessAt": last, "stoppedSince": stopped}


# ---------------------------------------------------------------- the writer
def _key(s: Source) -> tuple[str, str, str, str]:
    return (s.kind, s.board, s.id, s.field)


# The last problem logged per topic ("setting": the profile setting, "call": a failing model call): a problem that
# stays the same pass after pass is logged once, and again only when it changes.
_said: dict[str, str | None] = {"setting": None, "call": None}


def _say_setting(problem: str | None) -> None:
    if problem == _said["setting"]:
        return
    if problem:
        log.warning("board lines: %s; card lines stay empty until it is fixed", problem)
    else:
        log.info("board lines: %s works again", PROFILE_SETTING)
    _said["setting"] = problem


def _say_call_failed(s: Source, exc: Exception) -> None:
    problem = f"{type(exc).__name__}: {exc}"
    if problem == _said["call"]:
        log.debug("board line %s %s %s: the call failed again: %s", s.kind, s.id, s.field, problem)
        return
    log.warning("board line %s %s %s: the call failed (auxiliary.board_lines of the profile in %s, else of this "
                "process's own); the same failure is not logged again", s.kind, s.id, s.field, PROFILE_SETTING,
                exc_info=exc)
    _said["call"] = problem


def _answer(s: Source, deadline: float) -> tuple[str, str | None]:
    """One field's answer and the reason its check refused it (None when it passed): a refused answer is sent back
    with its reason and asked again, at most RETRIES times and only before *deadline*. Raises when a call fails."""
    refused: list[tuple[str, str]] = []
    while True:
        answer = ask(s.field, s.text.strip(), refused)
        reason = CHECKS[s.field](answer)
        if not reason or len(refused) == RETRIES or _clock() >= deadline:
            return answer, reason
        log.debug("board line %s %s %s refused, asking again: %s", s.kind, s.id, s.field, reason)
        refused.append((answer, reason))


def _due(s: Source, cached: tuple[str, str | None] | None) -> bool:
    """Whether a field is asked again: its source hash moved, or its stored ``now`` line fails the current
    ``check_now`` (a check grown since it was stored). A refused answer is stored with no text, so it is asked once."""
    if cached is None or cached[0] != _hash(s):
        return True
    return s.field == "now" and cached[1] is not None and check_now(cached[1]) is not None


def write(sources: list[Source], root: Path | None = None, now: float | None = None, usable: bool = True) -> dict:
    """Write what changed: one call per field whose source hash moved or whose stored now line its check refuses now
    (``_due``; at most MAX_CALLS and PASS_S a run), the check's verdict
    stored either way; a field whose source is gone is cleared. A call that fails stores nothing and is tried again
    next run; with no usable model setting (``usable`` False) nothing is called, the due fields stay pending and the
    writer counts as stopped. The due fields of unfinished cards queue first, then the rest; within each, by how long
    they have waited: never attempted first, then the oldest last attempt (a failed call counts as an attempt), so
    sources that change on every pass cannot keep a field waiting for more than a few passes. A refused answer is
    asked again in its slot (``_answer``). -> {called, failed, pending}."""
    now = int(time.time() if now is None else now)
    db = _open(root, write=True)
    try:
        with db:
            meta = _meta(db)
            db.execute("INSERT OR IGNORE INTO writer (key, value) VALUES ('first_run_at', ?)", (now,))
            cached = {key[:4]: key[4:] for key in db.execute("SELECT kind, board, id, field, source_hash, text FROM line")}
            tried = {key[:4]: key[4] for key in db.execute("SELECT kind, board, id, field, at FROM attempt")}
            for s in sources:
                if not s.text.strip():
                    for table in ("line", "attempt"):
                        db.execute(f"DELETE FROM {table} WHERE kind = ? AND board = ? AND id = ? AND field = ?",
                                   (s.kind, s.board, s.id, s.field))
        due = sorted((s for s in sources if s.text.strip() and _due(s, cached.get(_key(s)))),
                     key=lambda s: (not s.unfinished, tried.get(_key(s), -1)))  # stable: source order among equals
        calls, failed, deadline = [], 0, _clock() + PASS_S
        for s in due[:MAX_CALLS] if usable else ():
            if _clock() >= deadline:
                break
            calls.append(s)
            with db:
                db.execute("INSERT OR REPLACE INTO attempt (kind, board, id, field, at) VALUES (?, ?, ?, ?, ?)",
                           (*_key(s), now))
            try:
                answer, reason = _answer(s, deadline)
            except Exception as exc:  # health: allow BLE001 -- any provider error; logged once per distinct failure, the field is retried next pass
                _say_call_failed(s, exc)
                failed += 1
                continue
            _said["call"] = None
            if reason:
                log.info("board line %s %s %s refused: %s", s.kind, s.id, s.field, reason)
            with db:
                db.execute("INSERT INTO line (kind, board, id, field, source_hash, text, error, at) "
                           "VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (kind, board, id, field) DO UPDATE SET "
                           "source_hash = excluded.source_hash, text = excluded.text, error = excluded.error, "
                           "at = excluded.at",
                           (s.kind, s.board, s.id, s.field, _hash(s), None if reason else answer, reason, now))
        pending = len(due) - len(calls) + failed
        with db:
            if calls and not failed:
                db.execute("INSERT OR REPLACE INTO writer (key, value) VALUES ('last_success_at', ?)", (now,))
                db.execute("DELETE FROM writer WHERE key = 'stopped_since'")
            elif calls:
                db.execute("INSERT OR REPLACE INTO writer (key, value) VALUES ('stopped_since', ?)",
                           (meta.get("stopped_since") or now,))
            elif not due:  # nothing left to write (a failing source went away): no line is missing, so not stopped
                db.execute("DELETE FROM writer WHERE key = 'stopped_since'")
            elif not usable:
                db.execute("INSERT OR REPLACE INTO writer (key, value) VALUES ('stopped_since', ?)",
                           (meta.get("stopped_since") or now,))
            db.execute("INSERT OR REPLACE INTO writer (key, value) VALUES ('pending', ?)", (pending,))
    finally:
        db.close()
    return {"called": len(calls), "failed": failed, "pending": pending}


# ---------------------------------------------------------------- sources
# Tokens a now line may not hold (``_common``), taken out of its source before the model sees it: a model shown
# "lina#235" or a card id copies it, and asked again with the same source copies it again. Latin words stay: the
# prompt has the model write them in Korean.
_SOURCE_DROPS = (re.compile(r"(?:https?://|www\.)\S+", re.IGNORECASE), re.compile(r"(?:[\w.-]+/)?[\w.-]*#\d+"), _ID,
                 _HASH, re.compile(r"(?<!\d)\d{4,}(?!\d)"))
_SLASHED = re.compile(r"[^\s()\[\]]*[/\\~][^\s()\[\]]*")  # a path, or a count such as 1/3 (also refused as a path)
_EMPTY_BRACKETS = re.compile(r"\(\s*\)|\[\s*\]")


def bare(text: str) -> str:
    """*text* without URLs, issue and card ids, hashes, paths, numbers of four digits or more and backticks."""
    for rule in (*_SOURCE_DROPS, _SLASHED):
        text = rule.sub(" ", text)
    text = _EMPTY_BRACKETS.sub(" ", text.replace("`", ""))
    return "\n".join(" ".join(line.split()) for line in text.splitlines()).strip()


def _live_source(live: dict | None) -> str:
    """A pending question first, a long background job's label next, then the runner's last words (``bare``)."""
    live = live or {}
    if live.get("dash") in ("human", "background") and live.get("why"):
        return bare(str(live["why"]))
    return bare(str(live.get("last") or ""))


def _report_line(path: Path) -> str:
    if not path.is_file():
        return ""
    for line in path.read_text(encoding="utf-8-sig", errors="replace").splitlines():
        text = line.strip().lstrip("-*> ").strip()
        if text and not text.startswith("#"):
            return text
    return ""


def run_sources(root: Path, now: float, lives: dict[str, dict] | None = None) -> list[Source]:
    """The runs the board shows (``worker.board.cards``): title from the brief's first heading, now from the
    runner's live state while it runs, result from the report's first line once it ended."""
    from hermes_cli.worker import settings
    from hermes_cli.worker.board import _ends, _lives, _recent, _title
    from hermes_cli.worker.hub import ENDED, Hub
    from hermes_cli.worker.hub_store import Store
    worker_root = settings.root(root)
    store = Store(worker_root)
    ends = _ends(store)
    jobs = [job for job in store.all() if not job.get("card") and _recent(job, ends.get(job["id"]), now)]
    if lives is None:
        lives = _lives(Hub(worker_root, out=lambda *_: None), jobs)
    out = []
    for job in jobs:
        running = job.get("state") == "running"
        report = Path(job.get("out_dir") or store.outs / str(job.get("bot")) / job["id"]) / "report.md"
        out += [Source("run", job["id"], "title", _title(job)),
                Source("run", job["id"], "now", _live_source(lives.get(job["id"])) if running else ""),
                Source("run", job["id"], "result", _report_line(report) if job.get("state") in ENDED else "")]
    return out


_ISSUE_REF = re.compile(r"\b[\w.-]+/[\w.-]+#\d+\b")
_TODO_HEADING = re.compile(r"^##\s+To-?dos\s*$", re.IGNORECASE | re.MULTILINE)


def is_work_card(goal_mode: int | bool, body: str | None, status: str) -> bool:
    """A card the app lists as work, and this writer keeps lines for (lina#290): a goal-mode card, any
    non-archived card whose body has a ``## To-dos`` section, and every blocked or triage card, which the owner may
    have to answer or sort (lina#664). A goal card above it is not required; ``work_api`` selects the same set."""
    return status != "archived" and (bool(goal_mode) or bool(_TODO_HEADING.search(body or ""))
                                      or status in ("blocked", "triage"))


def _boards(kanban_db) -> list[str]:
    """The live board slugs, as the gateway's kanban watchers enumerate them (default board when listing fails)."""
    try:
        return [b.get("slug") or kanban_db.DEFAULT_BOARD for b in kanban_db.list_boards(include_archived=False)]
    except (OSError, ValueError, sqlite3.Error):
        log.warning("board lines: listing kanban boards failed; reading the default board only", exc_info=True)
        return [kanban_db.DEFAULT_BOARD]


def card_sources() -> list[Source]:
    """Work cards (``is_work_card``) of every live board (the client API serves any board's cards, ``?board=``), each
    under its board (``board_key``). The board list is iterated the dispatcher's way, inside
    ``pin_first_board_resolution``: with ``HERMES_KANBAN_DB`` set every slug resolves to that one file, read once
    under the first slug listed (the default board). A board that cannot be read is logged and skipped."""
    from hermes_cli import kanban_db
    with kanban_db.pin_first_board_resolution():
        files: dict[Path, str] = {}
        for slug in _boards(kanban_db):
            path = Path(kanban_db.kanban_db_path(board=slug))
            if path.is_file():
                files.setdefault(path.resolve(), board_key(slug))
    out = []
    for path, board in files.items():
        try:
            out += _board_cards(kanban_db, path, board)
        except (sqlite3.Error, OSError, ValueError):
            log.warning("board lines: kanban board %s could not be read; skipped this pass", board, exc_info=True)
    return out


# Block reasons that say nothing of what a card waits for: a card created blocked (``create_task``).
NOW_SKIP_REASONS = frozenset({"initial_status"})


def block_reason(events: list[tuple[str, dict | None]]) -> str:
    """The reason of a card's standing block from its ``(kind, payload)`` events, oldest first: the latest
    ``blocked`` event's, "" when an ``unblocked`` came after it or there is none."""
    for kind, payload in reversed(events):
        if kind == "unblocked":
            return ""
        if kind == "blocked":
            return str((payload or {}).get("reason") or "")
    return ""


def now_source(comments: list[tuple[str, str]], status: str, reason: str = "") -> str:
    """A card's ``now`` source (``bare``): for a blocked card its block *reason*, what it waits for, unless that is
    empty or says nothing (``NOW_SKIP_REASONS``); else from its ``(author, body)`` comments, oldest first, the latest
    that is not empty and not an automatic notice (``NOW_SKIP_AUTHORS``, ``NOW_SKIP_HEAD``); "" once the card is done
    or when no comment is left."""
    if status == "done":
        return ""
    if status == "blocked" and reason.strip() and reason.strip() not in NOW_SKIP_REASONS:
        return bare(reason)
    return bare(next((body for author, body in reversed(comments)
                      if body.strip() and author not in NOW_SKIP_AUTHORS and not NOW_SKIP_HEAD.match(body.lstrip())),
                     ""))


def _card_lines(card_id: str, board: str, title: str, body: str | None, status: str,
                comments: list[tuple[str, str]], result: str, reason: str = "") -> list[Source]:
    """One work card's sources: title from the card title plus the issue it names, now from ``now_source`` (with the
    card's ``block_reason``), result
    from *result* once it is done; a card that is not done queues first (``Source.unfinished``)."""
    issue = _ISSUE_REF.search(body or "")
    unfinished = status != "done"
    return [Source("card", card_id, "title", title + (f" {issue.group(0)}" if issue else ""), board, unfinished),
            Source("card", card_id, "now", now_source(comments, status, reason), board, unfinished),
            Source("card", card_id, "result", result if status == "done" else "", board, unfinished)]


def _board_cards(kanban_db, path: Path, board: str) -> list[Source]:
    """One board's work cards (``is_work_card``, the set the work API lists) over a read-only connection (a pass
    never initializes or migrates a board), as ``_card_lines``; the result is the card's result, or its latest run's
    summary."""
    conn = sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True, timeout=5)
    conn.row_factory = sqlite3.Row
    conn.text_factory = kanban_db._lossy_text
    try:
        out = []
        rows = conn.execute("SELECT id, goal_mode, body, status FROM tasks "
                            "ORDER BY created_at, rowid").fetchall()
        for row in rows:
            if not is_work_card(row["goal_mode"], row["body"], row["status"]):
                continue
            task_id = row["id"]
            task = kanban_db.get_task(conn, task_id)
            comments = [(c.author or "", c.body or "") for c in kanban_db.list_comments(conn, task_id)]
            result = ""
            if task.status == "done":
                runs = [r for r in kanban_db.list_runs(conn, task_id) if r.summary]
                result = task.result or (runs[-1].summary if runs else "")
            reason = block_reason([(e.kind, e.payload) for e in kanban_db.list_events(conn, task_id)])
            out += _card_lines(task_id, board, task.title, task.body, task.status, comments, result or "", reason)
        return out
    finally:
        conn.close()


def hermes_card_sources() -> list[Source]:
    """The connected Hermes board's work cards (``lina_board_hermes.work_shows``, the set ``work_api_hermes`` serves)
    from the last good pass of this process's reader (``last_cards``; none without one), under the ids and the board
    the work API looks them up by: ``hermes:<id>`` on the default board (``board_key(None)``)."""
    from hermes_cli import lina_board_hermes as hermes
    out = []
    for show, _goal_mode in hermes.work_shows(hermes.last_cards() or []):
        task = show["task"]
        comments = [(str(c.get("author") or ""), str(c.get("body") or ""))
                    for c in show.get("comments") or [] if isinstance(c, dict)]
        runs = [r for r in show.get("runs") or [] if isinstance(r, dict) and r.get("summary")]
        result = task.get("result") or (runs[-1]["summary"] if runs else "")
        events = [(str(e.get("kind") or ""), e.get("payload") if isinstance(e.get("payload"), dict) else None)
                  for e in show.get("events") or [] if isinstance(e, dict)]
        out += _card_lines(hermes.hermes_id(task["id"]), board_key(None), str(task.get("title") or ""),
                           task.get("body"), str(task.get("status") or ""), comments, str(result or ""),
                           block_reason(events))
    return out


def write_board(root: Path | None = None, now: float | None = None, lives: dict[str, dict] | None = None,
                usable: bool = True) -> dict:
    """The background step: every run's and work card's sources, then ``write``."""
    root, now = _root(root), time.time() if now is None else now
    return write(run_sources(root, now, lives) + card_sources() + hermes_card_sources(), root, now, usable)


@contextmanager
def _model_profile(root: Path) -> Iterator[str | None]:
    """Run the block as the profile whose ``auxiliary.board_lines`` model and secrets the writer calls with
    (lina#702). The writer is host-wide, so the host picks that profile: ``worker.board_lines_profile`` in the LINA
    root's config. It is bound the way a cron fire binds its owner: the profile's secret scope, its home for config
    reads, and multiplex semantics when it is not this process's own profile. Unset, a single-profile process calls
    as itself, as before; a multiplexed gateway has no profile of its own (``get_secret`` raises with no scope), so
    nothing is called. Yields the setting's problem, None when the block may call the model."""
    from agent.secret_scope import is_multiplex_active
    from hermes_cli.worker import settings
    name = settings.section(root).get("board_lines_profile")
    if not name:
        yield (f"{PROFILE_SETTING} is not set in the LINA root's config.yaml, and a multiplexed gateway has no "
               "profile of its own to call the model as") if is_multiplex_active() else None
        return
    from hermes_cli.profiles import get_profile_dir, profile_exists
    if not isinstance(name, str) or not profile_exists(name):
        yield f"{PROFILE_SETTING} names {name!r}, which is not a profile on this host"
        return
    from agent.secret_scope import (reset_multiplex_context, reset_secret_scope, serves_routed_profile,
                                    set_multiplex_context, set_secret_scope)
    from hermes_cli.env_loader import hydrate_profile_secret_sources
    from hermes_constants import reset_hermes_home_override, set_hermes_home_override
    from tui_gateway.launch_profile_policy import owner_secret_scope
    home = get_profile_dir(name)
    hydrate_profile_secret_sources(home)  # before the scope is frozen, as the cron fire does
    home_token = set_hermes_home_override(home)
    scope_token = set_secret_scope(owner_secret_scope(home), profile_home=str(home))
    context_token = set_multiplex_context(True) if serves_routed_profile() else None
    try:
        yield None
    finally:
        if context_token is not None:
            reset_multiplex_context(context_token)
        reset_secret_scope(scope_token)
        reset_hermes_home_override(home_token)


def _pass(worker_root: Path) -> None:
    from hermes_cli.config_backend import ConfigBackendUnavailable
    from hermes_cli.worker.hub_store import Store
    with Store(worker_root).lock("plain", wait=False) as held:  # one writer at a time across processes
        if not held:
            return
        try:
            with _model_profile(_root(None)) as problem:
                _say_setting(problem)
                write_board(usable=problem is None)
        except (Exception, ConfigBackendUnavailable):  # a broken pass must not end the thread; the next one retries
            log.exception("board lines writer failed")


def writer_thread(worker_root: Path, stop: threading.Event) -> threading.Thread:
    """The writer's own daemon thread, not started: a pass now, then one every EVERY_S until ``stop`` is set. It
    runs in a copy of the caller's context (profile scope), like the cron worktree prune and the curator's model
    pass. Every process that starts one (the gateway, the worker service) shares the ``plain`` lock, so on one host
    a second writer's passes are no-ops, never a second model caller."""
    def loop() -> None:
        while True:
            _pass(worker_root)
            if stop.wait(EVERY_S):
                return
    return threading.Thread(target=copy_context().run, args=(loop,), name="lina-board-lines", daemon=True)


def start_writer(worker_root: Path, stop: threading.Event) -> threading.Thread:
    thread = writer_thread(worker_root, stop)
    thread.start()
    return thread
