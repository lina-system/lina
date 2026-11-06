"""Reading the UI QA record a card carries, for ``kanban gate ui-qa`` (lina-system/lina#162).

A card that changes a screen is done only after QA on a disposable instance, run against the
screen feature map. The QA tooling lives outside this repository; it records its outcome as card
comments, and this module only reads them. Standard library only: ``kanban_gates.py`` loads it as
a plain-file sibling too.

A result is one comment whose first line is the marker::

    ui-qa-result v1 head=<sha> instance=disposable

followed by the aside-qa answer as it is: the four sections ``## (A) Defects``, ``## (B) Verified``,
``## (C) Suggestions`` and ``## (D) Map drift`` (the letter prefix is optional; ``Confirmed`` and
``Map differences`` are accepted names too). Every list item under Defects is a defect (``- None.``
is not), and so is every ``- FAIL: ...`` line under Verified: a Done when item the tool found
broken. A QA run in progress is a comment whose first line is
``ui-qa-run v1 head=<sha> state=running``.
"""
from __future__ import annotations

import re
from dataclasses import dataclass, field
from typing import Iterable, Optional

RESULT, RUN = "ui-qa-result", "ui-qa-run"
SECTIONS = ("Defects", "Verified", "Suggestions", "Map drift")
_SECTION_NAMES = {"defects": "Defects", "verified": "Verified", "confirmed": "Verified",
                  "suggestions": "Suggestions", "map drift": "Map drift", "map differences": "Map drift"}
_LETTER = re.compile(r"^\([A-Da-d]\)\s*")
_FAILED_CHECK = re.compile(r"^[*_`~\s]*fail(?:ed)?\b", re.IGNORECASE)
_FENCE = re.compile(r"^\s*(`{3,}|~{3,})(.*)$")
_PASSED_CHECK = re.compile(r"^PASS\s*:")
_MARKER = re.compile(r"^(ui-qa-result|ui-qa-run)\s+v1((?:\s+[a-z_]+=\S+)*)\s*$")
_SHA = re.compile(r"^[0-9a-f]{7,40}$")
_HEADING = re.compile(r"^(#{1,6})\s+(.*?)\s*#*\s*$")
_ITEM = re.compile(r"^\s*(?:[-*+]|[0-9]+[.)])\s+(.*\S)\s*$")
_NONE = re.compile(r"^(none|nothing|n/a)\.?$", re.IGNORECASE)


@dataclass
class Marker:
    kind: str
    comment_id: int
    head: str
    fields: dict
    body: str


@dataclass
class Verdict:
    code: str  # "pass", "fail" or "wait"
    text: str
    defects: list[str] = field(default_factory=list)


def parse_marker(comment_id: int, body: Optional[str]) -> Optional[Marker]:
    lines = (body or "").strip().splitlines()
    match = _MARKER.match(lines[0].strip()) if lines else None
    if not match:
        return None
    fields = dict(token.split("=", 1) for token in match[2].split())
    head = fields.get("head", "").lower()
    if not _SHA.match(head):
        return None
    return Marker(match[1], comment_id, head, fields, "\n".join(lines[1:]))


def sections(body: str) -> dict[str, list[str]]:
    """Each known section's lines. Only a ``#`` or ``##`` heading ends a section: a ``###`` or
    deeper heading groups lines inside it and stands as a blank line. A fenced code block is kept
    as its opening line only, so a ``#`` or ``- `` line inside it is neither a heading nor an item."""
    found: dict[str, list[str]] = {}
    current: Optional[list[str]] = None
    fence = ""
    for raw in body.splitlines():
        opener = _FENCE.match(raw)
        if fence:
            if opener and opener[1][0] == fence[0] and len(opener[1]) >= len(fence) and not opener[2].strip():
                fence = ""
            continue
        heading = _HEADING.match(raw)
        if opener:
            fence = opener[1]
        elif heading and len(heading[1]) <= 2:
            name = _SECTION_NAMES.get(_LETTER.sub("", heading[2].strip()).lower())
            current = found.setdefault(name, []) if name and len(heading[1]) == 2 else None
            continue
        if current is not None:
            indent = raw[:len(raw) - len(raw.lstrip())]
            current.append(indent + "(code block)" if opener else "" if heading else raw)
    return found


def _items(lines: Iterable[str]) -> list[str]:
    return [m[1] for m in (_ITEM.match(line) for line in lines) if m and not _NONE.match(m[1])]


def _defect_entries(lines: list[str]) -> list[str]:
    """List items, and any text that starts a paragraph of its own (not an item's continuation):
    a defect written as prose, a quote or a table still counts. ``None.`` does not."""
    found, paragraph_start = [], True
    for line in lines:
        if not line.strip():
            paragraph_start = True
            continue
        item = _ITEM.match(line)
        text = item[1] if item else line.strip()
        if (item or paragraph_start and not line[:1].isspace()) and not _NONE.match(text):
            found.append(text)
        paragraph_start = False
    return found


def defects(found: dict[str, list[str]]) -> list[str]:
    texts = [m[1] if (m := _ITEM.match(line)) else line.strip() for line in found.get("Verified", [])]
    failed = [text for text in texts if _FAILED_CHECK.match(text)]
    return _defect_entries(found.get("Defects", [])) + failed


def _judge(result: Marker) -> Verdict:
    where = f"QA result in comment {result.comment_id}"
    instance = result.fields.get("instance")
    if instance != "disposable":
        return Verdict("fail", f"{where} was taken on instance={instance or '-'}, not a disposable instance")
    found = sections(result.body)
    missing = [name for name in SECTIONS if name not in found]
    bad = defects(found)
    reasons = [f"lacks section(s): {', '.join(missing)}"] if missing else []
    reasons += [f"lists {len(bad)} defect(s)"] if bad else []
    if reasons:
        return Verdict("fail", f"{where} {'; '.join(reasons)}", bad)
    verified = sum(1 for item in _items(found["Verified"]) if _PASSED_CHECK.match(item))
    return Verdict("pass", f"{where} (disposable): 0 defects, {verified} verified")


def _same_head(marker: Marker, head: str) -> bool:
    return head.lower().startswith(marker.head)


def ui_qa_verdict(comments: Iterable[tuple[int, Optional[str]]], head: str) -> Verdict:
    """The verdict for ``head`` from the card's ``(comment id, body)`` rows.

    The newest marker for this head decides: a running QA run waits, a result is judged. A result
    for another (older) head fails: nothing is running that would bring a result for this head,
    so the QA must be run again; a wait would park the card on an event that never comes."""
    markers = [m for m in (parse_marker(cid, body) for cid, body in comments) if m]
    current = [m for m in markers if _same_head(m, head)
               and (m.kind == RESULT or m.fields.get("state") == "running")]
    if current:
        newest = max(current, key=lambda m: m.comment_id)
        if newest.kind == RUN:
            return Verdict("wait", f"QA run for this head in progress (comment {newest.comment_id})")
        return _judge(newest)
    older = [m for m in markers if m.kind == RESULT]
    if older:
        newest = max(older, key=lambda m: m.comment_id)
        return Verdict("fail", f"newest QA result (comment {newest.comment_id}) is for head {newest.head[:10]}; "
                               "run QA again on the current head")
    return Verdict("fail", "no QA result for this head")
