"""The hub's own health checks, read from status files (lina#233).

The hub runs some checks outside LINA's code: tool versions and sign-ins across machines, the daily tool update, a
backup network line, a standby agent. The core learns none of them. Each check writes one small
``<LINA root>/status/*.json`` file ``{name, ok, line, at, stale_after}`` (``at`` Unix epoch seconds, ``stale_after``
seconds) and the client API serves the folder; a missing folder is an empty list.

A file reads at most 64 KB. Only regular files are read: a directory or a FIFO named ``*.json`` is not a check and is
never opened (a FIFO would block). A file older than its ``stale_after`` is ``stale`` (that wins over
``ok``/``failing``); a malformed file is one ``error`` row naming it with the file's mtime, never a failed response.
``json.loads`` takes ``NaN``/``Infinity`` and ``json.dumps`` writes them back as bare tokens a browser cannot parse,
so a time that is not a finite number, or a negative ``stale_after``, is malformed too. So is text that is not valid
UTF-8 (a lone surrogate is valid JSON but ``json.dumps(ensure_ascii=False)`` cannot encode it), and a file name of
non-UTF-8 bytes is served with U+FFFD for those bytes.
"""

from __future__ import annotations

import json
import math
import time
from pathlib import Path

MAX_BYTES = 64 * 1024

class _Malformed(Exception):
    """A status file that cannot be served as a check; the message is the short reason in the error row."""

def _field(data: dict, key: str, kind: type):
    """One required field of a check's shape; a missing or wrong-typed one is the error row's short reason."""
    if key not in data:
        raise _Malformed(f"missing {key}")
    value = data[key]
    if kind is bool:
        good, what = isinstance(value, bool), "a boolean"
    elif kind is str:
        good, what = isinstance(value, str), "a string"
        if good:
            try:
                value.encode("utf-8")
            except UnicodeEncodeError as exc:
                raise _Malformed(f"{key} is not valid text") from exc
    else:
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise _Malformed(f"{key} is not a number")
        # float() refuses an int literal too large for one; that is no finite second count either.
        try:
            value = float(value)
        except OverflowError as exc:
            raise _Malformed(f"{key} is not a finite number") from exc
        if not math.isfinite(value):
            raise _Malformed(f"{key} is not a finite number")
        return value
    if not good:
        raise _Malformed(f"{key} is not {what}")
    return value

def _check(path: Path) -> tuple[str, bool, str, float, float]:
    try:
        with path.open("rb") as fh:
            raw = fh.read(MAX_BYTES)
    except OSError as exc:
        raise _Malformed("unreadable") from exc
    try:
        data = json.loads(raw.decode("utf-8"))
    except UnicodeDecodeError as exc:
        raise _Malformed("not UTF-8") from exc
    except ValueError as exc:
        raise _Malformed("not JSON") from exc
    if not isinstance(data, dict):
        raise _Malformed("not a JSON object")
    name, ok, line = _field(data, "name", str), _field(data, "ok", bool), _field(data, "line", str)
    at, stale_after = _field(data, "at", float), _field(data, "stale_after", float)
    if stale_after < 0:
        raise _Malformed("stale_after is negative")
    return name, ok, line, at, stale_after

def _text(value: str) -> str:
    """A file name as served text. On Linux a name of non-UTF-8 bytes comes back from glob with surrogate escapes, and
    no UTF-8 body can carry those, so they become U+FFFD instead of failing the whole response."""
    try:
        value.encode("utf-8")
    except UnicodeEncodeError:
        try:
            raw = value.encode("utf-8", "surrogateescape")
        except UnicodeEncodeError:  # a name Windows fsdecode kept as a lone high surrogate
            raw = value.encode("utf-8", "surrogatepass")
        return raw.decode("utf-8", "replace")
    return value

def _error_row(path: Path, reason: str) -> dict:
    name = _text(path.name)
    try:
        at = path.stat().st_mtime
    except OSError:
        at = time.time()
    return {"name": _text(path.stem), "ok": False, "line": f"{name}: {reason}", "at": at, "staleAfter": None,
            "stale": False, "state": "error", "file": name}

def _row(path: Path, now: float) -> dict:
    try:
        name, ok, line, at, stale_after = _check(path)
    except _Malformed as exc:
        return _error_row(path, str(exc))
    stale = now - at > stale_after
    return {"name": name, "ok": ok, "line": line, "at": at, "staleAfter": stale_after, "stale": stale,
            "state": "stale" if stale else "ok" if ok else "failing", "file": _text(path.name)}

def checks(now: float | None = None) -> dict:
    """``{version: 1, at, checks}`` for every regular ``status/*.json`` file, newest ``at`` first."""
    from hermes_cli.lina_deps import lina_root
    now = time.time() if now is None else now
    folder = lina_root() / "status"
    found = sorted(folder.glob("*.json")) if folder.is_dir() else []
    paths = [path for path in found if path.is_file()]  # a FIFO named *.json would block open() forever
    rows = [_row(path, now) for path in paths]
    rows.sort(key=lambda row: row["at"], reverse=True)
    return {"version": 1, "at": now, "checks": rows}
