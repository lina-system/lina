"""A brain page as written on disk: frontmatter, compiled truth above a divider, an append-only timeline below.

Parsing is lossless: every byte of the file lands in exactly one part, so ``render(parse(text)) == text`` for any
page, hand-written or not, and the importer never has a reason to rewrite a file. The renderer writes new pages
and new timeline entries in one canonical shape, which parses back to the same parts.
"""

from __future__ import annotations

import hashlib
import json
import re
import unicodedata
from dataclasses import dataclass, field, replace
from datetime import date

PAGE_TYPES = ("person", "organization", "project", "program", "concept", "idea", "source", "meeting", "writing")
GRADES = ("owner", "agent", "external")
# The least trusted grade: an entry nobody graded is treated as external until it is confirmed (lina#439).
DEFAULT_GRADE = "external"
# The work ledger is the only authority for these; a page never stores them (lina#438).
LEDGER_KEYS = frozenset({"status", "deadline", "due", "due_date"})
TIMELINE_HEADING = "## Timeline"
_DIVIDER = "---"
_HEADING = re.compile(r"^#{1,6}[ \t]*(?:timeline|타임라인)[ \t]*$", re.IGNORECASE)
_BULLET = re.compile(r"^[-*][ \t]")
_ENTRY_DATE = re.compile(r"^[-*][ \t]+(?:\*\*)?(\d{4})[-./](\d{1,2})[-./](\d{1,2})(?:\*\*)?[ \t]*[:|\u2014\u2013-]?[ \t]*(.*)$")
_CANONICAL_REST = re.compile(r"^(owner|agent|external)[ \t]*\|[ \t]*([^|]*?)[ \t]*\|[ \t]?(.*)$")


class PageError(ValueError):
    pass


def lines_of(text: str) -> list[str]:
    """Lines with their ``\n`` kept; unlike ``str.splitlines`` only ``\n`` ends a line, so line numbers match
    an editor's."""
    return re.findall(r"[^\n]*\n|[^\n]+$", text)


@dataclass(frozen=True)
class Entry:
    raw: str
    line: int
    date: date | None
    grade: str
    source_ref: str | None
    text: str


@dataclass(frozen=True)
class Page:
    frontmatter_text: str
    compiled: str
    divider: str
    entries: tuple[Entry, ...] = ()
    frontmatter: dict = field(default_factory=dict)
    frontmatter_error: bool = False


def nfc(text: str) -> str:
    return unicodedata.normalize("NFC", text)


def slugify(name: str) -> str:
    """Lower-case, NFC, Hangul kept; runs of anything but word characters, ``.`` and ``-`` become one ``-``."""
    text = nfc(name).strip().lower()
    text = re.sub(r"[^\w.\-]+", "-", text)
    text = re.sub(r"-{2,}", "-", text).strip("-.")
    return text or "page-" + hashlib.sha256(nfc(name).encode("utf-8")).hexdigest()[:8]


def alias_key(name: str) -> str:
    """The form a link name and an alias are compared in: NFC, case-folded, whitespace collapsed."""
    return " ".join(nfc(name).casefold().split())


def page_id(slug: str) -> int:
    """A stable 63-bit id for vec.embeddings(kind 'page'): the same slug always gets the same id."""
    return int.from_bytes(hashlib.sha256(slug.encode("utf-8")).digest()[:8], "big") >> 1


def _load_frontmatter(block: str) -> tuple[dict, bool]:
    from ruamel.yaml.error import YAMLError

    import hermes_yaml
    try:
        loaded = hermes_yaml.safe_load(block)
    except YAMLError:
        return {}, True
    if loaded is None:
        return {}, False
    if not isinstance(loaded, dict):
        return {}, True
    return json.loads(json.dumps(loaded, ensure_ascii=False, default=str)), False


def _split_frontmatter(lines: list[str]) -> tuple[str, dict, bool, int]:
    if not lines or lines[0].rstrip("\r\n") != _DIVIDER:
        return "", {}, False, 0
    for end in range(1, len(lines)):
        if lines[end].rstrip("\r\n") == _DIVIDER:
            data, error = _load_frontmatter("".join(lines[1:end]))
            return "".join(lines[:end + 1]), data, error, end + 1
    return "", {}, False, 0


def _timeline_start(lines: list[str], start: int) -> tuple[int, int] | None:
    """(divider line, first line after the heading) of the first ``---`` whose next text line is the heading."""
    for index in range(start, len(lines)):
        if lines[index].strip() != _DIVIDER:
            continue
        after = index + 1
        while after < len(lines) and not lines[after].strip():
            after += 1
        if after < len(lines) and _HEADING.match(lines[after].strip()):
            return index, after + 1
    return None


def parse_entry(raw: str, line: int) -> Entry:
    parts = raw.split("\n")
    first = parts[0].rstrip("\r")
    body = " ".join(part.strip() for part in parts).strip()
    text = re.sub(r"^[-*][ \t]+", "", body)
    match = _ENTRY_DATE.match(first)
    if not match:
        return Entry(raw, line, None, DEFAULT_GRADE, None, text)
    try:
        when = date(int(match.group(1)), int(match.group(2)), int(match.group(3)))
    except ValueError:
        return Entry(raw, line, None, DEFAULT_GRADE, None, text)
    rest = " ".join([match.group(4).strip(), *(part.strip() for part in parts[1:])]).strip()
    canonical = _CANONICAL_REST.match(rest)
    if canonical:
        return Entry(raw, line, when, canonical.group(1), canonical.group(2) or None, canonical.group(3).strip())
    return Entry(raw, line, when, DEFAULT_GRADE, None, rest)


def _entries(lines: list[str], start: int) -> tuple[str, tuple[Entry, ...]]:
    """The text after the timeline heading up to its first bullet (kept with the divider), then one entry per bullet
    with the lines that follow it."""
    first = start
    while first < len(lines) and not _BULLET.match(lines[first]):
        first += 1
    lead = "".join(lines[start:first])
    entries: list[Entry] = []
    index = first
    while index < len(lines):
        end = index + 1
        while end < len(lines) and not _BULLET.match(lines[end]):
            end += 1
        entries.append(parse_entry("".join(lines[index:end]), index + 1))
        index = end
    return lead, tuple(entries)


def parse(text: str) -> Page:
    lines = lines_of(text)
    frontmatter_text, data, error, body_start = _split_frontmatter(lines)
    found = _timeline_start(lines, body_start)
    if found is None:
        return Page(frontmatter_text, "".join(lines[body_start:]), "", (), data, error)
    divider_at, entries_at = found
    lead, entries = _entries(lines, entries_at)
    divider = "".join(lines[divider_at:entries_at]) + lead
    return Page(frontmatter_text, "".join(lines[body_start:divider_at]), divider, entries, data, error)


def render(page: Page) -> str:
    return page.frontmatter_text + page.compiled + page.divider + "".join(entry.raw for entry in page.entries)


def _check_ledger_keys(data: dict) -> None:
    held = sorted(LEDGER_KEYS & set(data))
    if held:
        raise PageError(f"a page never stores {', '.join(held)}: the work ledger owns them")


def new_page(page_type: str, title: str, compiled: str, *, aliases: tuple[str, ...] = (),
             extra: dict | None = None) -> Page:
    """A page in the canonical shape: frontmatter (type, title, aliases, extra keys), then the compiled truth."""
    import hermes_yaml
    if page_type not in PAGE_TYPES:
        raise PageError(f"unknown page type {page_type!r}; one of {', '.join(PAGE_TYPES)}")
    data = {"type": page_type, "title": nfc(title)}
    if aliases:
        data["aliases"] = [nfc(alias) for alias in aliases]
    data.update(extra or {})
    _check_ledger_keys(data)
    frontmatter_text = f"{_DIVIDER}\n{hermes_yaml.safe_dump(data, sort_keys=False)}{_DIVIDER}\n"
    body = compiled.strip("\n")
    return Page(frontmatter_text, f"\n{body}\n" if body else "\n", "", (),
                json.loads(json.dumps(data, ensure_ascii=False, default=str)))


def entry_line(when: date, text: str, *, grade: str = DEFAULT_GRADE, source_ref: str | None = None) -> str:
    if grade not in GRADES:
        raise PageError(f"unknown grade {grade!r}; one of {', '.join(GRADES)}")
    if "\n" in text or "\r" in text or "|" in (source_ref or ""):
        raise PageError("a timeline entry is one line, and its source ref has no '|'")
    return f"- {when.isoformat()} | {grade} | {source_ref or ''} | {text.strip()}\n"


def append(page: Page, when: date, text: str, *, grade: str = DEFAULT_GRADE, source_ref: str | None = None) -> Page:
    """The page with one more timeline entry at the end; nothing already written changes."""
    raw = entry_line(when, text, grade=grade, source_ref=source_ref)
    divider = page.divider
    entries = list(page.entries)
    if not divider:
        body = page.frontmatter_text + page.compiled
        divider = ("\n" if body and not body.endswith("\n") else "") + f"\n{_DIVIDER}\n\n{TIMELINE_HEADING}\n\n"
    elif entries and not entries[-1].raw.endswith("\n"):
        raw = "\n" + raw
    before = render(replace(page, divider=divider))
    line = before.count("\n") + 1 + raw.startswith("\n")
    entries.append(Entry(raw, line, when, grade, source_ref, text.strip()))
    return replace(page, divider=divider, entries=tuple(entries))
