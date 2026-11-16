"""Personal documents at the tool layer (lina#308, ``agent/personal_work.py``, docs/memory-layer.md).

``mark_personal`` records the owner's word that a document is personal, for the job of the session. The
``pre_tool_call`` hook does two things before any tool runs: a document a call names that sits under a folder with
the marker file ``.lina-personal`` is marked for the job (``how = folder``), and a call that would post a marked
document or text derived from it to a public place (a repository push, an issue, pull request, gist or release, a
public posting connector) or upload it to a third-party service (``curl``/``wget`` uploads such as web OCR or
converters) is refused with a message saying why, and the refusal is in the job's run record. Model calls,
workers (``delegate_task``), the owner's own devices and chats are never refused here.
"""

from __future__ import annotations

import json
import logging
import shlex
from pathlib import Path
from typing import Any, Optional

from agent import personal_numbers, personal_work

from . import personal_push

logger = logging.getLogger(__name__)

MARK_TOOL = "mark_personal"

MARK_SCHEMA = {
    "name": MARK_TOOL,
    "description": (
        "Mark a document as the owner's personal document (contract, tax return, ID card, form scan) for this job, "
        "when the owner says so. From then on ID and account numbers in this job's stored messages keep only their "
        "last four characters, and the document or text from it cannot be posted publicly or uploaded to a "
        "third-party service (workers and model calls are fine). Put every extract, page image or copy of it in "
        "the returned work_dir or temp_dir: both are deleted when the job ends. A folder marked with a "
        f"'{personal_work.MARKER_FILE}' file marks its documents by itself."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "path": {"type": "string", "description": "The document (or a folder of documents) the owner named."},
        },
        "required": ["path"],
    },
}

# gh <group> <action> that publishes: issues, pull requests, gists and releases, and comments on them.
_GH_PUBLIC = {
    "issue": {"create", "comment", "edit"},
    "pr": {"create", "comment", "edit", "review"},
    "gist": {"create", "edit"},
    "release": {"create", "edit", "upload"},
}
_UPLOAD_FLAGS = ("-F", "--form", "--form-string", "-T", "--upload-file", "-d", "--data", "--data-binary",
                 "--data-raw", "--data-urlencode", "--json", "--post-file", "--body-file")
# Connectors that publish to public places, and the words of their tools that write there.
_PUBLIC_CONNECTORS = {"github", "gitlab", "x", "twitter", "reddit", "medium", "wordpress"}
_PUBLISHING_WORDS = ("create", "comment", "post", "publish", "upload", "update", "edit", "reply")
_READ_LIMIT = 1 << 20
_DERIVED_LINE_CHARS = 24


def handle_mark(args: dict[str, Any], session_id: str) -> str:
    from hermes_state_store import StoreUnavailableError
    from tools.registry import tool_error
    raw = str(args.get("path") or "").strip()
    if not raw:
        return tool_error("path is required: the document the owner said is personal")
    if not session_id:
        return tool_error("no session to mark the document for")
    path = str(Path(raw).expanduser().resolve())
    try:
        marks = personal_work.marks_for_home()
        job = marks.mark(session_id, path, "owner")
        folders = personal_work.open_job_folders(marks, job)
    except StoreUnavailableError as exc:
        return tool_error(f"the mark could not be stored: {exc}")
    return json.dumps({"marked": path, "job": job, "work_dir": str(folders.work), "temp_dir": str(folders.temp),
                       "exists": Path(path).exists()}, ensure_ascii=False)


def _strings(value: Any) -> list[str]:
    if isinstance(value, str):
        return [value]
    if isinstance(value, dict):
        return [text for item in value.values() for text in _strings(item)]
    if isinstance(value, (list, tuple)):
        return [text for item in value for text in _strings(item)]
    return []


def _words(command: str) -> list[list[str]]:
    """The simple commands of a shell line, split on ``&&``, ``||``, ``;`` and ``|``."""
    try:
        lexer = shlex.shlex(command, posix=True, punctuation_chars=True)
        lexer.whitespace_split = True
        tokens = list(lexer)
    except ValueError:
        tokens = command.split()
    commands, current = [], []
    for token in tokens:
        if token and set(token) <= set("&|;()"):
            commands.append(current)
            current = []
        else:
            current.append(token)
    commands.append(current)
    return [words for words in commands if words]


def _command_destination(words: list[str]) -> Optional[str]:
    program = Path(words[0]).name
    rest = words[1:]
    if program == "git" and "push" in rest:
        return "public: git push"
    if program == "gh" and len(rest) >= 2 and rest[1] in _GH_PUBLIC.get(rest[0], set()):
        return f"public: gh {rest[0]} {rest[1]}"
    if program == "gh" and rest[:1] == ["api"] and any(flag in rest for flag in ("-f", "-F", "--field", "--input")):
        return "public: gh api"
    if program in {"curl", "wget", "http", "https"} and any(
            word == flag or word.startswith(flag + "=") for word in rest for flag in _UPLOAD_FLAGS):
        return f"third party: {program} upload"
    return None


def destination(tool_name: str, args: dict[str, Any]) -> Optional[str]:
    """What kind of outside place the call sends to (``public: ...`` or ``third party: ...``), or None."""
    if tool_name == "terminal":
        for words in _words(str(args.get("command") or "")):
            found = _command_destination(words)
            if found:
                return found
        return None
    if tool_name.startswith("connectors__"):
        parts = tool_name.split("__")
        if len(parts) >= 3 and parts[1].lower() in _PUBLIC_CONNECTORS and any(
                word in parts[2].lower() for word in _PUBLISHING_WORDS):
            return f"public: {parts[1]} {parts[2]}"
    return None


def _named_paths(texts: list[str]) -> list[str]:
    """Existing paths the call names: whole arguments, shell words, and ``@file`` / ``name=@file`` values."""
    found = []
    for text in texts:
        candidates = [text]
        for words in _words(text) if "\n" not in text else []:
            candidates += words
        for candidate in candidates:
            candidate = candidate.split("=@", 1)[-1].lstrip("@") if "@" in candidate else candidate
            if not candidate or len(candidate) > 4096 or ("/" not in candidate and "." not in candidate):
                continue
            try:
                if Path(candidate).expanduser().exists():
                    found.append(str(Path(candidate).expanduser().resolve()))
            except (OSError, ValueError):
                continue
    return list(dict.fromkeys(found))


def _read_text(path: Path) -> str:
    try:
        if path.is_file() and path.stat().st_size <= _READ_LIMIT:
            return path.read_text(encoding="utf-8-sig", errors="ignore")
    except OSError:
        pass
    return ""


def _derived_lines(marked: list[str], folders) -> set[str]:
    """Lines long enough to be the document's own, from marked text documents and the job's derived files."""
    sources = [Path(p) for p in marked if Path(p).is_file()]
    for root in (folders.work, folders.temp):
        if root.is_dir():
            sources += [p for p in root.rglob("*") if p.is_file() and not p.is_symlink()]
    lines: set[str] = set()
    for source in sources:
        lines.update(line.strip() for line in _read_text(source).splitlines()
                     if len(line.strip()) >= _DERIVED_LINE_CHARS)
    return lines


def carries_personal(texts: list[str], marked: list[str], folders) -> Optional[str]:
    """Why the call carries a marked document or text derived from it, or None."""
    for path in _named_paths(texts):
        if personal_work.is_marked(marked, path) or folders.holds(path) or personal_work.marker_folder(path):
            return f"it names the personal document {path}"
    sent = texts + [_read_text(Path(path)) for path in _named_paths(texts)]
    if any(personal_numbers.find(text) for text in sent):
        return "it carries an ID or account number"
    names = {Path(p).name for p in marked if len(Path(p).name) >= 5}
    if any(name in text for text in texts for name in names):
        return "it names a personal document"
    lines = _derived_lines(marked, folders)
    if lines and any(line in text for text in sent for line in lines):
        return "it carries text from a personal document"
    return None


_PUSH_HOW = "push from a job without a personal document, or remove the document from the commits"


def _terminal_cwd(args: dict[str, Any], task_id: str) -> str:
    """Where a terminal command starts on this machine: its ``workdir``, the task's recorded cwd, else this
    process's."""
    from tools.terminal_tool import get_session_cwd
    from tools.terminal_tool_config import _safe_getcwd
    return str(args.get("workdir") or get_session_cwd(task_id) or _safe_getcwd())


def pushed_personal(command: str, cwd: str, marked: list[str], folders) -> Optional[str]:
    """Why the commits a line's ``git push`` would send carry a personal document or text from one, or cannot be
    shown not to; None when every push sends nothing personal."""
    lines = None
    try:
        for sent in personal_push.outgoing(_words(command), cwd):
            for path in sent.paths:
                if personal_work.is_marked(marked, path) or folders.holds(path) or personal_work.marker_folder(path):
                    return f"the commits it sends carry the personal document {path}"
            if any(personal_numbers.find(line) for line in sent.added):
                return "the commits it sends carry an ID or account number"
            lines = _derived_lines(marked, folders) if lines is None else lines
            if lines and any(line.strip() in lines for line in sent.added):
                return "the commits it sends carry text from a personal document"
    except personal_push.Undetermined as exc:
        return f"the commits it sends cannot be checked ({exc}); {_PUSH_HOW}"
    return None


def _mark_from_folders(texts: list[str], session_id: str) -> None:
    folder_marked = [path for path in _named_paths(texts) if personal_work.marker_folder(path)]
    if not folder_marked:
        return
    marks = personal_work.marks_for_home()
    already = marks.marked_paths(session_id)
    for path in folder_marked:
        if not personal_work.is_marked(already, path) and Path(path).name != personal_work.MARKER_FILE:
            marks.mark(session_id, path, "folder")


def pre_tool_call(tool_name: str = "", args: Optional[dict[str, Any]] = None, session_id: str = "",
                  task_id: str = "", **_kwargs) -> Optional[dict[str, str]]:
    """The ``pre_tool_call`` hook: mark documents from marked folders, refuse public posts and third-party uploads
    of personal material, and a marked job's ``git push`` whose commits carry it. A store that cannot be reached
    refuses such a post rather than let it through."""
    from hermes_state_store import StoreUnavailableError
    args = args if isinstance(args, dict) else {}
    session = session_id or personal_work.current_session_id()
    texts = _strings(args)
    where = destination(tool_name, args)
    if not session:
        return None
    try:
        _mark_from_folders(texts, session)
        if where is None:
            return None
        marks = personal_work.marks_for_home()
        marked = marks.marked_paths(session)
        job = marks.job_of(session)
        folders = personal_work.folders_for(job)
        why = carries_personal(texts, marked, folders) if marked or any(
            personal_work.marker_folder(p) for p in _named_paths(texts)) else None
        if why is None and marked and tool_name == "terminal":
            why = pushed_personal(str(args.get("command") or ""), _terminal_cwd(args, task_id), marked, folders)
        if why is None:
            return None
        marks.record(job, "refused", {"tool": tool_name, "destination": where, "why": why})
    except StoreUnavailableError as exc:
        if where is None:
            logger.warning("could not record a personal-folder mark: %s", exc)
            return None
        return {"action": "block", "message": (
            f"Refused ({where}): personal-document marks cannot be checked because the memory store is "
            f"unreachable ({exc}).")}
    return {"action": "block", "message": (
        f"Refused ({where}): {why}. A personal document, and text derived from it, is never posted to a public "
        "place or uploaded to a third-party service; workers, model calls and the owner's own devices are fine.")}
