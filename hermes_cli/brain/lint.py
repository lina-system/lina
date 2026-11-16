"""Report-only checks over the importer's pages; filenames never substitute for a written title."""

from __future__ import annotations

import re
from collections import defaultdict
from dataclasses import dataclass
from datetime import date

from hermes_cli.brain import links
from hermes_cli.brain.page import PAGE_TYPES, parse, slugify
from hermes_cli.brain.scan import Index, _Resolver

_TITLE = re.compile(r"(?m)^#[ \t]+\S")
_DATE_HEADING = re.compile(r"^#{1,6}[ \t]+(\d{4})[-./](\d{1,2})[-./](\d{1,2})(?!\d)")


@dataclass(frozen=True, order=True)
class Finding:
    path: str
    rule: str
    detail: str
    line: int = 0


def lint(index: Index) -> list[Finding]:
    """Find missing titles, conflicting names, broken wiki links, date order and unknown types."""
    findings: set[Finding] = set()
    stems: dict[str, set[str]] = defaultdict(set)
    aliases: dict[str, set[str]] = defaultdict(set)
    resolver = _Resolver(list(index.pages.values()), index.aliases)
    for record in index.pages.values():
        stems[slugify(record.stem)].add(record.path)
        page = parse(record.body)
        title = page.frontmatter.get("title")
        if not (isinstance(title, str) and title.strip()) and not _TITLE.search(links._readable(page.compiled)):
            findings.add(Finding(record.path, "missing-title", "no frontmatter title or level-one heading"))
        page_type = page.frontmatter.get("type")
        if page_type is not None and page_type not in PAGE_TYPES:
            findings.add(Finding(record.path, "unknown-type", str(page_type)))
        elif record.type_from == "default" and "/" in record.path:
            findings.add(Finding(record.path, "unknown-type", "folder has no mapping"))
        body = record.body[len(page.frontmatter_text):]
        for link in links.extract(body, record.path):
            if link.kind == "wiki":
                target, outcome = resolver.resolve(link)
                if target is None:
                    findings.add(Finding(record.path, "broken-link", f"{link.target} ({outcome})"))
        if page.divider:
            # The parser retains headings before the first bullet in the divider itself.
            timeline = links._readable(record.body[len(page.frontmatter_text) + len(page.compiled):])
            previous: date | None = None
            for line in timeline.splitlines():
                match = _DATE_HEADING.match(line)
                if match:
                    try:
                        current = date(*(int(part) for part in match.groups()))
                    except ValueError:
                        continue
                    if previous is not None and current < previous:
                        findings.add(Finding(record.path, "timeline-order",
                                             f"{current.isoformat()} follows {previous.isoformat()}"))
                    previous = current
    for key, slug, _, _ in index.aliases:
        aliases[key].add(index.pages[slug].path)
    for rule, groups in (("duplicate-slug", stems), ("duplicate-alias", aliases)):
        for key, paths in groups.items():
            if len(paths) > 1:
                for path in paths:
                    findings.add(Finding(path, rule, key))
    return sorted(findings)
