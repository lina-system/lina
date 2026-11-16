"""Links between pages, found by pattern matching (no model call): ``[[wiki links]]`` and markdown links to
``.md`` files. Code spans and fenced blocks are not read."""

from __future__ import annotations

import posixpath
import re
from dataclasses import dataclass
from urllib.parse import unquote

from hermes_cli.brain.page import alias_key

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
ENDINGS = ("입니다", "이에요", "예요", "이세요", "세요", "이군요", "군요", "이네요", "네요",
           "이죠", "죠", "이었습니다", "였습니다", "이었어요", "였어요", "이라고", "라고")
_SUFFIX = re.compile(
    "(?:(?:" + "|".join(map(re.escape, PARTICLES)) + ")(?:은|는|도|만)?"
    "|(?:" + "|".join(map(re.escape, ENDINGS)) + "))?")


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
    suffixes = sorted({*PARTICLES, *ENDINGS,
                       *(particle + tail for particle in PARTICLES for tail in ("은", "는", "도", "만"))},
                      key=lambda value: (-len(value), value))
    return [name[:-len(suffix)].rstrip() for suffix in suffixes
            if name.endswith(suffix) and len(name) > len(suffix)]


def mentions(text: str, names: set[str]) -> list[Link]:
    """Plain names, longest first at each word boundary, outside code and written links.

    Suffixes must consume the rest of the word: a known short Hangul name inside a longer word never matches.
    Frontmatter is removed by the caller so aliases declaring a page are not evidence of a mention.
    """
    readable = alias_key(_MARKDOWN.sub(" ", _WIKI.sub(" ", _readable(text))))
    keys = sorted({alias_key(name) for name in names if alias_key(name)}, key=lambda key: (-len(key), key))
    if not keys:
        return []
    pattern = re.compile(r"(?<!\w)(" + "|".join(map(re.escape, keys)) + ")" + _SUFFIX.pattern + r"(?!\w)")
    return [Link(match.group(1), "wiki") for match in pattern.finditer(readable)]
