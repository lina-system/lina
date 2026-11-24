"""Private Rust read_file policy. The supervisor routes this in the owning session's scope."""

from __future__ import annotations

import os
import stat
from difflib import SequenceMatcher
from heapq import nsmallest
from pathlib import Path

from agent.file_safety import get_nt_namespace_error, get_read_block_error
from agent.redact import _is_secret_file_arg, redact_sensitive_text
from tools.file_operations_common import ReadResult
from tools.file_tools import (
    _SPECIAL_FILE_KINDS, _get_max_read_chars, _is_blocked_device, _is_blocked_device_path,
)
from tools.file_tools_paths import _expand_tilde
from tools.terminal_scope import get_terminal_scope, terminal_env
from tools.tool_output_limits import get_tool_output_limits
from tui_gateway.contracts.compute_host import ReadFileHintArgs
from tui_gateway.session_workdir import _session_cwd


def _identity(st: os.stat_result) -> dict:
    return {
        "dev": st.st_dev, "ino": st.st_ino, "size": st.st_size,
        "mtime_ns": st.st_mtime_ns, "ctime_ns": st.st_ctime_ns,
    }


def _parent_versions(path: Path) -> list[tuple[int, int, int]]:
    # get_read_block_error resolves its input again. A rename or replacement
    # of a canonical component during that check invalidates the decision.
    stats = (parent.lstat() for parent in path.parents)
    return [(s.st_dev, s.st_ino, s.st_ctime_ns) for s in stats]


def _suggestion_score(wanted: str, name: str) -> int:
    # FileOperations._suggest_similar_files uses these ranks after its ls probe.
    stem, ext = os.path.splitext(wanted)
    other_stem, other_ext = os.path.splitext(name)
    if wanted == name:
        return 100
    if stem == other_stem:
        return 90
    if name.startswith(wanted) or wanted.startswith(name):
        return 70
    if wanted in name:
        return 60
    if name in wanted and len(name) > 2:
        return 40
    if ext and ext == other_ext and len(set(wanted) & set(name)) >= 0.4 * max(len(wanted), len(name)):
        return 30
    return 50 if SequenceMatcher(None, wanted, name).ratio() >= 0.8 else 0


def _missing_result(target: Path, before: list[tuple[int, int, int]] | None) -> dict:
    result = ReadResult(error=f"File not found: {target}", not_found=True)
    if not before or os.scandir not in os.supports_fd or get_read_block_error(str(target.parent)):
        return result.to_dict()
    try:
        fd = os.open(target.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            parent = os.fstat(fd)
            if before[0] != (parent.st_dev, parent.st_ino, parent.st_ctime_ns):
                return result.to_dict()
            with os.scandir(fd) as entries:
                names = nsmallest(50, (e.name for e in entries if not e.name.startswith(".")))
        finally:
            os.close(fd)
        wanted = target.name.lower()
        ranked = sorted((-_suggestion_score(wanted, name.lower()), name) for name in names)
        for negative_score, name in ranked:
            if not negative_score:
                break
            candidate = str(target.parent / name)
            if not get_read_block_error(candidate) and not _is_blocked_device(candidate):
                result.similar_files.append(redact_sensitive_text(candidate, file_read=True))
                if len(result.similar_files) == 5:
                    break
        if _parent_versions(target) != before:
            result.similar_files.clear()
    except OSError:
        result.similar_files.clear()
    return result.to_dict()


def _decision(session: dict, frame: dict) -> dict:
    from hermes_constants import get_hermes_home

    if not session:
        return {"error": "Invalid read_file policy request."}
    if session.get("profile_home") and (
        get_terminal_scope() is None
        or Path(session["profile_home"]).resolve() != get_hermes_home().resolve()
    ):
        return {"error": "File policy is unavailable for this session."}
    if terminal_env("TERMINAL_ENV", "local") != "local":
        return {"error": "Rust read_file requires a local terminal backend."}
    if get_terminal_scope() is None:
        from hermes_cli.config import load_config_readonly

        if load_config_readonly().get("terminal", {}).get("backend", "local") != "local":
            return {"error": "Rust read_file requires a local terminal backend."}
    op = frame.get("op")
    fields = {"type", "sid", "turn_id", "request_id", "op"}
    if op == "hints":
        if frame.keys() - (fields | {"hint_args"}) or "hint_args" not in frame:
            return {"error": "Invalid read_file policy request."}
        return {"ok": True, "hints": _completion_hints(session, frame)}
    raw = frame.get("path")
    fields.add("path")
    if op == "redact":
        fields.update(("identity", "resolved", "content", "hint_args"))
    if op not in {"authorize", "redact"} or frame.keys() - fields or not isinstance(raw, str):
        return {"error": "Invalid read_file policy operation."}
    if error := get_nt_namespace_error(raw, verb="Read"):
        return {"error": error}
    cwd = Path(_session_cwd(session))
    if not cwd.is_absolute():
        return {"error": "The session has no absolute working directory."}
    target = (cwd / _expand_tilde(raw)).resolve()
    if _is_blocked_device(raw, base_dir=cwd) or _is_blocked_device_path(str(target)):
        return {"error": f"Cannot read '{raw}': this is a device file that would block or produce infinite output."}
    before = _parent_versions(target) if target.parent.is_dir() else None
    if error := get_read_block_error(str(target)):
        return {"error": error}
    try:
        metadata = target.lstat()
    except (FileNotFoundError, NotADirectoryError):
        if op == "redact":
            return {"error": "Read no longer matches its file authorization."}
        return {"result": _missing_result(target, before)}
    if before is None or before != _parent_versions(target) or stat.S_ISLNK(metadata.st_mode):
        return {"error": "File changed during authorization. Retry the read."}
    if stat.S_ISDIR(metadata.st_mode):
        return {"result": ReadResult(error=(
            f"Cannot read '{target}': not a regular file (directory, dangling symlink, "
            "FIFO, socket, or device). Reading it could block indefinitely.")).to_dict()}
    if not stat.S_ISREG(metadata.st_mode):
        kind = next((label for check, label in _SPECIAL_FILE_KINDS if check(metadata.st_mode)),
                    "a special (non-regular) file")
        return {"result": {"success": False, "note": (
            f"'{raw}' is {kind}, not a regular file — reading it would block indefinitely, "
            "so no read was attempted. Use terminal utilities if you need to interact with it.")}}
    identity = _identity(metadata)
    max_chars = _get_max_read_chars()
    if op == "authorize":
        limits = get_tool_output_limits()
        return {"ok": True, "resolved": str(target), "identity": identity, "max_chars": max_chars,
                "display_path": redact_sensitive_text(raw, file_read=True),
                "max_line_length": limits["max_line_length"], "max_lines": limits["max_lines"]}
    content = frame.get("content")
    if (frame.get("resolved") != str(target) or frame.get("identity") != identity
            or not isinstance(content, str) or len(content) > max_chars):
        return {"error": "Read no longer matches its file authorization."}
    reply = {"ok": True, "content": redact_sensitive_text(
        content, file_read=True, secret_file=_is_secret_file_arg(str(target)))}
    if frame.get("hint_args") is not None:
        reply["hints"] = _completion_hints(session, frame)
    return reply


def _completion_hints(session: dict, frame: dict) -> str:
    args = ReadFileHintArgs.model_validate(frame["hint_args"]).model_dump(exclude_none=True)
    tracker = getattr(session.get("agent"), "_subdirectory_hints", None)
    return (tracker.check_tool_call("read_file", args) or "") if tracker is not None else ""


def handle_file_policy(session: dict, frame: dict) -> dict:
    """Handle private read policy without echoing a frame in an error or log."""
    route = {key: frame.get(key) for key in ("sid", "turn_id", "request_id")}
    ack = {"type": "read_file.policy.ack", **route, "ok": False}
    try:
        if (frame.get("type") != "read_file.policy" or not route["sid"]
                or not route["turn_id"] or not route["request_id"]):
            return ack
        decision = _decision(session, frame)
        # Even a filename in a refusal may contain a registered value.
        if "error" in decision:
            decision["error"] = redact_sensitive_text(decision["error"], file_read=True)
        if "result" in decision:
            for key, value in decision["result"].items():
                if isinstance(value, str):
                    decision["result"][key] = redact_sensitive_text(value, file_read=True)
        return {**ack, **decision}
    except Exception:
        # This is the private IPC boundary. Exceptions may contain file bytes.
        return {**ack, "error": "File policy is unavailable for this read."}
