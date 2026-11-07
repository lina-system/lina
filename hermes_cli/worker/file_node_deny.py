"""A file node's denied folders (``devices.workers.<name>.deny``): refused at submit, looked for after a run.

An entry starting with ``/`` is a folder by its absolute path; any other entry is a folder-name pattern (``#recycle``,
``@*``). A brief or an input that names a path under one is refused before the run is handed out. After a run the
node lists files changed under them since the run started (``file_node_bash.DENY_SCAN``); one such file owned by
the run's user, or a look that did not finish (time limit, a find error), ends the run as ``review``. Changes owned
by other users (backup, sync, indexing services) are counted but not held against the run. This is a check, not a
sandbox: the run can reach the whole volume, and an in-place edit of another user's file is not seen.
"""

from __future__ import annotations

import fnmatch
import re
from pathlib import Path

from hermes_cli.worker.report import NEXT_NOTES
from hermes_cli.worker.settings import Device

_TOKEN = re.compile(r"[^\s\"'`()<>|;,]+")


def split(deny: tuple[str, ...]) -> tuple[list[str], list[str]]:
    """``(absolute folders, folder-name patterns)``."""
    roots = [entry.rstrip("/") or "/" for entry in deny if entry.startswith("/")]
    return roots, [entry for entry in deny if not entry.startswith("/")]


def _hit(path: str, roots: list[str], names: list[str]) -> str | None:
    clean = path.rstrip("/.:")
    for root in roots:
        if clean == root or clean.startswith(root + "/"):
            return root
    parts = [part for part in clean.split("/") if part]
    for name in names:
        if any(fnmatch.fnmatchcase(part, name) for part in parts) and ("/" in clean or "*" not in name):
            return name
    return None


def in_text(text: str, deny: tuple[str, ...]) -> str | None:
    """The first path in *text* under a denied folder, with the entry it falls under."""
    roots, names = split(deny)
    for token in _TOKEN.findall(text):
        if "/" in token or token in names:
            entry = _hit(token, roots, names)
            if entry:
                return f"{token} (deny {entry})"
    return None


def in_inputs(inputs: list[str], deny: tuple[str, ...]) -> str | None:
    """The first input, or file inside an input folder, whose path falls under a denied folder."""
    roots, names = split(deny)
    for raw in inputs:
        path = Path(raw)
        for each in [path, *(sorted(path.rglob("*")) if path.is_dir() else [])]:
            relative = path.name if each == path else f"{path.name}/{each.relative_to(path).as_posix()}"
            entry = _hit(each.as_posix(), roots, []) or _hit("/" + relative, [], names)
            if entry:
                return f"{each} (deny {entry})"
    return None


def submit_refusal(dev: Device, texts: list[str], inputs: list[str]) -> str | None:
    """Why a run with these briefs and inputs must not go to *dev*; None when nothing names a denied folder."""
    if not dev.deny:
        return None
    for text in texts:
        if found := in_text(text, dev.deny):
            return f"{dev.name}: the brief names a denied folder: {found}"
    if found := in_inputs(inputs, dev.deny):
        return f"{dev.name}: an input is under a denied folder: {found}"
    return None


MOUNTS = "/proc/mounts"  # the node's mount table, read on the node


def denied(path: str, deny: tuple[str, ...]) -> bool:
    """*path* is a denied folder or lies under one (an absolute entry, or a folder named like a pattern)."""
    roots, names = split(deny)
    return _hit(path, roots, names) is not None


def _unescape(field: str) -> str:
    return re.sub(r"\\([0-7]{3})", lambda found: chr(int(found.group(1), 8)), field)


def aliases(mounts: str, deny: tuple[str, ...]) -> list[str]:
    """Mount points under a denied folder whose source lies outside every denied folder: the same files seen a second
    time through a bind mount (a container app binding every share under its own ``@appdata`` folder). A btrfs mount
    names its source by ``subvol=``; its path is the mount point of a mount of the same device with a parent
    subvolume, plus the rest. A mount whose source cannot be placed, or is denied too, is looked through as usual."""
    table = []
    for line in mounts.splitlines():
        parts = line.split()
        if len(parts) >= 4:
            options = dict(option.partition("=")[::2] for option in parts[3].split(","))
            subvol = options.get("subvol")
            table.append((parts[0], _unescape(parts[1]).rstrip("/") or "/", subvol.rstrip("/") if subvol else None))
    found = []
    for device, point, subvol in table:
        if subvol is None or not denied(point, deny):
            continue
        for other, where, parent in table:
            inside = parent is not None and (subvol == parent or subvol.startswith(parent + "/"))
            if other != device or where == point or where.startswith(point + "/") or not inside:
                continue
            if not denied(where.rstrip("/") + subvol[len(parent):], deny):
                found.append(point)
                break
    return sorted(set(found))


def scan_args(dev: Device, skips: list[str]) -> list[str]:
    roots, names = split(dev.deny)
    return [str(len(roots)), *roots, str(len(names)), *names, str(len(skips)), *skips, *dev.volumes]


LISTED_MAX = 50000  # lines the node sends back; a longer list is not seen whole


def owned(lines: list[str], uid: str) -> tuple[list[str], int]:
    """Split ``<uid>\\t<path>`` lines into the paths owned by *uid* (the run's user) and the count of the others.

    Backup, sync and indexing services write under denied folders all the time as their own users; only files the
    run's user owns are the run's. A file of another user that the run edited in place keeps that owner and is not
    counted: the gap of this check."""
    mine, others, seen = [], 0, set()
    for line in lines:
        owner, tab, path = line.partition("\t")
        if not tab or path in seen:  # a file under a moved folder can be listed twice
            continue
        seen.add(path)
        if owner == uid:
            mine.append(path)
        else:
            others += 1
    return mine, others


def parse_scan(out: str, code: int, limit: int, skipped: int = 0) -> dict:
    """The node's scan answer as ``{"complete", "count", "files", "others", "why"?}``: ``count``/``files`` are the
    run user's changed files, ``others`` the changes owned by other users; an unreadable answer is incomplete."""
    head, _, listing = out.partition("files:\n")
    fields = dict(line.split("=", 1) for line in head.splitlines() if "=" in line)
    lines = [line for line in listing.splitlines() if line.strip()]
    mine, others = owned(lines, fields.get("uid", ""))
    total = int(fields["count"]) if fields.get("count", "").strip().isdigit() else len(lines)
    check = {"complete": code == 0 and fields.get("complete") == "1" and "uid" in fields, "files": mine[:200],
             "count": len(mine), "others": others, "uid": fields.get("uid"), "limit_s": limit,
             "aliases_skipped": skipped, "not_writable": int(fields.get("notwritable") or 0)}
    if total > len(lines):
        check["complete"], check["why"] = False, f"{total} changed files, only the first {len(lines)} were read"
    if not check["complete"]:
        check.setdefault("why", fields.get("why") or f"the look did not answer (exit {code})")
    return check


def needs_review(check: dict | None) -> bool:
    return bool(check) and (bool(check.get("count")) or not check.get("complete"))


_REVIEW_HEAD = re.compile(rf"needs review — the '{re.escape(NEXT_NOTES)}' section of report\.md is (missing|empty)\n")


def with_check(text: str, check: dict | None) -> str:
    """The end notice with the denied-folder check: the reason a review came from it, and the files it found."""
    if not check:
        return text
    if check.get("forced_review"):
        text = _REVIEW_HEAD.sub(f"needs review — {review_reason(check)}\n", text, count=1)
    lines = ["- Denied-folder check (a check, not a sandbox: the run could reach the whole volume): "]
    if check.get("count"):
        lines[0] += f"{check['count']} file(s) owned by the run's user changed under a denied folder during the run:"
        lines += [f"  - {path}" for path in check.get("files", [])[:50]]
        if check["count"] > min(50, len(check.get("files", []))):
            lines.append(f"  - … {check['count'] - min(50, len(check.get('files', [])))} more")
    else:
        lines[0] += "no file owned by the run's user changed."
    lines.append(f"- Changed by other users: {check.get('others') or 0}, not counted (backup, sync and indexing "
                 "services write there as their own users). A file of another user that the run edited in place keeps "
                 "that owner and is not counted either.")
    if check.get("aliases_skipped"):
        lines.append(f"- Aliases skipped: {check['aliases_skipped']} (bind mounts under a denied folder whose source is "
                     "outside every denied folder).")
    if check.get("not_writable"):
        lines.append(f"- Not writable by the run's user, not looked into: {check['not_writable']} folder(s). The run "
                     "could not have written there; a folder it may write inside one of them (DSM ACLs) is not seen "
                     "either.")
    if not check.get("complete"):
        lines.append(f"- Not every denied folder was seen: {check.get('why')}.")
    return text + "\n".join(lines) + "\n"


def review_reason(check: dict | None) -> str | None:
    """The reason the denied-folder check gave a run ``review`` (end notice, card comment, ``lina worker status``)."""
    if not check or not check.get("forced_review"):
        return None
    return "files changed under a denied folder" if check.get("count") else "the denied-folder check did not finish"
