"""Links between pages, found by pattern matching (no model call): ``[[wiki links]]`` and markdown links to
``.md`` files. Code spans and fenced blocks are not read."""

from __future__ import annotations

import posixpath
import re
from dataclasses import dataclass
from urllib.parse import unquote

_FENCE = re.compile(r"(?ms)^[ \t]*(```|~~~).*?^[ \t]*\1[^\n]*$")
_CODE = re.compile(r"`[^`\n]*`")
_WIKI = re.compile(r"\[\[([^\[\]\n]+?)\]\]")
_MARKDOWN = re.compile(r"(?<!!)\[[^\]\n]*\]\(<?([^)\s>]+)>?(?:[ \t]+\"[^\"\n]*\")?\)")
_SCHEME = re.compile(r"^[A-Za-z][A-Za-z0-9+.\-]*:")
# Korean particles a writer may have put inside the brackets ([[name의]]); tried only when the whole name names no
# page. Longest first, so a name ending in 에서 loses 에서, not only its last syllable.
PARTICLES = tuple(sorted(
    ("은", "는", "이", "가", "을", "를", "의", "에", "에서", "에게", "께", "께서", "와", "과", "로", "으로", "도", "만",
     "랑", "이랑", "하고", "부터", "까지", "처럼", "보다", "한테", "이나", "이며", "이고"), key=len, reverse=True))


@dataclass(frozen=True)
class Link:
    target: str
    kind: str  # "wiki": a page name or path; "file": a path relative to the imported directory


def _readable(text: str) -> str:
    return _CODE.sub(" ", _FENCE.sub(" ", text))


def _wiki_target(inner: str) -> str:
    return inner.split("|", 1)[0].split("#", 1)[0].strip()


def _file_target(url: str, page_path: str) -> str | None:
    if _SCHEME.match(url) or url.startswith("#"):
        return None
    path = unquote(url.split("#", 1)[0])
    if not path.lower().endswith(".md"):
        return None
    if path.startswith("/"):
        joined = posixpath.normpath(path.lstrip("/"))
    else:
        joined = posixpath.normpath(posixpath.join(posixpath.dirname(page_path), path))
    return None if joined.startswith("../") or joined == ".." else joined


def extract(text: str, page_path: str) -> list[Link]:
    """Every link in *text* in order of appearance; *page_path* (relative to the imported directory) anchors
    relative markdown links."""
    readable = _readable(text)
    found: list[tuple[int, Link]] = []
    for match in _WIKI.finditer(readable):
        target = _wiki_target(match.group(1))
        if target:
            found.append((match.start(), Link(target, "wiki")))
    for match in _MARKDOWN.finditer(readable):
        target = _file_target(match.group(1), page_path)
        if target:
            found.append((match.start(), Link(target, "file")))
    return [link for _, link in sorted(found, key=lambda item: item[0])]


def without_particle(name: str) -> list[str]:
    """*name* with each particle it ends in removed, longest particle first; never empty names."""
    return [name[:-len(particle)].rstrip() for particle in PARTICLES
            if name.endswith(particle) and len(name) > len(particle)]
