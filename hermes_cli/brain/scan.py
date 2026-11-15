"""Reading a wiki directory into a brain index. Files are only opened for reading; every file under the directory is
either a page or listed in ``skipped`` with a reason, so nothing is lost silently."""

from __future__ import annotations

import hashlib
import os
import re
from collections import Counter
from dataclasses import dataclass, field
from datetime import date
from pathlib import Path

from hermes_cli.brain import links
from hermes_cli.brain.page import LEDGER_KEYS, PAGE_TYPES, alias_key, nfc, page_id, parse, slugify
from hermes_cli.brain.resolver import Mapping

METAFILES = frozenset({"index.md", "readme.md", "schema.md", "resolver.md"})
_ARTIFACT_DIRS = frozenset({"@eadir", "#recycle", "#snapshot", ".synologyworkingdirectory", ".git"})
# Synology Drive (<name>_<device>_Conflict, <name>_Conflict), Dropbox ("conflicted copy") and
# Nextcloud/Syncthing ("(conflict ...)", ".sync-conflict-") conflict copies.
_CONFLICT = re.compile(r"(?i)(?:_conflict(?![a-z])|conflicted copy|\(conflict[^)]*\)|\.sync-conflict-)")
_YYMMDD = re.compile(r"^(\d{2})(\d{2})(\d{2})(?!\d)")
_ISO_DATE = re.compile(r"^(\d{4})-(\d{2})-(\d{2})(?!\d)")


@dataclass
class PageRecord:
    slug: str
    path: str
    stem: str
    type: str
    type_from: str
    title: str
    dated: date | None
    frontmatter: dict
    compiled_truth: str
    content_sha256: str
    body: str
    names: list[tuple[str, str]]
    held_ledger_keys: bool
    frontmatter_error: bool

    def row(self) -> tuple:
        return (self.slug, page_id(self.slug), self.path, self.type, self.title, self.dated, self.frontmatter,
                self.compiled_truth, self.content_sha256)


@dataclass
class LinkCounts:
    found: int = 0
    resolved: int = 0
    unresolved: int = 0
    ambiguous: int = 0
    to_itself: int = 0
    by_particle: int = 0


@dataclass
class Index:
    root: str
    mapping: Mapping
    files_in: int = 0
    pages: dict[str, PageRecord] = field(default_factory=dict)
    aliases: set[tuple[str, str, str, str]] = field(default_factory=set)
    edges: set[tuple[str, str, str, str]] = field(default_factory=set)
    timeline: list[tuple] = field(default_factory=list)
    skipped: list[tuple[str, str]] = field(default_factory=list)
    links: LinkCounts = field(default_factory=LinkCounts)
    unmapped_folders: Counter = field(default_factory=Counter)
    redirects: dict[str, str] = field(default_factory=dict)
    merges_ignored: list[str] = field(default_factory=list)

    def orphans(self) -> list[str]:
        linked = {slug for edge in self.edges for slug in edge[:2]}
        return sorted(slug for slug in self.pages if slug not in linked)

    def drop(self, slug: str, reason: str) -> None:
        record = self.pages.pop(slug)
        self.skipped.append((record.path, reason))
        self.aliases = {row for row in self.aliases if row[1] != slug}
        self.edges = {edge for edge in self.edges if slug not in edge[:2]}
        self.timeline = [row for row in self.timeline if row[0] != slug]

    def summary(self) -> dict:
        types = Counter(record.type for record in self.pages.values())
        return {
            "root": self.root,
            "files_in": self.files_in,
            "pages_out": len(self.pages),
            "merged": len(self.redirects),
            "skipped": len(self.skipped),
            "skipped_by_reason": dict(sorted(Counter(reason for _, reason in self.skipped).items())),
            "pages_by_type": {name: types[name] for name in PAGE_TYPES if types[name]},
            "type_from": dict(sorted(Counter(record.type_from for record in self.pages.values()).items())),
            "unmapped_folders": dict(sorted(self.unmapped_folders.items())),
            "links": vars(self.links).copy(),
            "edges": len(self.edges),
            "orphans": len(self.orphans()),
            "aliases": len(self.aliases),
            "timeline_entries": len(self.timeline),
            "pages_with_ledger_keys_in_file": sum(record.held_ledger_keys for record in self.pages.values()),
            "frontmatter_unreadable": sum(record.frontmatter_error for record in self.pages.values()),
            "merges_ignored": sorted(self.merges_ignored),
        }


def _dir_reason(name: str) -> str | None:
    lowered = name.lower()
    if lowered == ".git":
        return "git folder"
    if lowered in _ARTIFACT_DIRS:
        return "sync artifact folder"
    if name.startswith("."):
        return "hidden folder"
    return None


def _file_reason(name: str) -> str | None:
    if name.startswith("."):
        return "hidden file"
    if not name.lower().endswith(".md"):
        return "not markdown"
    if name.lower() in METAFILES:
        return "metafile"
    if _CONFLICT.search(name):
        return "sync conflict copy"
    return None


def _walk(root: Path) -> tuple[list[tuple[str, Path]], list[tuple[str, str]]]:
    """(markdown candidates, skipped) as paths relative to *root*, NFC, in a fixed order."""
    found: list[tuple[str, Path]] = []
    skipped: list[tuple[str, str]] = []

    def rel(path: str) -> str:
        return nfc(Path(os.path.relpath(path, root)).as_posix())

    for current, dirnames, filenames in os.walk(root, followlinks=False):
        kept = []
        for name in sorted(dirnames):
            full = os.path.join(current, name)
            reason = "symlink" if os.path.islink(full) else _dir_reason(name)
            if reason is None:
                kept.append(name)
            elif reason == "symlink":
                skipped.append((rel(full), reason))
            else:
                for inner, _, inner_files in os.walk(full, followlinks=False):
                    skipped.extend((rel(os.path.join(inner, file)), reason) for file in sorted(inner_files))
        dirnames[:] = kept
        for name in sorted(filenames):
            full = os.path.join(current, name)
            reason = "symlink" if os.path.islink(full) else _file_reason(name)
            if reason:
                skipped.append((rel(full), reason))
            else:
                found.append((rel(full), Path(full)))
    return found, skipped


def date_from_name(stem: str) -> date | None:
    """``YYMMDD`` (read as 20YY) or ``YYYY-MM-DD`` at the start of a file name."""
    iso = _ISO_DATE.match(stem)
    match = iso or _YYMMDD.match(stem)
    if not match:
        return None
    year = int(match.group(1)) + (0 if iso else 2000)
    try:
        return date(year, int(match.group(2)), int(match.group(3)))
    except ValueError:
        return None


def _names(stem: str, slug: str, title: str, data: dict) -> list[tuple[str, str]]:
    names = [(stem, "slug"), (slug, "slug"), (title, "title")]
    if isinstance(data.get("name"), str):
        names.append((data["name"], "frontmatter"))
    given = data.get("aliases")
    for alias in ([given] if isinstance(given, str) else given or []):
        if isinstance(alias, (str, int, float)):
            names.append((str(alias), "frontmatter"))
    return names


def _record(rel: str, raw: bytes, mapping: Mapping, unmapped: Counter) -> PageRecord:
    text = raw.decode("utf-8")
    page = parse(text)
    data = page.frontmatter
    stem = nfc(rel.rsplit("/", 1)[-1][:-3])
    folder = rel.rsplit("/", 1)[0] if "/" in rel else ""
    if data.get("type") in PAGE_TYPES:
        page_type, type_from = data["type"], "frontmatter"
    else:
        page_type, key = mapping.resolve(folder)
        type_from = "folder" if key else "default"
        if key is None and folder:
            unmapped[folder] += 1
    title = nfc(data["title"]) if isinstance(data.get("title"), str) and data["title"].strip() else stem
    return PageRecord(
        slug="", path=rel, stem=stem, type=page_type, type_from=type_from, title=title, dated=date_from_name(stem),
        frontmatter={key: value for key, value in data.items() if key not in LEDGER_KEYS},
        compiled_truth=page.compiled, content_sha256=hashlib.sha256(raw).hexdigest(), body=text,
        names=[], held_ledger_keys=bool(LEDGER_KEYS & set(data)), frontmatter_error=page.frontmatter_error)


def _assign_slugs(records: list[PageRecord]) -> None:
    """The file name's slug; when two files share it, each gets its path's slug instead (and a digest of the path
    when even that is shared, as with ``A.md`` beside ``a.md``)."""
    by_stem = Counter(slugify(record.stem) for record in records)
    for record in records:
        base = slugify(record.stem)
        record.slug = base if by_stem[base] == 1 else "/".join(slugify(part) for part in record.path[:-3].split("/"))
    by_slug = Counter(record.slug for record in records)
    for record in records:
        if by_slug[record.slug] > 1:
            record.slug = f"{record.slug}-{hashlib.sha256(record.path.encode('utf-8')).hexdigest()[:8]}"


# When one name belongs to several pages, a file name beats a title, and a title beats a frontmatter alias.
_ORIGIN_RANK = {"slug": 0, "title": 1, "frontmatter": 2, "merge": 3}


class _Resolver:
    def __init__(self, records: list[PageRecord], aliases: set[tuple[str, str, str, str]]) -> None:
        self.paths = {alias_key(record.path[:-3]): record.slug for record in records}
        self.names: dict[str, dict[str, int]] = {}
        for key, slug, _, origin in aliases:
            ranks = self.names.setdefault(key, {})
            ranks[slug] = min(ranks.get(slug, 9), _ORIGIN_RANK[origin])

    def _name(self, name: str) -> tuple[str | None, str]:
        key = alias_key(name)
        if key in self.paths:
            return self.paths[key], "resolved"
        ranks = self.names.get(key, {})
        if not ranks:
            return None, "unresolved"
        best = min(ranks.values())
        hits = [slug for slug, rank in ranks.items() if rank == best]
        return (hits[0], "resolved") if len(hits) == 1 else (None, "ambiguous")

    def resolve(self, link: links.Link) -> tuple[str | None, str]:
        target = link.target[:-3] if link.target.lower().endswith(".md") else link.target
        if link.kind == "file":
            slug = self.paths.get(alias_key(target))
            return slug, "resolved" if slug else "unresolved"
        slug, outcome = self._name(target)
        last = target.rsplit("/", 1)[-1]
        if slug is None and outcome == "unresolved" and last != target:
            slug, outcome = self._name(last)
        if slug is None and outcome == "unresolved":
            for stripped in links.without_particle(last):
                found, _ = self._name(stripped)
                if found:
                    return found, "by_particle"
        return slug, outcome


def _link_pages(index: Index, records: list[PageRecord]) -> None:
    resolver = _Resolver(records, index.aliases)
    counts = index.links
    for record in records:
        for link in links.extract(record.body, record.path):
            counts.found += 1
            slug, outcome = resolver.resolve(link)
            if slug == record.slug:
                counts.to_itself += 1
                continue
            if slug is None:
                setattr(counts, outcome, getattr(counts, outcome) + 1)
                continue
            counts.resolved += 1
            counts.by_particle += outcome == "by_particle"
            index.edges.add((record.slug, slug, "mentions", record.path))


def apply_merges(index: Index, merges: list[tuple[str, str]]) -> None:
    """Fold each ``from`` page into its ``into`` page, in order: aliases carry over (origin ``merge``), edges and
    timeline entries re-point, and edges that become a page's link to itself go."""
    for from_slug, into_slug in merges:
        into = into_slug
        while into in index.redirects:
            into = index.redirects[into]
        if from_slug not in index.pages or into not in index.pages or from_slug == into:
            index.merges_ignored.append(from_slug)
            continue
        index.pages.pop(from_slug)
        index.redirects[from_slug] = into
        held = {row[0] for row in index.aliases if row[1] == into}
        index.aliases = {row for row in index.aliases if row[1] != from_slug} | {
            (key, into, alias, "merge") for key, slug, alias, _ in index.aliases
            if slug == from_slug and key not in held}

        def moved(slug: str) -> str:
            return into if slug == from_slug else slug
        index.edges = {(moved(a), moved(b), kind, ref) for a, b, kind, ref in index.edges if moved(a) != moved(b)}
        index.timeline = [(moved(row[0]), *row[1:]) for row in index.timeline]


def _deduped_names(record: PageRecord) -> list[tuple[str, str, str]]:
    seen: dict[str, tuple[str, str, str]] = {}
    for name, origin in record.names:
        key = alias_key(name)
        if key and key not in seen:
            seen[key] = (key, nfc(name).strip(), origin)
    return list(seen.values())


def scan(root: Path, mapping: Mapping, merges: tuple[tuple[str, str], ...] = ()) -> Index:
    root = Path(root)
    index = Index(root=nfc(str(root.resolve())), mapping=mapping)
    found, index.skipped = _walk(root)
    index.files_in = len(found) + len(index.skipped)
    records = []
    for rel, full in found:
        raw = full.read_bytes()
        try:
            raw.decode("utf-8")
        except UnicodeDecodeError:
            index.skipped.append((rel, "not UTF-8"))
            continue
        if b"\x00" in raw:
            index.skipped.append((rel, "contains NUL bytes"))
            continue
        records.append(_record(rel, raw, mapping, index.unmapped_folders))
    _assign_slugs(records)
    for record in records:
        record.names = _names(record.stem, record.slug, record.title, record.frontmatter)
        index.pages[record.slug] = record
        index.aliases.update((key, record.slug, alias, origin) for key, alias, origin in _deduped_names(record))
        for entry in parse(record.body).entries:
            index.timeline.append((record.slug, record.path, entry.line, entry.date, entry.grade, entry.source_ref,
                                   entry.text))
    _link_pages(index, records)
    apply_merges(index, list(merges))
    return index
