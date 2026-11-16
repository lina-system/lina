"""Person pages (lina#445, design lina#439 section 1): everything durable about a person lives on their brain page.

A person of the identity registry (``plugins/memory/lina/identity.py``) has one page of type ``person``: the page
names the person in its frontmatter (``person_id: person:<hash>``), so the link lives in the file, survives every
reindex and needs no table of its own (the frontmatter copy in ``brain.pages`` holds it).

- Honcho's conclusions about the person are appended to the page's timeline as ``agent`` entries whose source is
  ``honcho:<conclusion id>``; an id already on the page is never written again, and compiled truth is never touched.
- The person card a session injects is built from the indexed page: its compiled truth, then the newest timeline
  entries that fit the card's size, each labelled with its grade and source. An ``external`` entry appears only
  inside the quoted data block (``plugins/memory/lina/grade.py``), never as card text.

The file stays the original: writes append to it (or add the frontmatter key) and then sync the page's directory
into the index as the store owner, like ``lina brain import``.
"""

from __future__ import annotations

import logging
import os
import re
from collections.abc import Iterable, Sequence
from dataclasses import dataclass
from datetime import date, datetime
from pathlib import Path
from typing import Any, Optional

from hermes_cli.brain import page as pages
from hermes_cli.brain.store import BrainStoreError

logger = logging.getLogger(__name__)

PERSON_KEY = "person_id"
HONCHO_SOURCE = "honcho"
# The card rides the frozen system prompt of every session: compiled truth plus the newest entries that fit.
CARD_CHARS = 2400
# Serializes writers of one page file (session ends of several agents, the one-time import).
_PAGE_LOCK_NS = 190445
_KEY_LINE = re.compile(rf"^{PERSON_KEY}[ \t]*:.*$", re.MULTILINE)


class PersonPageError(BrainStoreError):
    pass


@dataclass(frozen=True)
class TimelineLine:
    date: Optional[date]
    grade: str
    source_ref: Optional[str]
    text: str


@dataclass(frozen=True)
class PersonPage:
    slug: str
    title: str
    compiled_truth: str
    file: Path
    timeline: tuple[TimelineLine, ...]  # newest first


@dataclass(frozen=True)
class Conclusion:
    """One Honcho conclusion about a person: its id, text and creation time."""
    id: str
    content: str
    created_at: Optional[datetime] = None


def page_of(conn, person_id: str) -> Optional[PersonPage]:
    """The indexed person page that names *person_id*; None when no page does, or when several do (a guess must not
    become a fact: the owner fixes the frontmatter)."""
    rows = conn.execute(
        "SELECT p.slug, p.title, p.compiled_truth, r.path, p.path FROM brain.pages AS p "
        "JOIN brain.roots AS r USING (root_id) WHERE p.type = 'person' AND p.frontmatter->>%s = %s ORDER BY p.slug",
        (PERSON_KEY, person_id)).fetchall()
    if len(rows) > 1:
        logger.warning("person %s is named by %d brain pages (%s); no page is used until one is left",
                       person_id, len(rows), ", ".join(row[0] for row in rows))
    if len(rows) != 1:
        return None
    slug, title, compiled, root, path = rows[0]
    entries = conn.execute(
        "SELECT entry_date, grade, source_ref, text FROM brain.timeline WHERE page_slug = %s "
        "ORDER BY entry_date DESC NULLS LAST, line DESC", (slug,)).fetchall()
    return PersonPage(slug, title, compiled, Path(root) / path, tuple(TimelineLine(*row) for row in entries))


# ── the card ──

def _graded_line(line: TimelineLine) -> str:
    when = line.date.isoformat() if line.date else "undated"
    return f"- {when} (grade {line.grade}, source {line.source_ref or 'unknown'}) {line.text}"


def _render(page: PersonPage, compiled: str, chosen: Sequence[TimelineLine]) -> str:
    from plugins.memory.lina.grade import Quoted, data_block
    ordered = list(reversed(chosen))  # oldest first, as on the page
    own_lines = [_graded_line(line) for line in ordered if line.grade in ("owner", "agent")]
    quoted = [Quoted(line.source_ref or page.slug, line.date.isoformat() if line.date else None, "external",
                     line.text) for line in ordered if line.grade not in ("owner", "agent")]
    parts = [f"## Person card: {page.title}", compiled]
    if own_lines:
        parts.append("Timeline (oldest first):\n" + "\n".join(own_lines))
    if quoted:
        parts.append(data_block(quoted))
    return "\n\n".join(part for part in parts if part)


def card(page: PersonPage, limit: int = CARD_CHARS) -> str:
    """The person card of *page*: compiled truth (cut to *limit* when it alone is longer), then the newest timeline
    entries for as long as the card stays within *limit* characters."""
    compiled = page.compiled_truth.strip()
    if len(compiled) > limit:
        compiled = compiled[:limit].rstrip() + " [...]"
    chosen: list[TimelineLine] = []
    text = _render(page, compiled, chosen)
    for line in page.timeline:
        candidate = _render(page, compiled, [*chosen, line])
        if len(candidate) > limit:
            break
        chosen.append(line)
        text = candidate
    return text


def card_for(conn, person_id: str, limit: int = CARD_CHARS) -> Optional[str]:
    """The card of *person_id*'s page, or None when the person has no page."""
    found = page_of(conn, person_id)
    return card(found, limit) if found is not None else None


# ── writes, as the store owner ──

def _resync(conn, file: Path) -> None:
    """Sync the imported directory that holds *file* into the index, the way ``lina brain import`` does."""
    from hermes_cli.brain.scan import scan
    from hermes_cli.brain.store import merges_of, root_of, sync
    root_path = conn.execute("SELECT r.path FROM brain.roots AS r JOIN brain.pages AS p USING (root_id) "
                             "WHERE r.path || '/' || p.path = %s", (pages.nfc(str(file)),)).fetchone()
    if root_path is None:
        raise PersonPageError(f"{file} is not an indexed brain page")
    root_id, mapping = root_of(conn, root_path[0])
    sync(conn, scan(Path(root_path[0]), mapping, merges_of(conn, root_id)))


def _lock(conn, file: Path) -> None:
    conn.execute("SELECT pg_advisory_xact_lock(%s, hashtext(%s))", (_PAGE_LOCK_NS, pages.nfc(str(file))))


def _person_page_file(conn, slug: str) -> Path:
    row = conn.execute("SELECT r.path, p.path, p.type FROM brain.pages AS p JOIN brain.roots AS r USING (root_id) "
                       "WHERE p.slug = %s", (pages.nfc(slug),)).fetchone()
    if row is None:
        raise PersonPageError(f"no brain page {slug}")
    if row[2] != "person":
        raise PersonPageError(f"brain page {slug} is a {row[2]} page, not a person page")
    return Path(row[0]) / row[1]


def _with_key(text: str, person_id: str) -> str:
    """*text* with ``person_id: <id>`` in its frontmatter, every other byte kept."""
    parsed = pages.parse(text)
    line = f"{PERSON_KEY}: {person_id}"
    if not parsed.frontmatter_text:
        return f"---\n{line}\n---\n{text}"
    block = parsed.frontmatter_text
    if _KEY_LINE.search(block):
        block = _KEY_LINE.sub(line, block, count=1)
    else:
        closing = block.rstrip("\r\n").rfind("\n")
        block = f"{block[:closing + 1]}{line}\n{block[closing + 1:]}"
    return block + text[len(parsed.frontmatter_text):]


def link(conn, slug: str, person_id: str) -> Path:
    """Name *person_id* in the frontmatter of person page *slug* and sync the index; returns the page's file."""
    if not person_id.startswith("person:"):
        raise PersonPageError(f"not a person id: {person_id}")
    with conn.transaction():
        file = _person_page_file(conn, slug)
        _lock(conn, file)
        others = conn.execute("SELECT slug FROM brain.pages WHERE frontmatter->>%s = %s AND slug <> %s",
                              (PERSON_KEY, person_id, pages.nfc(slug))).fetchall()
        if others:
            raise PersonPageError(f"{person_id} already has page {others[0][0]}; a person has one page")
        text = file.read_text(encoding="utf-8-sig")
        changed = _with_key(text, person_id)
        if changed != text:
            temporary = file.with_name(f".{file.name}.lina-tmp")
            temporary.write_text(changed, encoding="utf-8")
            os.replace(temporary, file)
        _resync(conn, file)
    return file


def _entry_text(content: str) -> str:
    return " ".join(content.split())


def record_conclusions(conn, person_id: str, conclusions: Iterable[Conclusion], *,
                       today: Optional[date] = None) -> Optional[int]:
    """Append each conclusion not yet on *person_id*'s page to its timeline (grade ``agent``, source
    ``honcho:<id>``), oldest first, then sync the index. Returns how many were added, None when the person has no
    page. The file only grows: what is written stays byte for byte."""
    found = page_of(conn, person_id)
    if found is None:
        return None
    with conn.transaction():
        _lock(conn, found.file)
        before = found.file.read_text(encoding="utf-8-sig")
        parsed = pages.parse(before)
        known = {entry.source_ref for entry in parsed.entries if entry.source_ref}
        fresh = {}
        for item in conclusions:
            ref = f"{HONCHO_SOURCE}:{item.id}"
            text = _entry_text(item.content)
            if ref not in known and ref not in fresh and text:
                fresh[ref] = item
        if not fresh:
            return 0
        oldest_first = sorted(fresh.items(), key=lambda pair: (
            pair[1].created_at is None, pair[1].created_at.timestamp() if pair[1].created_at else 0.0, pair[1].id))
        for ref, item in oldest_first:
            when = item.created_at.date() if item.created_at else (today or date.today())
            parsed = pages.append(parsed, when, _entry_text(item.content), grade="agent", source_ref=ref)
        after = pages.render(parsed)
        if not after.startswith(before):
            raise PersonPageError(f"appending to {found.file} would change what is written")
        with found.file.open("a", encoding="utf-8") as handle:
            handle.write(after[len(before):])
        _resync(conn, found.file)
    return len(fresh)


def conclusion_from(raw: Any) -> Conclusion:
    """A Conclusion from what the Honcho adapter lists (``{"id", "content", "created_at"}``)."""
    created = raw.get("created_at")
    if isinstance(created, str):
        created = datetime.fromisoformat(created)
    return Conclusion(str(raw["id"]), str(raw.get("content") or ""), created)


# ── the one-time import ──

@dataclass
class ImportReport:
    pages: int = 0
    pages_without_peer: int = 0
    peers: int = 0
    peers_in_other_workspaces: int = 0
    conclusions_seen: int = 0
    added: int = 0

    def as_json(self) -> dict:
        return dict(self.__dict__)


def import_honcho(conn, registry, workspace: str, list_conclusions) -> ImportReport:
    """Copy every Honcho conclusion about each linked person into their page, once. *list_conclusions(peer_id)*
    lists one peer's conclusions in *workspace*; Honcho itself is only read. Running it again adds nothing."""
    report = ImportReport()
    people = conn.execute("SELECT DISTINCT frontmatter->>%s FROM brain.pages WHERE type = 'person' "
                          "AND frontmatter ? %s ORDER BY 1", (PERSON_KEY, PERSON_KEY)).fetchall()
    for (person_id,) in people:
        report.pages += 1
        peers = [key for source, key in registry.handles(person_id) if source == "honcho_peer"]
        mine = [key.split(":", 1)[1] for key in peers if key.split(":", 1)[0] == workspace]
        report.peers_in_other_workspaces += len(peers) - len(mine)
        if not mine:
            report.pages_without_peer += 1
            continue
        found: dict[str, Conclusion] = {}
        for peer in mine:
            report.peers += 1
            for raw in list_conclusions(peer):
                item = conclusion_from(raw)
                found.setdefault(item.id, item)
        report.conclusions_seen += len(found)
        report.added += record_conclusions(conn, person_id, found.values()) or 0
    return report
