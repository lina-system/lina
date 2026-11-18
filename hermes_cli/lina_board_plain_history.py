"""A work card's step record (``plain.history``, G138): what each of its earlier now lines did, one past-tense
sentence each, newest first, served on the card's detail only.

The record grows from the now line (``lina_board_plain.write``): the model writes a now line's past tense in the
same call as the line, and when a card's line is stored as a new sentence, or goes away because the card ended, the
line it replaces is closed into the record, from when it was first stored (``at``) to when it was replaced
(``until``). A line whose past tense no check passed (``check_past``) is not recorded; a closed sentence equal to
the newest one recorded lengthens that one. A card whose earlier steps predate the record (``history_from``) is
backfilled once, from its own timed comments and block reasons: one call per card, each sentence checked, its time
the source item's. Like the rest of ``plain.db`` this is a display cache.
"""

from __future__ import annotations

import logging
import re
import sqlite3
from pathlib import Path
from typing import TYPE_CHECKING, Callable, Iterable

if TYPE_CHECKING:
    from hermes_cli.lina_board_plain import Source

HISTORY_MAX = 40  # lines kept and served per card
BACKFILL_CALLS = 3  # model calls one pass may spend on backfills, on top of the fields' MAX_CALLS
BACKFILL_RECENT_S = 14 * 86400  # a done card is backfilled only when its last step came this soon before the record
ITEM_CHARS = 300
SCHEMA = (
    "CREATE TABLE IF NOT EXISTS history (kind TEXT NOT NULL, board TEXT NOT NULL, id TEXT NOT NULL, "
    "at INTEGER NOT NULL, until INTEGER, text TEXT NOT NULL)",
    "CREATE INDEX IF NOT EXISTS history_card ON history (kind, board, id, at)",
    # The now line being shown: its sentence, its past tense (null when none passed ``check_past``), when first stored.
    "CREATE TABLE IF NOT EXISTS history_open (kind TEXT NOT NULL, board TEXT NOT NULL, id TEXT NOT NULL, "
    "now TEXT NOT NULL, past TEXT, at INTEGER NOT NULL, PRIMARY KEY (kind, board, id))",
    # Cards whose backfill was tried, answered or not: never asked again.
    "CREATE TABLE IF NOT EXISTS history_backfill (kind TEXT NOT NULL, board TEXT NOT NULL, id TEXT NOT NULL, "
    "at INTEGER NOT NULL, PRIMARY KEY (kind, board, id))",
)
_CARD = "kind = ? AND board = ? AND id = ?"
log = logging.getLogger(__name__)


def _card(s: Source) -> tuple[str, str, str]:
    return (s.kind, s.board, s.id)


def _close(db: sqlite3.Connection, card: tuple[str, str, str], past: str | None, at: int, until: int) -> None:
    if not past:
        return
    top = db.execute(f"SELECT rowid, text FROM history WHERE {_CARD} ORDER BY at DESC, rowid DESC LIMIT 1",
                     card).fetchone()
    if top and top[1] == past:
        db.execute("UPDATE history SET until = ? WHERE rowid = ?", (until, top[0]))
    else:
        db.execute("INSERT INTO history (kind, board, id, at, until, text) VALUES (?, ?, ?, ?, ?, ?)",
                   (*card, at, until, past))
    _prune(db, card)


def _prune(db: sqlite3.Connection, card: tuple[str, str, str]) -> None:
    db.execute(f"DELETE FROM history WHERE {_CARD} AND rowid NOT IN (SELECT rowid FROM history WHERE {_CARD} "
               "ORDER BY at DESC, rowid DESC LIMIT ?)", (*card, *card, HISTORY_MAX))


def record(db: sqlite3.Connection, s: Source, line: str, past: str | None, now: int) -> None:
    """A now line that passed its check was stored: the same sentence goes on (its past tense filled in if it had
    none), a new one closes the one shown into the record and is shown from *now*."""
    card = _card(s)
    shown = db.execute(f"SELECT now, past, at FROM history_open WHERE {_CARD}", card).fetchone()
    if shown and shown[0] == line:
        if shown[1] is None and past:
            db.execute(f"UPDATE history_open SET past = ? WHERE {_CARD}", (past, *card))
        return
    if shown:
        _close(db, card, shown[1], shown[2], now)
    db.execute("INSERT OR REPLACE INTO history_open (kind, board, id, now, past, at) VALUES (?, ?, ?, ?, ?, ?)",
               (*card, line, past, now))


def end(db: sqlite3.Connection, s: Source, now: int) -> None:
    """A card's now source went away (the card ended): the line shown last is closed into the record."""
    card = _card(s)
    shown = db.execute(f"SELECT past, at FROM history_open WHERE {_CARD}", card).fetchone()
    if shown:
        _close(db, card, shown[0], shown[1], now)
        db.execute(f"DELETE FROM history_open WHERE {_CARD}", card)


def lookup(kind: str, item: str, root: Path | None = None, board: str = "") -> list[dict]:
    """One card's record, ``[{at, until, text}]`` newest first; empty when there is none or no cache."""
    from hermes_cli.lina_board_plain import _open
    db = _open(root, write=False)
    if db is None:
        return []
    try:
        rows = db.execute(f"SELECT at, until, text FROM history WHERE {_CARD} ORDER BY at DESC, rowid DESC LIMIT ?",
                          (kind, board, item, HISTORY_MAX)).fetchall()
    except sqlite3.OperationalError:
        return []  # a cache written before the record existed has no table yet
    finally:
        db.close()
    return [{"at": at, "until": until, "text": text} for at, until, text in rows]


# ---------------------------------------------------------------- backfill
def items(comments: Iterable[tuple[int, str, str]], events: Iterable[tuple[int, str, dict | None]]) -> tuple:
    """A card's timed steps for its backfill, oldest first: ``(at, text)`` of its ``(at, author, body)`` comments
    that are not automatic notices, and of its ``(at, kind, payload)`` block events whose reason says something; each
    without ids (``bare``), on one line."""
    from hermes_cli.lina_board_plain import NOW_SKIP_AUTHORS, NOW_SKIP_HEAD, NOW_SKIP_REASONS, bare
    out = [(at, body) for at, author, body in comments
           if body.strip() and author not in NOW_SKIP_AUTHORS and not NOW_SKIP_HEAD.match(body.lstrip())]
    out += [(at, str((payload or {}).get("reason") or "")) for at, kind, payload in events if kind == "blocked"]
    steps = ((int(at or 0), " ".join(bare(text).split())[:ITEM_CHARS]) for at, text in out
             if text.strip() not in NOW_SKIP_REASONS)
    return tuple(sorted(step for step in steps if step[1]))


_SYSTEM = ("너는 작업 카드의 단계 기록을 쓴다. 한국어로, 짧고 쉬운 업무 말투로 쓴다. "
           "설명, 따옴표, 머리말 없이 번호 붙은 줄만 답한다. id, 경로, 파일 이름, 명령, 영어 도구 이름은 쓰지 않는다.")
PROMPT = ("아래는 한 작업 카드에 시각순으로 남은 글이다. 각 글이 그 단계에서 한 일을 말하면, 그 일을 40자 안의 과거형 "
          "한 문장으로 '글 번호. 문장' 꼴로 한 줄씩 쓴다('~함', '~했다', '~를 기다림'). '~하는 중', '~대기 중'으로 "
          "끝내지 않는다. 영문 글자, 화살표(→), 빗금(/), 말줄임(…)은 쓰지 않는다. 맡은 사람이나 봇의 이름을 주어로 "
          "쓰지 않는다. 한 일을 말하지 않는 글(인사, 접수 알림, 상태 낱말만 있는 글)은 건너뛴다.\n"
          "예) 1. 디스크를 비웠고 경고가 사라졌다 / 2. 검토를 통과했다 -> 1. 빌드 기기 디스크를 비움")
_NUMBERED = re.compile(r"^\s*(\d{1,2})\s*[.):]\s*(.+?)\s*$")


def ask(steps: tuple) -> str:
    """One backfill call through the auxiliary client; raises when the call fails."""
    from agent.auxiliary_client import call_llm
    from hermes_cli.lina_board_plain import TASK
    listed = "\n".join(f"{n}. {text}" for n, (_at, text) in enumerate(steps, 1))
    response = call_llm(task=TASK, max_tokens=1500, temperature=0.2,
                        messages=[{"role": "system", "content": _SYSTEM + "\n\n" + PROMPT},
                                  {"role": "user", "content": listed}])
    return str(response.choices[0].message.content or "")


def lines(answer: str, steps: tuple, names: frozenset[str]) -> list[tuple[int, str]]:
    """The ``(at, text)`` lines of a backfill *answer*: each numbered line that names one of *steps* and passes
    ``check_past``, at that step's time; a sentence equal to the one before it is kept once, at the earlier time."""
    from hermes_cli.lina_board_plain import check_past
    picked: dict[int, str] = {}
    for row in answer.splitlines():
        match = _NUMBERED.match(row)
        if not match or not 1 <= int(match.group(1)) <= len(steps) or int(match.group(1)) in picked:
            continue
        text = match.group(2).strip("\"'“”‘’ ")
        reason = check_past(text, names)
        if reason:
            log.debug("board history line refused: %s", reason)
            continue
        picked[int(match.group(1))] = text
    out: list[tuple[int, str]] = []
    for n in sorted(picked, key=lambda n: (steps[n - 1][0], n)):
        if not out or out[-1][1] != picked[n]:
            out.append((steps[n - 1][0], picked[n]))
    return out[-HISTORY_MAX:]


def backfill(db: sqlite3.Connection, sources: list[Source], now: int, names: frozenset[str],
             room: Callable[[], bool], said: Callable[[Source, Exception], None]) -> tuple[int, int]:
    """Backfill the cards not tried yet (``history_backfill``), unfinished ones first, then done ones by their last
    step: from their steps before ``history_from``, at most ``BACKFILL_CALLS`` calls while *room* holds. A card with
    no such step, or a done card whose last one is older than ``BACKFILL_RECENT_S``, is marked without a call; a
    failed call is told through *said* and tried again next pass. -> (calls, failed)."""
    since = (db.execute("SELECT value FROM writer WHERE key = 'history_from'").fetchone() or (now,))[0]
    tried = set(db.execute("SELECT kind, board, id FROM history_backfill").fetchall())
    due = [s for s in sources if s.field == "now" and s.kind == "card" and _card(s) not in tried]
    due.sort(key=lambda s: (not s.unfinished, -max((at for at, _ in s.items), default=0)))
    calls = failed = 0
    for s in due:
        steps = tuple(step for step in s.items if step[0] < since)[-HISTORY_MAX:]
        if steps and (s.unfinished or steps[-1][0] >= since - BACKFILL_RECENT_S):
            if calls == BACKFILL_CALLS or not room():
                continue
            calls += 1
            try:
                answer = ask(steps)
            except Exception as exc:  # health: allow BLE001 -- any provider error; logged once per distinct failure, the card is retried next pass
                said(s, exc)
                failed += 1
                continue
            with db:
                db.executemany("INSERT INTO history (kind, board, id, at, until, text) VALUES (?, ?, ?, ?, NULL, ?)",
                               [(*_card(s), at, text) for at, text in lines(answer, steps, names)])
                _prune(db, _card(s))
        with db:
            db.execute("INSERT OR REPLACE INTO history_backfill (kind, board, id, at) VALUES (?, ?, ?, ?)",
                       (*_card(s), now))
    return calls, failed
