"""Links on an item (GitHub, kanban cards, worker runs, Desk, files, URLs), their labels and live states, and the
read-only guard and runner for an item's check command."""

from __future__ import annotations

import contextlib
import os
import re
import signal
import sqlite3
import subprocess
import time
from contextlib import closing
from pathlib import Path

from hermes_cli.work.common import die, mask, now
from hermes_cli.work.env import current
from hermes_cli.work.locale import t, word

OMO_ID = r"OT-[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*"
CHECK_TIMEOUT = 30
# A Hermes kanban card id: one card is one work session (t_ + eight hex digits). A card off the default board names
# its board: kanban:<board>/t_xxxxxxxx.
KANBAN_LINK = re.compile(r"kanban:(?:(?P<board>[A-Za-z0-9][A-Za-z0-9_-]{0,63})/)?(?P<card>t_[0-9A-Fa-f]{8})$")
LINK_FORMS = "gh:repo#n · ms:repo/n · omo:OT-ID · session:<topic> · kanban:t_<8 hex> · todoist:<id> · notion:<url> " \
             "· desk:<path> · URL"


def _session_link(v: str, owner: str) -> str:
    from hermes_cli.work.people import agent
    if re.match(r"\d{8}-\d{6}-", v):
        return f"session:{v}"
    if ":" in v:
        p, n = v.split(":", 1)
        return f"session:{agent(p)}:{n}"
    return f"session:{owner}:{v}"


def _kanban_link(link: str) -> str | None:
    m = KANBAN_LINK.fullmatch(link)
    if m:
        board = (m.group("board") or "").lower()
        return f"kanban:{board}/{m.group('card')}" if board else f"kanban:{m.group('card')}"
    if link.startswith("kanban:"):
        die(t("칸반 링크 형식을 모름: {link} (kanban:t_<8 hex> 또는 kanban:<board>/t_<8 hex>)", link=link))
    return None


_KEPT = (r"gh:([\w.-]+/)?[\w.-]+#\d+$", r"ms:([\w.-]+/)?[\w.-]+/\d+$", r"dv:[A-Za-z]+-[\w-]+(@[\w.-]+)?$",
         "omo:" + OMO_ID + "$", r"(todoist|notion):\S+", r"desk:", r"https?://", r"file:")


def norm_link(link: str, owner: str) -> str:
    link = link.strip()
    card = _kanban_link(link)
    if card:
        return card
    m = re.match(r"https?://github\.com/([^/]+)/([^/]+)/(?:issues|pull)/(\d+)", link)
    if m:
        return f"gh:{m.group(1)}/{m.group(2)}#{m.group(3)}"
    m = re.match(r"https?://github\.com/([^/]+)/([^/]+)/milestone/(\d+)", link)
    if m:
        return f"ms:{m.group(1)}/{m.group(2)}/{m.group(3)}"
    m = re.match(r"session:(.+)$", link)
    if m:
        return _session_link(m.group(1), owner)
    if any(re.match(rx, link) for rx in _KEPT):
        return link
    if link.startswith(("/", "~")):
        return "file:" + os.path.expanduser(link)
    die(t("링크 형식을 모름: {link} ({forms})", link=link, forms=LINK_FORMS))


def gh_repo(spec: str) -> tuple[str, str]:
    """'repo' | 'owner/repo' -> (owner, repo); a bare name takes ``work.github.repos`` or ``work.github.owner``."""
    if "/" in spec:
        o, r = spec.split("/", 1)
        return o, r
    gh = current().section("work").get("github")
    gh = gh if isinstance(gh, dict) else {}
    repos = gh.get("repos") if isinstance(gh.get("repos"), dict) else {}
    return str(repos.get(spec) or gh.get("owner") or ""), spec


def _last(path: str) -> str:
    return path.rstrip("/").split("/")[-1]


def _label_session(v: str) -> str:
    from hermes_cli.work.people import name
    if ":" in v:
        p, n = v.split(":", 1)
        return t("{who} 세션 {name}", who=name(p), name=n)
    return t("세션 {name}", name=v)


def _label_kanban(spec: str) -> str:
    board, sep, card = spec.partition("/")
    return t("작업 세션 {card}", card=(board + "/" + card) if sep else spec)


_LABELS = {
    "gh": lambda v: f"{gh_repo(v.split('#')[0])[1]} #{v.split('#')[1]}",
    "ms": lambda v: t("{repo} 마일스톤 {n}", repo=gh_repo(v.rsplit('/', 1)[0])[1], n=v.rsplit("/", 1)[1]),
    "dv": lambda v: t("작업 {key}", key=v.split("@")[0]),
    "omo": lambda v: t("작업 {key}", key=v),
    "session": _label_session,
    "kanban": _label_kanban,
    "todoist": lambda v: t("Todoist 할 일"),
    "notion": lambda v: t("Notion 페이지"),
    "desk": lambda v: "Desk " + _last(v),
    "file": lambda v: t("파일 {name}", name=_last(v)),
}


def link_label(link: str) -> str:
    prefix, sep, rest = link.partition(":")
    fn = _LABELS.get(prefix) if sep else None
    if fn is None or (prefix == "omo" and not rest):
        return link[:60]
    return fn(rest)


# ---------------------------------------------------------------- kanban cards
# One card is one work session, so the card's own state is the to-do's: a live card is the work, a card that ended
# asks for the confirmation attached evidence asks for, and a card nobody can read is 모름 — never 끝남 (#249).
_KANBAN_CACHE: dict[str, tuple[str, int, str]] = {}  # link -> (status, completed_at, the board it was found on)
# A card a closed to-do waits on again (#249): it is planned, running, blocked or in review.
KANBAN_REOPEN = {"todo", "ready", "running", "blocked", "triage", "scheduled", "review"}


def kanban_parts(link: str) -> tuple[str, str]:
    m = KANBAN_LINK.fullmatch(link)
    return ((m.group("board") or "").lower(), m.group("card")) if m else ("", "")


def _kanban_db_card(path: Path, card: str) -> tuple[str, int] | None:
    """(status, completed_at) of the card in one board file, opened read-only, or None when it is not there."""
    try:
        if not path.is_file():
            return None
        with closing(sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True, timeout=5)) as db:
            db.row_factory = sqlite3.Row
            for ident in dict.fromkeys((card, card.lower())):
                row = db.execute("SELECT * FROM tasks WHERE id = ?", (ident,)).fetchone()
                if row and row["status"] is not None:
                    done_at = row["completed_at"] if "completed_at" in row.keys() else None
                    return str(row["status"]).strip().lower(), int(done_at or 0)
    except (OSError, ValueError, sqlite3.Error):
        return None
    return None


def _kanban_db_status(board: str, card: str) -> tuple[str, int, str] | None:
    """(status, completed_at, board) of the card. A named board reads its own file; a bare card id reads the default
    board, and when the card is not there, the one other board under the kanban root that has it (several: unread,
    #249). A pinned store is the only file read."""
    from hermes_cli.kanban_db import kanban_db_path
    found = _kanban_db_card(Path(kanban_db_path(board=board or None)), card)
    if found or board not in ("", "default") or os.environ.get("HERMES_KANBAN_DB"):
        return (*found, board or "default") if found else None
    from hermes_cli.kanban_db_paths import boards_root
    hits = [(*hit, path.parent.name.lower()) for path in sorted(boards_root().glob("*/kanban.db"))
            if (hit := _kanban_db_card(path, card))]
    if len(hits) > 1:
        return None  # the same id on several boards: never guessed
    return hits[0] if hits else None


def kanban_card(link: str) -> tuple[str, int, str]:
    """(status, completed_at, board) of the card; ('', 0, '') when it cannot be read. A missing store is never done."""
    if link not in _KANBAN_CACHE:
        board, card = kanban_parts(link)
        _KANBAN_CACHE[link] = (_kanban_db_status(board, card) if card else None) or ("", 0, "")
    return _KANBAN_CACHE[link]


def kanban_read(link: str) -> str:
    """The card's raw kanban status, or '' when the card cannot be read."""
    return kanban_card(link)[0]


def kanban_ended(link: str) -> bool:
    """Done, or archived with a completion time (#249). Archived without one never finished."""
    status, done_at, _ = kanban_card(link)
    return status == "done" or (status == "archived" and bool(done_at))


def card_key(link: str) -> tuple[str, str]:
    """(board, card id) of a kanban link as the store answers it: a bare link is the board the card was found on, so
    kanban:t_x and kanban:work/t_x are one card when it lives on work. A card nobody can read is ('', id)."""
    board, card = kanban_parts(link)
    return ((board or kanban_card(link)[2]) if kanban_read(link) else board, card.lower())


def kanban_state_word(status: str) -> str:
    return word("kanban", status or "", t("모름"))


# ---------------------------------------------------------------- read-only guard for checks
WRITE_RE = re.compile(r"""(?x)
 (?:^|[\s;&|(`'"])(?:rm|mv|cp|dd|tee|truncate|chmod|chown|ln|mkdir|rmdir|touch|kill|pkill|killall|reboot|shutdown|
     halt|launchctl|sudo|doas|rsync|scp|crontab|osascript)(?=\s|$|['";|&)])
 | \bsed\s+(-\w*\s+)*-i | \bperl\s+-\w*i
 | \bgit\s+(-C\s+\S+\s+)?(push|commit|reset|checkout|switch|merge|rebase|clean|stash|tag|pull|fetch|am|apply|restore|rm|mv|add|worktree|gc|prune)\b
 | \bgh\s+(pr|issue|release|repo|label|workflow|run|secret|variable)\s+(create|edit|close|merge|delete|reopen|comment|review|ready|lock|transfer|rerun|cancel|enable|disable|set|archive|rename|fork|sync)\b
 | \bgh\s+api\b.*(\s-X\s*|\s--method\s*)(POST|PATCH|PUT|DELETE)
 | \bgh\s+api\b.*\s(-f|-F|--field|--raw-field|--input)\s
 | \bsystemctl\s+(--user\s+)?(start|stop|restart|enable|disable|kill|reload|daemon-reload|mask|unmask|reset-failed)\b
 | \bcurl\b.*(\s-X\s*(POST|PUT|PATCH|DELETE)|\s--data|\s-d\s|\s-F\s|\s-T\s|\s--upload|\s-o\s|\s--output)
 | \bwget\b
 | \b(?:lina\s+|hermes\s+)?work\s+(add|link|unlink|note|drop|edit|wait|evidence|confirm|check|reopen|comment|reply|owner-done|owner-undo|run|import|migrate|sprint\s+(set|owner))\b
 | \blina\s+(worker|services|store|install|update)\b
 | (?:^|[\s;&|(`'"])(?:\S*/)?(?:session-start|hermes)(?=\s|$|['";|&)]) | \btodoist\b.*\b(add|close|update|delete)\b
 | \b(npm|pnpm|yarn|pip3?|brew|apt(-get)?|dnf|pacman|cargo|go|uv)\s+(install|add|remove|uninstall|upgrade|update|get|sync)\b
""")


def strip_quotes(s: str) -> str:
    return re.sub(r"'[^']*'|\"(?:\\.|[^\"\\])*\"", " ", s)


def refuse_reason(cmd: str) -> str | None:
    if not cmd or cmd.startswith("evidence:"):
        return None
    # a quoted single word ("open", 'closed') is data for jq/grep, not a command
    if WRITE_RE.search(re.sub(r"'[^'\s]*'|\"[^\"\s]*\"", " ", cmd)):
        return t("쓰기·변경 명령이 섞여 있다(검사는 읽기만)")
    bare = re.sub(r"\d?>&\d|&>\s*/dev/null|\d?>\s*/dev/null", " ", strip_quotes(cmd))
    inner = re.sub(r"\d?>\s*/dev/null|\d?>&\d", " ", cmd)
    if ">" in bare or re.search(r">\s*(~|\$HOME|/(?!dev/null))", inner):
        return t("출력 리다이렉트(>)는 못 쓴다")
    return None


def _check_env() -> dict[str, str]:
    """A check is a read-only shell line written by an agent: it gets the profile's env without secrets."""
    from tools.environments.local import build_subprocess_env
    return build_subprocess_env(extra={"WORK_CHECK": "1"})


def run_check(cmd: str, timeout: float = CHECK_TIMEOUT) -> dict:
    t0 = time.time()
    try:
        # windows-footgun: ok — checks run on the POSIX hub; the group lets a timeout kill the whole check
        p = subprocess.Popen(["/bin/bash", "-c", cmd], stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                             # detached-spawn: ok — awaited below; a timeout kills the whole group
                             stdin=subprocess.DEVNULL, cwd=str(Path.home()), env=_check_env(), start_new_session=True)
    except OSError as e:
        return {"rc": 127, "out": t("실행 실패: {e}", e=e), "secs": 0, "at": now()}
    try:
        out, _ = p.communicate(timeout=timeout)
        rc = p.returncode
    except subprocess.TimeoutExpired:
        with contextlib.suppress(ProcessLookupError):
            os.killpg(p.pid, signal.SIGKILL)  # windows-footgun: ok — checks run on the POSIX hub only
        try:
            out, _ = p.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            out = b""
        rc = 124
        out = (out or b"") + ("\n[" + t("{s}초 시간 초과", s=int(timeout)) + "]").encode()
    txt = mask((out or b"").decode("utf-8", "replace")).strip()
    if len(txt) > 600:
        txt = txt[:250] + " … " + txt[-300:]
    return {"rc": rc, "out": txt, "secs": round(time.time() - t0, 1), "at": now()}
