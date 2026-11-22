"""Split legacy persona text into exact common and per-home occurrences (lina#825)."""

import re
from collections import Counter, defaultdict
from collections.abc import Iterator, Set as AbstractSet
from dataclasses import dataclass
from difflib import SequenceMatcher
from graphlib import CycleError, TopologicalSorter
from hashlib import sha256
from io import StringIO
from itertools import pairwise
from typing import Final, Literal, NotRequired, TypedDict


_HEADING: Final = re.compile(r" {0,3}(#{1,6})(?=[ \t\r\n]|$)")
_FENCE: Final = re.compile(r" {0,3}(`{3,}|~{3,})([^\r\n]*)")
_NEAR_RATIO: Final = 0.90

Membership = Literal["common", "partly_shared", "single", "exempt"]
Layer = Literal["common", "work"]
Disposition = Literal["common", "work", "replaced", "persona", "dropped"]


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
    canonical: NotRequired[Occurrence]
    ratio: NotRequired[float]


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


class LayerStats(TypedDict):
    paragraphs: int
    characters: int


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
    layers: dict[Layer, LayerStats]


@dataclass(frozen=True, slots=True)
class PersonaSplitResult:
    personas: dict[str, str]
    common: dict[str, str]
    work: dict[str, str]
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


def _ratio(left: str, right: str) -> float:
    matcher = SequenceMatcher(None, left, right)
    if matcher.quick_ratio() < _NEAR_RATIO:
        return 0.0
    return max(matcher.ratio(), SequenceMatcher(None, right, left).ratio())


def _near_matches(group: _Group, groups: dict[str, _Group]) -> list[NearMatch]:
    """Report singleton similarities and possible variants of more widely shared rules."""
    text = group.units[0].normalized
    matches: list[NearMatch] = []
    for candidate_text, candidate in groups.items():
        if candidate.homes <= group.homes:
            continue
        if len(group.homes) > 1 and len(candidate.homes) < len(group.homes):
            continue
        ratio = _ratio(text, candidate_text)
        if ratio >= _NEAR_RATIO:
            matches.extend(
                NearMatch(**unit.identity, ratio=ratio)
                for unit in candidate.units
                if unit.identity["home"] not in group.homes
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
        home: {
            i for i, text in heading_texts.items()
            if len(seen[text]) == total or _membership(len(seen[text]), total) == "common"
        }
        for home, heading_texts in by_home.items()
    }


def _render_common(units: list[tuple[_Unit, tuple[str, ...]]]) -> str:
    parts: list[str] = []
    previous_headings: tuple[str, ...] = ()
    for unit, headings in units:
        text = unit.raw
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


@dataclass(frozen=True, slots=True)
class _Copy:
    unit: _Unit
    headings: tuple[str, ...]

    @property
    def context(self) -> tuple[str, ...]:
        return tuple(" ".join(heading.split()) for heading in self.headings)


def _movable_copies(
    active: dict[str, list[_Unit]], lines: dict[str, list[str]], shared: dict[str, set[int]],
    titles: dict[str, int | None], drops: dict[str, str],
) -> dict[str, dict[str, _Copy]]:
    copies: defaultdict[str, dict[str, _Copy]] = defaultdict(dict)
    for home in sorted(active):
        for unit in active[home]:
            if unit.identity["normalized_sha256"] in drops:
                continue
            if any(i not in shared[home] and i != titles[home] for i in unit.headings):
                continue
            chain = tuple(lines[home][i] for i in unit.headings if i in shared[home])
            # Only one occurrence per home can be represented by one shared paragraph.
            copies[unit.normalized].setdefault(home, _Copy(unit, chain))
    return dict(copies)


def _proposals(
    copies: dict[str, dict[str, _Copy]], homes: set[str], groups: dict[str, _Group],
) -> dict[str, dict[str, tuple[_Copy, float]]]:
    """Reserve each missing home's unique best match for at most one canonical rule."""
    proposals: defaultdict[str, dict[str, tuple[_Copy, float]]] = defaultdict(dict)
    claims: Counter[tuple[str, str]] = Counter()
    for text, exact in copies.items():
        if len(exact) < 2 or len(exact) == len(homes) or groups[text].homes != exact.keys():
            continue
        context = next(iter(exact.values())).context
        for home in homes - exact.keys():
            best: list[_Copy] = []
            score = _NEAR_RATIO
            for other, variants in copies.items():
                if home not in variants or len(variants) >= len(exact) or variants[home].context != context:
                    continue
                ratio = _ratio(text, other)
                if ratio > score:
                    best.clear()
                    score = ratio
                if ratio == score:
                    best.append(variants[home])
            for variant in best:
                claims[home, variant.unit.normalized] += 1
            if len(best) == 1:
                proposals[text][home] = (best[0], score)
    return {
        text: matches for text, matches in proposals.items()
        if copies[text].keys() | matches.keys() == homes
        and all(claims[home, copy.unit.normalized] == 1 for home, (copy, _) in matches.items())
    }


def _ordered_layer(
    texts: list[str], copies: dict[str, dict[str, _Copy]], homes: set[str],
) -> str:
    order: TopologicalSorter[str] = TopologicalSorter()
    for text in texts:
        order.add(text)
    for home in sorted(homes):
        sequence = sorted(
            (copies[text][home].unit.span.start, text) for text in texts if home in copies[text]
        )
        for (_, before), (_, after) in pairwise(sequence):
            order.add(after, before)
    try:
        ordered = tuple(order.static_order())
    except CycleError as exc:
        raise ValueError("conflicting canonical paragraph order across exact sources") from exc
    canonical = [next(iter(copies[text].values())) for text in ordered]
    return _render_common([(copy.unit, copy.headings) for copy in canonical])


@dataclass(frozen=True, slots=True)
class _LayerPlan:
    layers: dict[Layer, tuple[str, set[str], int]]
    placements: dict[tuple[str, int], Disposition]
    replacements: dict[tuple[str, int], tuple[Occurrence, float]]


def _plan_layers(
    copies: dict[str, dict[str, _Copy]], homes: set[str], groups: dict[str, _Group],
) -> _LayerPlan:
    # A common heading in two different contexts can reverse a rule's meaning.
    copies = {text: exact for text, exact in copies.items() if len({c.context for c in exact.values()}) == 1}
    proposals = _proposals(copies, homes, groups)
    matches = {
        text: by_home for text, by_home in proposals.items()
        if not any(copy.unit.normalized in proposals for copy, _ in by_home.values())
    }
    common = [t for t, exact in copies.items() if len(homes) > 1 and (exact.keys() == homes or t in matches)]
    consumed = {copy.unit.normalized for t in common for copy, _ in matches.get(t, {}).values()}
    work = [t for t in copies if groups[t].membership == "common" and t not in common and t not in consumed]
    participants = homes.intersection(*(copies[t].keys() for t in work)) if work else set()
    placements: dict[tuple[str, int], Disposition] = {}
    replacements: dict[tuple[str, int], tuple[Occurrence, float]] = {}
    layers: dict[Layer, tuple[str, set[str], int]] = {}
    targets: dict[Layer, tuple[list[str], set[str]]] = {"common": (common, homes), "work": (work, participants)}
    for layer, (texts, recipients) in targets.items():
        rendered = _ordered_layer(texts, copies, homes) if recipients else ""
        layers[layer] = rendered, recipients, len(texts) if recipients else 0
        for text in texts:
            for home, copy in copies[text].items():
                if home in recipients:
                    placements[home, copy.unit.span.start] = layer
    for text in common:
        canonical = next(iter(copies[text].values())).unit.identity
        for home, (copy, ratio) in matches.get(text, {}).items():
            key = (home, copy.unit.span.start)
            placements[key] = "replaced"
            replacements[key] = (canonical, ratio)
    return _LayerPlan(layers, placements, replacements)


def split_personas(
    sources: dict[str, str], drops: dict[str, str], *, exempt: AbstractSet[str] = frozenset(),
) -> PersonaSplitResult:
    """Split in source order with whitespace-only comparison and exact raw spans.

    Ledger locations are 1-based inclusive line ranges. Membership describes
    the participating source set before drops. Shared files use the same
    canonical text in every recipient home.
    """
    lines_by_home = {home: list(StringIO(source, newline="")) for home, source in sources.items()}
    empty_sections: dict[str, set[int]] = {home: set() for home in sources}
    units_by_home = {home: _parse(home, lines, empty_sections[home]) for home, lines in lines_by_home.items()}
    if unknown := exempt - sources.keys():
        raise ValueError(f"unknown exempt profile: {', '.join(sorted(unknown))}")
    active = {home: units for home, units in units_by_home.items() if home not in exempt}
    groups = _groups(active)
    source_groups = _groups(units_by_home) if exempt else groups
    unmatched = drops.keys() - {unit.identity["normalized_sha256"] for units in units_by_home.values() for unit in units}
    if unmatched:
        raise UnmatchedDropError(tuple(sorted(unmatched)))
    near = {
        text: _near_matches(group, groups)
        for text, group in groups.items()
        if len(group.homes) < len(active)
    }
    result = PersonaSplitResult(
        personas={},
        common={},
        work={},
        ledger=[],
        summary={
            "distinct": len(source_groups),
            "occurrences": sum(map(len, units_by_home.values())),
            "common": sum(group.membership == "common" for group in groups.values()),
            "near": sum(bool(matches) for matches in near.values()),
            "partly_shared": sum(group.membership == "partly_shared" for group in groups.values()),
            "counts": {},
            "group_histogram": {},
            "homes": {},
            "ledger_lines": 0,
            "layers": {},
        },
    )
    shared_headings = _shared_headings(lines_by_home, active)
    titles = {home: _title(lines) for home, lines in lines_by_home.items()}
    copies = _movable_copies(active, lines_by_home, shared_headings, titles, drops)
    plan = _plan_layers(copies, set(active), groups)
    for layer, (rendered, recipients, count) in plan.layers.items():
        outputs = result.common if layer == "common" else result.work
        outputs.update({home: rendered if home in recipients else "" for home in sources})
        result.summary["layers"][layer] = {"paragraphs": count, "characters": len(rendered)}
    for home, units in units_by_home.items():
        keep = empty_sections[home]
        removed: set[int] = set()
        membership_counts: Counter[Membership] = Counter()
        counts: Counter[Disposition] = Counter()
        characters: Counter[Disposition] = Counter()
        for unit in units:
            membership: Membership = "exempt" if home in exempt else groups[unit.normalized].membership
            reason = None if home in exempt else drops.get(unit.identity["normalized_sha256"])
            key = (home, unit.span.start)
            row = LedgerRow(
                **unit.identity,
                membership=membership,
                disposition=plan.placements.get(key, "persona"),
                near_matches=[] if home in exempt else near.get(unit.normalized, []),
            )
            if unit.headings:
                row["heading_lines"] = [i + 1 for i in unit.headings]
            if reason is not None:
                row["disposition"] = "dropped"
                row["reason"] = reason
            elif row["disposition"] != "persona":
                keep.update(set(unit.headings) - shared_headings[home])
                if key in plan.replacements:
                    canonical, ratio = plan.replacements[key]
                    row.update(canonical=canonical, ratio=ratio)
                    row["reason"] = f"replaced by common: common:{canonical['normalized_sha256'][:12]}"
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
        result.personas[home] = sources[home] if home in exempt else persona
        result.summary["homes"][home] = HomeStats(
            source_chars=len(sources[home]),
            persona_chars=characters["persona"],
            common_chars=characters["common"],
            dropped_chars=characters["dropped"],
            membership=dict(membership_counts),
            counts={
                "common": counts["common"], "work": counts["work"], "replaced": counts["replaced"],
                "persona": counts["persona"], "dropped": counts["dropped"],
            },
        )
    _summarize_groups(source_groups, result.summary)
    result.summary["counts"] = dict(Counter(row["disposition"] for row in result.ledger))
    result.summary["ledger_lines"] = len(result.ledger)
    return result
