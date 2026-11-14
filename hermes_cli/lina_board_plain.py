"""Checked model-written card lines (lina#151): a short title, a "doing now" line and a result line for every
``lina worker`` run and every work card (``is_work_card``: a goal-mode card, or any card whose body has a
``## To-dos`` section), served as ``plain`` on run cards and work cards.

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
LIMITS = {"title": 25, "now": 60, "result": 80}
EMPTY = dict.fromkeys(FIELDS)
STOP_AFTER_S = 30 * 60
MAX_CALLS = 20
PASS_S = 90.0  # wall time one pass may spend calling the model; what is left stays pending for the next
EVERY_S = 60.0
_clock = time.monotonic
SOURCE_CHARS = 2000
ALLOWED_LATIN = frozenset({"LINA", "Honcho", "GitHub", "macOS", "iPhone", "Telegram", "Codex"})
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


def check_now(text: str) -> str | None:
    """At most 60 characters, one sentence, in the present progressive (~하는 중, ~고 있다) or a short noun
    phrase."""
    reason = _common(text, LIMITS["now"])
    if reason:
        return reason
    if not _one_sentence(text):
        return "more than one sentence"
    body = text.rstrip(_ENDERS).rstrip()
    if body.endswith(("다", "요")) and not body.endswith(("중이다", "고 있다", "중입니다", "고 있습니다")):
        return "not the present progressive or a noun phrase"
    return None


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
           "설명, 따옴표, 머리말 없이 한 줄만 답한다. id, 경로, 파일 이름, 명령, 영어 도구 이름, 번호는 쓰지 않는다.")
_PROMPTS = {
    "title": ("작업의 제목을 25자 안으로 쓴다. [대상] [무엇] [동작] 순서로, 동작 명사나 ~기로 끝낸다.\n"
              "예) 원문: 'ulw # Rotate the build machine disks before they fill' -> 빌드 기기 디스크 교체\n"
              "예) 원문: '알림 문구를 앱 화면에 맞추기 lina-system/lina-app#9' -> 앱 알림 문구 정리\n"
              "예) 원문: 'Add weekly backup check for the photo library' -> 사진 보관함 주간 백업 점검"),
    "now": ("지금 하고 있는 일을 60자 안의 한 문장으로 쓴다. '~하는 중' 같은 진행형이나 짧은 명사구로 쓴다.\n"
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


def ask(field: str, source: str) -> str:
    """One field from its source text through the auxiliary client; raises when the call fails."""
    from agent.auxiliary_client import call_llm
    response = call_llm(task=TASK, max_tokens=120, temperature=0.2, messages=[
        {"role": "system", "content": _SYSTEM + "\n\n" + _PROMPTS[field]},
        {"role": "user", "content": "원문: " + source[:SOURCE_CHARS]}])
    return _clean(response.choices[0].message.content or "")


# ---------------------------------------------------------------- the cache
# Version 2 keys a line by its board as well. The cache is regenerable, so an older ``line`` table is dropped (the
# next pass writes it again); the ``writer`` state is kept.
SCHEMA_VERSION = 2
_SCHEMA = ("CREATE TABLE IF NOT EXISTS line (kind TEXT NOT NULL, board TEXT NOT NULL, id TEXT NOT NULL, "
           "field TEXT NOT NULL, source_hash TEXT NOT NULL, text TEXT, error TEXT, at INTEGER NOT NULL, "
           "PRIMARY KEY (kind, board, id, field))",
           "CREATE TABLE IF NOT EXISTS writer (key TEXT PRIMARY KEY, value INTEGER)",
           # When each field's model call was last made, answered or not: the order of the next pass's queue.
           "CREATE TABLE IF NOT EXISTS attempt (kind TEXT NOT NULL, board TEXT NOT NULL, id TEXT NOT NULL, "
           "field TEXT NOT NULL, at INTEGER NOT NULL, PRIMARY KEY (kind, board, id, field))")


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


def write(sources: list[Source], root: Path | None = None, now: float | None = None, usable: bool = True) -> dict:
    """Write what changed: one call per field whose source hash moved (at most MAX_CALLS and PASS_S a run), the check's verdict
    stored either way; a field whose source is gone is cleared. A call that fails stores nothing and is tried again
    next run; with no usable model setting (``usable`` False) nothing is called, the due fields stay pending and the
    writer counts as stopped. The due fields queue by how long they have waited: never attempted first, then the oldest last
    attempt (a failed call counts as an attempt), so sources that change on every pass cannot keep a field waiting
    for more than a few passes. -> {called, failed, pending}."""
    now = int(time.time() if now is None else now)
    db = _open(root, write=True)
    try:
        with db:
            meta = _meta(db)
            db.execute("INSERT OR IGNORE INTO writer (key, value) VALUES ('first_run_at', ?)", (now,))
            cached = {key[:4]: key[4] for key in db.execute("SELECT kind, board, id, field, source_hash FROM line")}
            tried = {key[:4]: key[4] for key in db.execute("SELECT kind, board, id, field, at FROM attempt")}
            for s in sources:
                if not s.text.strip():
                    for table in ("line", "attempt"):
                        db.execute(f"DELETE FROM {table} WHERE kind = ? AND board = ? AND id = ? AND field = ?",
                                   (s.kind, s.board, s.id, s.field))
        due = sorted((s for s in sources if s.text.strip() and cached.get(_key(s)) != _hash(s)),
                     key=lambda s: tried.get(_key(s), -1))  # stable: source order among equals
        calls, failed, deadline = [], 0, _clock() + PASS_S
        for s in due[:MAX_CALLS] if usable else ():
            if _clock() >= deadline:
                break
            calls.append(s)
            with db:
                db.execute("INSERT OR REPLACE INTO attempt (kind, board, id, field, at) VALUES (?, ?, ?, ?, ?)",
                           (*_key(s), now))
            try:
                answer = ask(s.field, s.text.strip())
            except Exception as exc:  # health: allow BLE001 -- any provider error; logged once per distinct failure, the field is retried next pass
                _say_call_failed(s, exc)
                failed += 1
                continue
            _said["call"] = None
            reason = CHECKS[s.field](answer)
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
def _live_source(live: dict | None) -> str:
    """A pending question first, a long background job's label next, then the runner's last words."""
    live = live or {}
    if live.get("dash") in ("human", "background") and live.get("why"):
        return str(live["why"])
    return str(live.get("last") or "")


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


def _board_cards(kanban_db, path: Path, board: str) -> list[Source]:
    """One board's work cards (``is_work_card``, the set the work API lists) over a read-only connection (a pass
    never initializes or migrates a board): title from the card title plus the issue it names, now from its latest
    comment, result from its result (or its latest run's summary) once it is done."""
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
            issue = _ISSUE_REF.search(task.body or "")
            comments = [c for c in kanban_db.list_comments(conn, task_id) if (c.body or "").strip()]
            result = ""
            if task.status == "done":
                runs = [r for r in kanban_db.list_runs(conn, task_id) if r.summary]
                result = task.result or (runs[-1].summary if runs else "")
            out += [Source("card", task_id, "title", task.title + (f" {issue.group(0)}" if issue else ""), board),
                    Source("card", task_id, "now", comments[-1].body if comments and task.status != "done" else "",
                           board),
                    Source("card", task_id, "result", result or "", board)]
        return out
    finally:
        conn.close()


def write_board(root: Path | None = None, now: float | None = None, lives: dict[str, dict] | None = None,
                usable: bool = True) -> dict:
    """The background step: every run's and work card's sources, then ``write``."""
    root, now = _root(root), time.time() if now is None else now
    return write(run_sources(root, now, lives) + card_sources(), root, now, usable)


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
