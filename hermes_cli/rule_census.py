"""Read-only rule discovery for an explicitly marked copied Hermes home.

Create an empty ``.standing-rule-copy`` at the copy root. That root represents
``default``; named profiles live in ``profiles/<name>``. Copy install-wide
rules into the root and point its ``config.yaml`` ``undo.rules`` globs there.
Copy-relative paths and absolute paths inside the copy are supported. The
runtime does not inject these files itself; consult the install's own setup
for how its agents received them. Shared patterns skip ``undo`` and
``checkpoints`` directories and never promote persona overlays or self notes.
Only single-link regular files and directories reached without symlinks qualify.

Each profile can supply SOUL.md, memories/{USER,MEMORY}.md (plus distinct
legacy USER.md or MEMORY.md at the profile root), USER_NOTES.md,
user-notes/**/*.md, skills/**/SKILL.md, and cron/jobs.json (``jobs[].prompt``).
For install-specific filters, copy room-lists, closed-items, and
script-constants into .txt (one exact ID per line) or .json (arrays of IDs,
optionally under ``rooms``, ``items``, or ``sources``, respectively). Treat
these files as the copied filter lists themselves. For Python constants,
put the copied scripts under ``scripts/rule-filters/``. The census reads
top-level list literals named ROOM_EXCLUSIONS, CLOSED_ITEMS, or MUTED_SOURCES
without importing or running the scripts.
Place exported Honcho conclusions under honcho/conclusions.json or .jsonl
as strings or objects with a ``content`` field (JSON also accepts a
``conclusions`` envelope). SOUL.self.md contributes a report-only count.

Run ``python -m hermes_cli.rule_census COPY [--agent NAME]`` for JSON counts.
Call ``owner_list`` only when rendering to the owner in conversation.
"""

from __future__ import annotations

import argparse
import ast
import hashlib
import json
import re
import stat
import tokenize
from bisect import bisect_left
from collections import Counter
from collections.abc import Iterator
from dataclasses import dataclass, field, replace
from fnmatch import fnmatchcase
from io import StringIO
from pathlib import Path
from typing import Final, Literal

from ruamel.yaml import YAML, YAMLError
from ruamel.yaml.nodes import MappingNode, Node, ScalarNode, SequenceNode

Kind = Literal["conduct", "filter"]
Scope = Literal["all", "agent", "place"]
Span = tuple[int, int]
Matcher = tuple[str, str | bool]
MAX_OWNER_ITEMS: Final = 12
COPY_MARKER: Final = ".standing-rule-copy"
_SOURCES: Final = (
    ("soul", "SOUL.md"), ("user", "memories/USER.md"), ("user", "USER.md"),
    ("memory", "memories/MEMORY.md"), ("memory", "MEMORY.md"),
    ("user_notes", "USER_NOTES.md"), ("user_notes", "user-notes/**/*.md"),
    ("skill", "skills/**/SKILL.md"), ("job", "cron/jobs.json"),
    ("room_list", "room-lists.json"), ("room_list", "room-lists.txt"),
    ("closed_list", "closed-items.json"), ("closed_list", "closed-items.txt"),
    ("script_constant", "script-constants.json"), ("script_constant", "script-constants.txt"),
    ("script_constant", "scripts/rule-filters/**/*.py"),
    ("honcho", "honcho/conclusions.json"), ("honcho", "honcho/conclusions.jsonl"),
)
_LISTS: Final = {"room_list": ("rooms", "room"), "closed_list": ("items", "item"),
                 "script_constant": ("sources", "source")}
_SCRIPT_MATCHERS: Final = {"ROOM_EXCLUSIONS": "room", "CLOSED_ITEMS": "item", "MUTED_SOURCES": "source"}
_METADATA_DIRS: Final = frozenset({"undo", "checkpoints"})
_REPORT_ONLY_FILES: Final = frozenset({"soul.self.md", "soul.overlay.md", "config.yaml"})
_ID: Final = r"(?a:[A-Za-z0-9#@][A-Za-z0-9_:.#@-]{0,127})"
_ALL_PREFIX: Final = re.compile(
    r"^(?:everyone|all of you|every agent|모두(?:는)?|모든 에이전트(?:는)?)[,:]?\s+", re.IGNORECASE,
)
_AGENT_PREFIX: Final = re.compile(r"^(?:you|너는|넌|당신은)[,:]?\s+", re.IGNORECASE)
_NAMED_AGENT: Final = re.compile(r"^agent[: ]+(" + _ID + r")[,:]\s+", re.IGNORECASE)
_ROOM_PREFIX: Final = re.compile(
    r"^(?:in (?:the )?(" + _ID + r") room[,:]\s+|(" + _ID + r") 방에서는?[,]?\s+)", re.IGNORECASE,
)
_DEICTIC_ROOM: Final = re.compile(r"^(?:in (?:this|that) room\b|(?:이|그) 방에서는?\s)", re.IGNORECASE)
_EXACT_FILTERS: Final[tuple[tuple[str, Matcher], ...]] = (
    (r"(?:skip|exclude|hide) (archived|closed) (?:posts|messages|items)", ("kind", "")),
    (r"only (?:(?:show|surface|bring)(?: me)? )?(?:posts|messages|items) that mention me",
     ("mentions_owner", False)),
    (r"(?:나를|저를|날|절) (?:언급한|멘션한) (?:메시지|게시물|글|항목)만"
     r"(?: (?:보여줘|보여주세요|전해줘|전해주세요|가져와|가져와 줘|알려줘|알려주세요|보내줘|보내주세요))?",
     ("mentions_owner", False)),
    (r"(?:skip|exclude|hide) (?:posts|messages|items) (?:in|from) room (" + _ID + ")",
     ("room", "")),
    (r"(?:skip|exclude|hide) (?:posts|messages|items) from source (" + _ID + ")",
     ("source", "")),
)
_OWNER_LINE: Final = re.compile(
    r"(?i)^(?:(?:please\s+)?(?:always|never|do not|don['’]t|must not)\b"
    r"|(?:please\s+)?remember to\b|(?:항상|절대|앞으로는?)\s|.+(?:하지 마세요|하지 마|해주세요)[.!]?$)"
)
_UNSUPPORTED_FILTER: Final = re.compile(
    r"(?i)^(?:please\s+)?(?:always|never|do not|don['’]t|must not)\s+"
    r"(?:show|surface|bring|deliver|skip|exclude|hide)\b"
    r"|(?:언급|멘션).+(?:메시지|게시물|글|항목)만"
)
_BULLET: Final = re.compile(r"^(?:[-*+>]\s+|\d+[.)]\s+)")
_JSON_EOL: Final = re.compile(
    r"(\\(?:r|u000[dD])(?:\\(?:n|u000[aA]))?|\\(?:n|u000[aA]))|\\(?:u[0-9a-fA-F]{4}|.)"
)


class CensusError(ValueError):
    """A copied-home request or source cannot be read safely."""


@dataclass(frozen=True, slots=True)
class Candidate:
    path: Path
    place: str
    agent: str
    statement: str = field(repr=False)
    kind: Kind
    _digest: bytes = field(repr=False)
    _span: Span = field(repr=False)
    _matcher: Matcher | None = field(default=None, repr=False)
    _separators: tuple[int | None, int | None] | None = field(default=None, repr=False)
    scope: Scope = "agent"
    place_ref: str | None = field(default=None, repr=False)

    @property
    def filter_match(self) -> dict[str, str | bool] | None:
        """Return JSON-ready matching data for supported filters."""
        if self._matcher is None:
            return None
        matcher = dict((self._matcher,))
        if self.place_ref and "room" not in matcher:
            matcher["room"] = self.place_ref.removeprefix("channel:")
        return matcher


@dataclass(frozen=True, slots=True)
class _Source:
    path: Path
    place: str
    agent: str
    text: str
    digest: bytes

    def item(self, statement: str, span: Span, *,
             separators: tuple[int | None, int | None] | None = None) -> Candidate | None:
        clean = _BULLET.sub("", statement.strip().lstrip("\ufeff")).strip()
        if not clean or len(clean) > 300 or not clean.isprintable():
            return None
        if _DEICTIC_ROOM.match(clean):
            return None
        scope, place_ref, directive = _scope(self.place, clean)
        matcher = _matcher(self.place, directive)
        named = _NAMED_AGENT.match(clean)
        if matcher and named and named[1] != self.agent:
            return None
        if place_ref and matcher and matcher[0] == "room" and matcher[1] != place_ref.removeprefix("channel:"):
            return None
        if matcher is None and (
            self.place in _LISTS or not _OWNER_LINE.match(directive) or _UNSUPPORTED_FILTER.search(directive)
        ):
            return None
        return Candidate(self.path, self.place, self.agent, clean,
                         "filter" if matcher else "conduct", self.digest, span, matcher, separators,
                         scope, place_ref)


def _scope(place: str, statement: str) -> tuple[Scope, str | None, str]:
    if place in _LISTS:
        return "agent", None, statement
    room = _ROOM_PREFIX.match(statement)
    if room:
        return "place", "channel:" + (room[1] or room[2]), statement[room.end():]
    everyone = _ALL_PREFIX.match(statement)
    if everyone:
        return "all", None, statement[everyone.end():]
    named = _NAMED_AGENT.match(statement)
    if named:
        return "agent", None, statement[named.end():]
    addressed = _AGENT_PREFIX.match(statement)
    scope: Scope = "all" if place == "shared" else "agent"
    return scope, None, statement[addressed.end():] if addressed else statement


def _matcher(place: str, text: str) -> Matcher | None:
    if place in _LISTS:
        return (_LISTS[place][1], text) if re.fullmatch(_ID, text) else None
    for expression, (key, value) in _EXACT_FILTERS:
        match = re.fullmatch(expression, text.rstrip(".!?"), re.IGNORECASE)
        if match:
            if match.lastindex:
                value = match[1].lower() if key == "kind" else match[1]
            return key, value
    return None


def _mode(path: Path) -> int:
    try:
        info = path.lstat()
    except (FileNotFoundError, NotADirectoryError):
        return 0
    if getattr(info, "st_file_attributes", 0) & stat.FILE_ATTRIBUTE_REPARSE_POINT:
        return 0
    if stat.S_ISREG(info.st_mode) and info.st_nlink != 1:
        return 0
    return info.st_mode


def copy_root(copy: Path) -> Path:
    """Validate the explicit copy root and return its absolute path."""
    root = copy.absolute()
    if ".." in root.parts or any(not stat.S_ISDIR(_mode(part)) for part in (*reversed(root.parents), root)):
        raise CensusError("Copy must be a directory reached without symlinks.")
    marker = root / COPY_MARKER
    if not stat.S_ISREG(_mode(marker)) or marker.read_bytes() != b"":
        raise CensusError("Copy requires an empty .standing-rule-copy file.")
    return root


def _paths(root: Path, parts: tuple[str, ...], *, skip: frozenset[str] = frozenset()) -> Iterator[Path]:
    """Match path components without following symlinks, even in literal components."""
    if not parts:
        if stat.S_ISREG(_mode(root)):
            yield root
        return
    if not stat.S_ISDIR(_mode(root)):
        return
    part, *rest = parts
    if not any(char in part for char in "*?["):
        if part.casefold() not in skip:
            yield from _paths(root / part, tuple(rest), skip=skip)
        return
    if part == "**":
        yield from _paths(root, tuple(rest), skip=skip)
    for child in sorted(root.iterdir()):
        if child.name.casefold() in skip or (child.name.startswith(".") and not part.startswith(".")):
            continue
        if part == "**":
            yield from _paths(child, parts, skip=skip)
        elif fnmatchcase(child.name, part):
            yield from _paths(child, tuple(rest), skip=skip)


def _profile(root: Path, agent: str) -> Path:
    if agent == "default":
        return root
    if not re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_.-]*", agent):
        raise CensusError("Invalid copied profile name.")
    home = root / "profiles" / agent
    if not stat.S_ISDIR(_mode(home.parent)) or not stat.S_ISDIR(_mode(home)):
        raise CensusError("Copied profile does not exist.")
    return home


def _shared(root: Path) -> Iterator[Path]:
    config = root / "config.yaml"
    if not stat.S_ISREG(_mode(config)):
        return
    # The runtime config backend may be remote. Read only the explicit copied file.
    document = YAML(typ="safe").load(config.read_bytes())
    undo = document.get("undo") if isinstance(document, dict) else None
    rules = undo.get("rules") if isinstance(undo, dict) else None
    if not isinstance(rules, list):
        return
    seen: set[Path] = set()
    for pattern in rules:
        if not isinstance(pattern, str) or pattern.startswith("~") or "\x00" in pattern:
            continue
        path = Path(pattern)
        if path.is_absolute():
            if not path.is_relative_to(root):
                continue
            path = path.relative_to(root)
        if path.anchor or ".." in path.parts:
            continue
        for candidate in _paths(root, path.parts, skip=_METADATA_DIRS):
            if candidate.name.casefold() not in _REPORT_ONLY_FILES and candidate not in seen:
                seen.add(candidate)
                yield candidate


def _node_key(node: Node | None, key: str) -> Node | None:
    if isinstance(node, MappingNode):
        return next(
            (value for name, value in reversed(node.value) if isinstance(name, ScalarNode) and name.value == key),
            None,
        )
    return None


def _span(node: Node) -> Span:
    return node.start_mark.index, node.end_mark.index


def _json_node(text: str) -> Node | None:
    # JSON syntax validation is separate from composing nodes to obtain lexical offsets.
    json.loads(text.removeprefix("\ufeff"))
    return YAML(typ="safe", pure=True).compose(text)


def _physical_lines(text: str) -> Iterator[Span]:
    for match in re.finditer(r"[^\r\n]*(?:\r\n|\r|\n)|[^\r\n]+$", text):
        yield match.span()


def _json_lines(raw: str) -> Iterator[Span]:
    start = 0
    for match in _JSON_EOL.finditer(raw):
        if match[1]:
            yield start, match.end()
            start = match.end()
    if start < len(raw):
        yield start, len(raw)


def _scalar_items(source: _Source, span: Span) -> Iterator[Candidate]:
    """Map decoded JSON string lines back to their original escape sequences."""
    start, end = span
    raw = source.text[start + 1:end - 1]
    for left, right in _json_lines(raw):
        line = json.loads('"' + raw[left:right] + '"')
        candidate = source.item(line, (start + 1 + left, start + 1 + right))
        if candidate is not None:
            yield candidate


def _record_value(place: str, node: Node) -> Node | None:
    if place == "job":
        return _node_key(node, "prompt")
    if place == "honcho" and isinstance(node, MappingNode):
        return _node_key(node, "content")
    return node


def _string_node(node: Node | None) -> bool:
    return isinstance(node, ScalarNode) and node.style == '"'


def _array_items(source: _Source, rows: SequenceNode) -> Iterator[Candidate]:
    before = None
    for index, row in enumerate(rows.value):
        start, end = _span(row)
        after = source.text.index(",", end, _span(rows.value[index + 1])[0]) if index + 1 < len(rows.value) else None
        value = _record_value(source.place, row)
        if value is not None and _string_node(value):
            value_span = _span(value)
            if source.place in _LISTS:
                statement = json.loads(source.text[slice(*value_span)])
                item = source.item(statement, (start, end), separators=(before, after))
                if item is not None:
                    yield item
            else:
                for item in _scalar_items(source, value_span):
                    if source.place == "honcho" and item._span == (value_span[0] + 1, value_span[1] - 1):
                        yield replace(item, _span=(start, end), _separators=(before, after))
                    else:
                        yield item
        before = after


def _structured(source: _Source) -> Iterator[Candidate]:
    bom = int(source.text.startswith("\ufeff"))
    text = source.text[bom:]
    if source.path.suffix == ".jsonl":
        for left, right in _physical_lines(text):
            line = text[left:right]
            if not line.strip():
                continue
            row = _json_node(line)
            value = _record_value(source.place, row) if row is not None else None
            if value is not None and _string_node(value):
                start, end = _span(value)
                for item in _scalar_items(source, (bom + left + start, bom + left + end)):
                    if item._span == (bom + left + start + 1, bom + left + end - 1):
                        yield replace(item, _span=(bom + left, bom + right))
                    else:
                        yield item
        return
    root = _json_node(source.text)
    key = {"job": "jobs", "honcho": "conclusions"}.get(source.place)
    if source.place in _LISTS:
        key = _LISTS[source.place][0]
    rows = _node_key(root, key) if isinstance(root, MappingNode) and key is not None else root
    if not isinstance(rows, SequenceNode):
        raise CensusError("Invalid structured census source.")
    yield from _array_items(source, rows)


def _plain(source: _Source) -> Iterator[Candidate]:
    fence = ""
    frontmatter = source.place == "skill" and source.text.lstrip("\ufeff").splitlines()[:1] == ["---"]
    for left, right in _physical_lines(source.text):
        line = source.text[left:right].strip().lstrip("\ufeff")
        if frontmatter:
            if left and line in {"---", "..."}:
                frontmatter = False
            continue
        marker = re.match(r"^(`{3,}|~{3,})", line)
        if marker:
            token = marker[1]
            if not fence:
                fence = token
            elif token.startswith(fence) and not line[len(token):].strip():
                fence = ""
            continue
        if not fence:
            start = left + int(left == 0 and source.text.startswith("\ufeff"))
            candidate = source.item(line, (start, right))
            if candidate is not None:
                yield candidate


def _script_lists(tree: ast.Module) -> Iterator[tuple[str, ast.List]]:
    for statement in tree.body:
        if isinstance(statement, (ast.Assign, ast.AnnAssign)) and isinstance(statement.value, ast.List):
            targets = statement.targets if isinstance(statement, ast.Assign) else [statement.target]
            if len(targets) == 1 and isinstance(targets[0], ast.Name):
                key = _SCRIPT_MATCHERS.get(targets[0].id)
                if key is not None:
                    yield key, statement.value


def _script_items(source: _Source) -> Iterator[Candidate]:
    bom = int(source.text.startswith("\ufeff"))
    text = source.text[bom:]
    tree = ast.parse(text)
    lines = list(_physical_lines(text))
    trivia = {tokenize.COMMENT, tokenize.NL, tokenize.NEWLINE, tokenize.INDENT,
              tokenize.DEDENT, tokenize.ENDMARKER}
    tokens = [
        (bom + lines[token.start[0] - 1][0] + token.start[1], token.string)
        for token in tokenize.generate_tokens(StringIO(text, newline=None).readline)
        if token.type not in trivia
    ]
    positions = [position for position, _ in tokens]

    def offset(line: int, byte_column: int) -> int:
        start, end = lines[line - 1]
        # AST columns count UTF-8 bytes. Token columns and edit spans count characters.
        return bom + start + len(text[start:end].encode()[:byte_column].decode())

    for key, rows in _script_lists(tree):
        for row in rows.elts:
            if not isinstance(row, ast.Constant) or not isinstance(row.value, str):
                continue
            if row.end_lineno is None or row.end_col_offset is None:
                continue
            start = offset(row.lineno, row.col_offset)
            end = offset(row.end_lineno, row.end_col_offset)
            before, opening = tokens[bisect_left(positions, start) - 1]
            after, closing = tokens[bisect_left(positions, end)]
            if opening in {"[", ","} and closing in {"]", ","}:
                separators = (before if opening == "," else None, after if closing == "," else None)
                item = source.item(row.value, (start, end), separators=separators)
                if item is not None:
                    yield replace(item, _matcher=(key, item.statement))


def _scan(path: Path, place: str, agent: str) -> Iterator[Candidate]:
    data = path.read_bytes()
    source = _Source(path, place, agent, data.decode("utf-8"), hashlib.sha256(data).digest())
    if place == "script_constant" and path.suffix == ".py":
        yield from _script_items(source)
    elif path.suffix in {".json", ".jsonl"}:
        yield from _structured(source)
    else:
        yield from _plain(source)


def candidates(copy: Path, agent: str) -> list[Candidate]:
    """Discover removable candidate lines in deterministic owner-review order."""
    root = copy_root(copy)
    home = _profile(root, agent)
    try:
        sources = {path: "shared" for path in _shared(root)}
        for place, pattern in _SOURCES:
            for path in _paths(home, Path(pattern).parts):
                if pattern in {"USER.md", "MEMORY.md"}:
                    canonical = home / "memories" / path.name
                    if canonical in sources and path.read_bytes() == canonical.read_bytes():
                        continue
                sources.setdefault(path, place)
        return [item for path, place in sources.items() for item in _scan(path, place, agent)]
    except (YAMLError, ValueError, SyntaxError, tokenize.TokenError):
        raise CensusError("A copied census source is malformed.") from None


def _separators(items: list[Candidate]) -> Iterator[int]:
    preceding = {item._separators[0] for item in items if item._separators is not None}
    previous_after = None
    run_before = None
    for item in items:
        if item._separators is None:
            continue
        before, after = item._separators
        if before is None or before != previous_after:
            run_before = before
        else:
            yield before
        if after is None and run_before is not None:
            yield run_before
        elif after is not None and after not in preceding:
            yield after
        previous_after = after


def remove_candidates(original: bytes, candidates: list[Candidate]) -> bytes:
    """Delete selected spans from one unchanged source without writing it."""
    if not candidates:
        return original
    digest = hashlib.sha256(original).digest()
    path = candidates[0].path
    if any(item.path != path or item._digest != digest for item in candidates):
        raise CensusError("Selections must come from one unchanged source.")
    items = sorted({item._span: item for item in candidates}.values(), key=lambda item: item._span)
    spans = {item._span for item in items} | {(index, index + 1) for index in _separators(items)}
    text = original.decode("utf-8")
    keep = []
    cursor = 0
    for start, end in sorted(spans):
        if not cursor <= start <= end <= len(text):
            raise CensusError("Overlapping or invalid census selections.")
        keep.append(text[cursor:start])
        cursor = end
    return ("".join(keep) + text[cursor:]).encode("utf-8")


def owner_list(items: list[Candidate], *, limit: int = MAX_OWNER_ITEMS) -> str:
    """Render at most twelve statements for a single owner conversation."""
    visible = items[:max(0, min(limit, MAX_OWNER_ITEMS))]
    totals = Counter((item.place, item.scope, item.place_ref, item.agent, item.statement) for item in visible)
    occurrences: Counter[tuple[str, Scope, str | None, str, str]] = Counter()
    rendered = []
    for index, item in enumerate(visible, 1):
        named = _NAMED_AGENT.match(item.statement)
        scope = {"all": "all agents", "agent": f"profile {named[1] if named else item.agent}",
                 "place": item.place_ref}[item.scope]
        label = f"{item.place.replace('_', ' ')} · {scope}"
        if item._matcher:
            key, value = item._matcher
            interpretation = {
                "mentions_owner": "only items mentioning me",
                "room": "exclude room", "source": "exclude source",
                "item": "exclude item", "kind": f"exclude {value} items",
            }[key]
            label += f" · {interpretation}"
        identity = (item.place, item.scope, item.place_ref, item.agent, item.statement)
        occurrences[identity] += 1
        if totals[identity] > 1:
            label += f" · occurrence {occurrences[identity]}"
        rendered.append(f"{index}. [{label}] {item.statement}")
    return "\n".join(rendered)


def main() -> int:
    parser = argparse.ArgumentParser(description="Count candidate rules in a marked copied Hermes home.")
    parser.add_argument("copy", type=Path)
    parser.add_argument("--agent", help="Count one copied profile (default: count all profiles).")
    args = parser.parse_args()
    try:
        root = copy_root(args.copy)
        agents = [args.agent] if args.agent is not None else ["default"]
        profiles = root / "profiles"
        if args.agent is None and stat.S_ISDIR(_mode(profiles)):
            agents.extend(
                child.name for child in sorted(profiles.iterdir())
                if child.name != "default" and re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_.-]*", child.name)
                and stat.S_ISDIR(_mode(child))
            )
        report = {}
        for agent in agents:
            counts = Counter(item.place for item in candidates(root, agent))
            for path in _paths(_profile(root, agent), ("SOUL.self.md",)):
                counts["self_note"] = sum(1 for _ in _scan(path, "self_note", agent))
            report[agent] = counts
    except (OSError, ValueError, YAMLError):
        parser.exit(2, "Census could not read the marked copy or a copied source.\n")
    print(json.dumps(report, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
