"""Split legacy persona text into exact common and per-home occurrences (lina#825)."""

import re
from collections import Counter, defaultdict
from dataclasses import dataclass
from difflib import SequenceMatcher
from hashlib import sha256
from io import StringIO
from collections.abc import Iterator
from typing import Final, Literal, NotRequired, TypedDict


_HEADING: Final = re.compile(r" {0,3}(#{1,6})(?=[ \t\r\n]|$)")
_FENCE: Final = re.compile(r" {0,3}(`{3,}|~{3,})([^\r\n]*)")
_NEAR_RATIO: Final = 0.90

Membership = Literal["common", "partly_shared", "single"]
Disposition = Literal["common", "persona", "dropped"]


class Location(TypedDict):
    start_line: int
    end_line: int


class Occurrence(TypedDict):
    home: str
    location: Location
    normalized_sha256: str
    raw_sha256: str


class NearMatch(Occurrence):
    ratio: float


class LedgerRow(Occurrence):
    membership: Membership
    disposition: Disposition
    near_matches: list[NearMatch]
    reason: NotRequired[str]
    heading_lines: NotRequired[list[int]]


class GroupStats(TypedDict):
    groups: int
    occurrences: int
    characters: int


class HomeStats(TypedDict):
    source_chars: int
    persona_chars: int
    common_chars: int
    dropped_chars: int
    membership: dict[Membership, int]
    counts: dict[Disposition, int]


class SplitSummary(TypedDict):
    distinct: int
    occurrences: int
    common: int
    near: int
    partly_shared: int
    counts: dict[Disposition, int]
    group_histogram: dict[str, GroupStats]
    homes: dict[str, HomeStats]
    ledger_lines: int


@dataclass(frozen=True, slots=True)
class PersonaSplitResult:
    personas: dict[str, str]
    common: dict[str, str]
    ledger: list[LedgerRow]
    summary: SplitSummary


class UnmatchedDropError(ValueError):
    """One or more supplied drop digests have no source occurrence."""

    def __init__(self, digests: tuple[str, ...]) -> None:
        self.digests = digests
        super().__init__(f"Unmatched normalized drop digests: {', '.join(digests)}")


@dataclass(frozen=True, slots=True)
class _Unit:
    raw: str
    normalized: str
    span: range
    headings: tuple[int, ...]
    identity: Occurrence


def _digest(text: str) -> str:
    return sha256(text.encode("utf-8")).hexdigest()


def _structural_lines(lines: list[str]) -> Iterator[tuple[int, str]]:
    """Only the opening line of a fenced block participates in unit boundaries."""
    fence = ""
    for index, line in enumerate(lines):
        marker = _FENCE.match(line)
        if fence:
            if marker and marker[1].startswith(fence) and not marker[2].strip(" \t"):
                fence = ""
            continue
        if marker and (marker[1][0] == "~" or "`" not in marker[2]):
            fence = marker[1]
        yield index, line
    yield len(lines), ""  # Flush the final unit, including an unclosed fence.


def _parse(home: str, lines: list[str], empty_sections: set[int]) -> list[_Unit]:
    """Collect units and their active ATX heading ancestors by source line."""
    units: list[_Unit] = []
    headings: list[tuple[int, int]] = []
    heading_without_body = False
    start: int | None = None
    for index, line in _structural_lines(lines):
        blank = not line.strip()
        heading = _HEADING.match(line)
        if start is not None and (blank or heading or line.startswith("- ")):
            raw = "".join(lines[start:index])
            normalized = " ".join(raw.split())
            identity = Occurrence(
                home=home,
                location={"start_line": start + 1, "end_line": index},
                normalized_sha256=_digest(normalized),
                raw_sha256=_digest(raw),
            )
            units.append(
                _Unit(raw, normalized, range(start, index), tuple(i for _, i in headings), identity)
            )
            start = None
        if heading_without_body and (index == len(lines) or (heading and len(heading[1]) <= headings[-1][0])):
            # No unit can carry this section; keep its heading and ancestors in the persona.
            empty_sections.update(i for _, i in headings)
        if heading:
            level = len(heading[1])
            while headings and headings[-1][0] >= level:
                headings.pop()
            headings.append((level, index))
            heading_without_body = True
        elif not blank and start is None:
            start = index
            heading_without_body = False
    return units


def _title(lines: list[str]) -> int | None:
    """The leading level-1 heading when deeper sections follow it: it names the home."""
    first: int | None = None
    for index, line in _structural_lines(lines):
        heading = _HEADING.match(line)
        if heading is None:
            continue
        if first is None:
            if len(heading[1]) != 1:
                return None
            first = index
        elif len(heading[1]) > 1:
            return first
    return None


def _membership(homes: int, total_homes: int) -> Membership:
    if homes >= 3 and 3 * homes >= 2 * total_homes:
        return "common"
    return "partly_shared" if homes > 1 else "single"


@dataclass(frozen=True, slots=True)
class _Group:
    units: list[_Unit]
    homes: set[str]
    membership: Membership


def _near_matches(single: _Group, groups: dict[str, _Group]) -> list[NearMatch]:
    """Report every cross-home occurrence of a sufficiently similar unit."""
    text = single.units[0].normalized
    matches: list[NearMatch] = []
    for candidate_text, candidate in groups.items():
        if candidate.homes <= single.homes:
            continue
        matcher = SequenceMatcher(None, text, candidate_text)
        if matcher.quick_ratio() < _NEAR_RATIO:
            continue
        ratio = max(matcher.ratio(), SequenceMatcher(None, candidate_text, text).ratio())
        if ratio >= _NEAR_RATIO:
            matches.extend(
                NearMatch(**unit.identity, ratio=ratio)
                for unit in candidate.units
                if unit.identity["home"] not in single.homes
            )
    return matches


def _groups(units_by_home: dict[str, list[_Unit]]) -> dict[str, _Group]:
    grouped: defaultdict[str, list[_Unit]] = defaultdict(list)
    for units in units_by_home.values():
        for unit in units:
            grouped[unit.normalized].append(unit)
    groups: dict[str, _Group] = {}
    for normalized, occurrences in grouped.items():
        homes = {unit.identity["home"] for unit in occurrences}
        groups[normalized] = _Group(occurrences, homes, _membership(len(homes), len(units_by_home)))
    return groups


def _summarize_groups(groups: dict[str, _Group], summary: SplitSummary) -> None:
    """Count normalized groups and their raw occurrences by home cardinality."""
    for group in groups.values():
        bin_stats = summary["group_histogram"].setdefault(
            str(len(group.homes)), {"groups": 0, "occurrences": 0, "characters": 0}
        )
        bin_stats["groups"] += 1
        bin_stats["occurrences"] += len(group.units)
        bin_stats["characters"] += sum(len(unit.raw) for unit in group.units)


def _render_persona(lines: list[str], keep: set[int], removed: set[int]) -> str:
    if not keep:
        return ""
    parts: list[str] = []
    for index, line in enumerate(lines):
        if index in keep or (not line.strip() and index not in removed):
            # A bare CR and a formerly non-adjacent blank LF must stay two
            # line endings, or the neighboring retained unit changes on read.
            if line == "\n" and parts and parts[-1].endswith("\r"):
                parts.append(" ")
            parts.append(line)
    return "".join(parts)


def _shared_headings(
    lines_by_home: dict[str, list[str]], units_by_home: dict[str, list[_Unit]]
) -> dict[str, set[int]]:
    """Per home, the ancestor headings that the common file may carry.

    Only a heading that passes the same membership test as a unit travels with it.
    """
    seen: defaultdict[str, set[str]] = defaultdict(set)
    by_home: dict[str, dict[int, str]] = {}
    for home, units in units_by_home.items():
        indexes = {i for unit in units for i in unit.headings}
        by_home[home] = {i: " ".join(lines_by_home[home][i].split()) for i in indexes}
        for text in by_home[home].values():
            seen[text].add(home)
    total = len(units_by_home)
    return {
        home: {i for i, text in heading_texts.items() if _membership(len(seen[text]), total) == "common"}
        for home, heading_texts in by_home.items()
    }


def _render_common(lines: list[str], units: list[tuple[_Unit, tuple[int, ...]]]) -> str:
    parts: list[str] = []
    previous_headings: list[str] = []
    for unit, chain in units:
        text = unit.raw
        headings = [lines[i] for i in chain]
        if headings != previous_headings:
            text = "".join(headings) + text
            previous_headings = headings
        if parts:
            # Count existing boundary newlines without changing either raw unit.
            left = parts[-1][-4:].replace("\r\n", "\n")
            right = text[:4].replace("\r\n", "\n")
            newlines = len(left) - len(left.rstrip("\n")) + len(right) - len(right.lstrip("\n"))
            parts.append("\n" * max(0, 2 - newlines))
        parts.append(text)
    return "".join(parts)


def split_personas(sources: dict[str, str], drops: dict[str, str]) -> PersonaSplitResult:
    """Split in source order with whitespace-only comparison and exact raw spans.

    Ledger locations are 1-based inclusive line ranges. Membership describes
    the source set before drops. Near matches are advisory and only reported
    on singleton groups. Drop reasons appear only on explicitly dropped rows.
    Aggregate membership counts refer to normalized groups before drops.
    """
    lines_by_home = {home: list(StringIO(source, newline="")) for home, source in sources.items()}
    empty_sections: dict[str, set[int]] = {home: set() for home in sources}
    units_by_home = {home: _parse(home, lines, empty_sections[home]) for home, lines in lines_by_home.items()}
    groups = _groups(units_by_home)
    unmatched = drops.keys() - {group.units[0].identity["normalized_sha256"] for group in groups.values()}
    if unmatched:
        raise UnmatchedDropError(tuple(sorted(unmatched)))
    near = {
        text: _near_matches(group, groups)
        for text, group in groups.items()
        if group.membership == "single"
    }
    result = PersonaSplitResult(
        personas={},
        common={},
        ledger=[],
        summary={
            "distinct": len(groups),
            "occurrences": sum(map(len, units_by_home.values())),
            "common": sum(group.membership == "common" for group in groups.values()),
            "near": sum(bool(matches) for matches in near.values()),
            "partly_shared": sum(group.membership == "partly_shared" for group in groups.values()),
            "counts": {},
            "group_histogram": {},
            "homes": {},
            "ledger_lines": 0,
        },
    )
    shared_headings = _shared_headings(lines_by_home, units_by_home)
    titles = {home: _title(lines) for home, lines in lines_by_home.items()}
    for home, units in units_by_home.items():
        common_units: list[tuple[_Unit, tuple[int, ...]]] = []
        keep = empty_sections[home]
        removed: set[int] = set()
        membership_counts: Counter[Membership] = Counter()
        counts: Counter[Disposition] = Counter()
        characters: Counter[Disposition] = Counter()
        for unit in units:
            membership = groups[unit.normalized].membership
            reason = drops.get(unit.identity["normalized_sha256"])
            row = LedgerRow(
                **unit.identity,
                membership=membership,
                disposition="persona",
                near_matches=near.get(unit.normalized, []),
            )
            if unit.headings:
                row["heading_lines"] = [i + 1 for i in unit.headings]
            if reason is not None:
                row["disposition"] = "dropped"
                row["reason"] = reason
            elif membership == "common" and all(
                i in shared_headings[home] or i == titles[home] for i in unit.headings
            ):
                # A home's own title names the home and changes nothing about the unit.
                # Any other heading that cannot travel stays with the unit in the persona.
                row["disposition"] = "common"
                chain = tuple(i for i in unit.headings if i in shared_headings[home])
                keep.update(set(unit.headings) - set(chain))
                common_units.append((unit, chain))
            else:
                keep.update(unit.span)
                keep.update(unit.headings)
            if row["disposition"] != "persona":
                removed.update(unit.span)
            result.ledger.append(row)
            membership_counts[membership] += 1
            counts[row["disposition"]] += 1
            characters[row["disposition"]] += len(unit.raw)
        # Blank lines are separators outside units. Keep them in surviving
        # personas so removal cannot join adjacent paragraphs.
        persona = _render_persona(lines_by_home[home], keep, removed)
        result.personas[home] = persona
        result.common[home] = _render_common(lines_by_home[home], common_units)
        result.summary["homes"][home] = HomeStats(
            source_chars=len(sources[home]),
            persona_chars=characters["persona"],
            common_chars=characters["common"],
            dropped_chars=characters["dropped"],
            membership=dict(membership_counts),
            counts=dict(counts),
        )
    _summarize_groups(groups, result.summary)
    result.summary["counts"] = dict(Counter(row["disposition"] for row in result.ledger))
    result.summary["ledger_lines"] = len(result.ledger)
    return result
