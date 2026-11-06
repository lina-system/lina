"""A file node's denied folders (``devices.workers.<name>.deny``): refused at submit, looked for after a run.

An entry starting with ``/`` is a folder by its absolute path; any other entry is a folder-name pattern (``#recycle``,
``@*``). A brief or an input that names a path under one is refused before the run is handed out. After a run the
node lists files changed under them since the run started (``file_node_bash.DENY_SCAN``); one such file, or a look
that did not finish in time, ends the run as ``review``. This is a check, not a sandbox: the run can reach the whole
volume, and a backup or indexing service writing into those folders shows up too.
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


def scan_args(dev: Device) -> list[str]:
    roots, names = split(dev.deny)
    return [str(len(roots)), *roots, str(len(names)), *names, *dev.volumes]


def parse_scan(out: str, code: int, limit: int) -> dict:
    """The node's scan answer as ``{"complete", "count", "files", "why"?}``; an unreadable answer is incomplete."""
    head, _, listing = out.partition("files:\n")
    fields = dict(line.split("=", 1) for line in head.splitlines() if "=" in line)
    files = [line for line in listing.splitlines() if line.strip()]
    check = {"complete": code == 0 and fields.get("complete") == "1", "files": files,
             "count": int(fields["count"]) if fields.get("count", "").strip().isdigit() else len(files),
             "limit_s": limit}
    if not check["complete"]:
        check["why"] = fields.get("why") or f"the look did not answer (exit {code})"
    return check


def needs_review(check: dict | None) -> bool:
    return bool(check) and (bool(check.get("count")) or not check.get("complete"))


_REVIEW_HEAD = re.compile(rf"needs review — the '{re.escape(NEXT_NOTES)}' section of report\.md is (missing|empty)\n")


def with_check(text: str, check: dict | None) -> str:
    """The end notice with the denied-folder check: the reason a review came from it, and the files it found."""
    if not check:
        return text
    if check.get("forced_review"):
        reason = "files changed under a denied folder" if check.get("count") else "the denied-folder check did not finish"
        text = _REVIEW_HEAD.sub(f"needs review — {reason}\n", text, count=1)
    lines = ["- Denied-folder check (a check, not a sandbox: the run could reach the whole volume): "]
    if check.get("count"):
        lines[0] += f"{check['count']} file(s) changed under a denied folder during the run:"
        lines += [f"  - {path}" for path in check.get("files", [])[:50]]
        if check["count"] > min(50, len(check.get("files", []))):
            lines.append(f"  - … {check['count'] - min(50, len(check.get('files', [])))} more")
        lines.append("  A backup or indexing service writing there shows up here too: check before you accept.")
    else:
        lines[0] += "no changed file found."
    if not check.get("complete"):
        lines.append(f"- Not every denied folder was seen: {check.get('why')}.")
    return text + "\n".join(lines) + "\n"
