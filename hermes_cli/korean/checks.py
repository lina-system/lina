"""Deterministic, span-local Korean checks and an advisory reader boundary (lina#381)."""

from __future__ import annotations

import json
import re
from collections.abc import Sequence
from dataclasses import dataclass, field
from difflib import SequenceMatcher
from functools import lru_cache
from pathlib import Path
from typing import Protocol

from hermes_cli.korean import words

Span = tuple[int, int]
PARTICLES = (("을", "를"), ("이", "가"), ("은", "는"), ("과", "와"), ("으로", "로"))
CALQUE_HITS_PER_100 = 2
REPORT_MIN_CHARS = 1000
_NUMBER = re.compile(r"[+-]?\d+(?:[.,]\d+)*|(?:"
                     r"(?:열|스물|스무|서른|마흔|쉰|예순|일흔|여든|아흔)(?:한|두|세|네|다섯|여섯|일곱|여덟|아홉)?"
                     r"|한|두|세|네|다섯|여섯|일곱|여덟|아홉|[영일이삼사오육칠팔구십백천만억조]+)"
                     r"\s*(?:개|명|번|원|시|분|초|살|년|월|일)"
                     r"|하나|둘|셋|넷|다섯|여섯|일곱|여덟|아홉|스물|스무|서른|마흔|쉰|예순|일흔|여든|아흔")
_NEGATION = re.compile(r"(?<!\w)안(?!\w)|(?<!\w)"
                       r"(?:못|않|없|아니|금지|불가|말다|말아|말았|말고|말지|마세요|마십시오|안[하해했할한합])\w*")


@dataclass(frozen=True)
class Edit:
    start: int
    end: int
    replacement: str = field(repr=False)
    word_changed: bool = False
    particle_changed: bool = False


@dataclass(frozen=True)
class CheckResult:
    text: str = field(repr=False)
    edits: tuple[Edit, ...] = ()
    dropped: bool = False
    reason: str | None = None
    calques: tuple[Span, ...] = ()


def particle_for(noun: str, particle: str) -> str:
    """Reselect a particle from the last composed Hangul syllable; other scripts stay as supplied."""
    if not noun or not "\uac00" <= noun[-1] <= "\ud7a3":
        return particle
    final = (ord(noun[-1]) - 0xAC00) % 28
    for consonant, vowel in PARTICLES:
        if particle in (consonant, vowel):
            return consonant if final and not (consonant == "으로" and final == 8) else vowel
    return particle


def proposed_edits(text: str, entries: Sequence[words.WordEntry]) -> tuple[Edit, ...]:
    """Match original offsets, longest noun first; never feed replacements back into the scanner."""
    swaps: dict[str, str] = {}
    for entry in entries:
        if entry.retired_at is None:
            if entry.avoid in swaps and swaps[entry.avoid] != entry.use:
                raise ValueError("ambiguous word list")
            swaps[entry.avoid] = entry.use
    nouns = set(swaps) | set(swaps.values())
    if not nouns:
        return ()
    alternatives = "|".join(re.escape(noun) for noun in sorted(nouns, key=lambda s: (-len(s), s)))
    pattern = re.compile(rf"(?<!\w)(?P<noun>{alternatives})(?P<particle>으로|로|을|를|이|가|은|는|와|과)?(?!\w)")
    edits = []
    for hit in pattern.finditer(text):
        noun, particle = hit["noun"], hit["particle"] or ""
        use = swaps.get(noun, noun)
        selected = particle_for(use, particle)
        replacement = use + selected
        if replacement != hit[0]:
            edits.append(Edit(hit.start(), hit.end(), replacement, use != noun, selected != particle))
    return tuple(edits)


def _drop_reason(text: str, edits: tuple[Edit, ...], names: Sequence[str] | None) -> str | None:
    if edits and names is None:
        return "names_unavailable"
    changed = 0
    for edit in edits:
        before, after = text[edit.start:edit.end], edit.replacement
        candidate = text[:edit.start] + after + text[edit.end:]
        for pattern, reason in ((_NUMBER, "number"), (_NEGATION, "negation")):
            if pattern.findall(text) != pattern.findall(candidate):
                return reason
        for name in names or ():
            # Check the whole name, including names that cross the edge of a matched phrase.
            for hit in re.finditer(re.escape(name), text):
                if hit.start() < edit.end and edit.start < hit.end():
                    relative = hit.start() - edit.start
                    if relative < 0 or after[relative:relative + len(name)] != name:
                        return "name"
            if after.count(name) != before.count(name):
                return "name"
        changed += sum(max(b - a, d - c) for tag, a, b, c, d in
                       SequenceMatcher(None, before, after, autojunk=False).get_opcodes() if tag != "equal")
    return "change_ratio" if changed > len(text) * 0.30 else None


def check_reply(text: str, entries: Sequence[words.WordEntry] | None = None, *,
                names: Sequence[str] | None = None) -> CheckResult:
    """Return an atomic fix, or the original with a reason. No send is gated by this callable.

    Names must be supplied by the caller; None refuses edits, () explicitly means no names.
    This is the fail-open boundary: exception details may contain private text and are never emitted.
    """
    try:
        found = words.word_list_entries() if entries is None else entries
        edits = proposed_edits(text, found)
        calques = tuple(calque_spans(text))
        reason = _drop_reason(text, edits, names)
        if reason:
            return CheckResult(text, dropped=True, reason=reason, calques=calques)
        parts, cursor = [], 0
        for edit in edits:
            parts.extend((text[cursor:edit.start], edit.replacement))
            cursor = edit.end
        parts.append(text[cursor:])
        return CheckResult("".join(parts), edits, calques=calques)
    except Exception:  # health: allow BLE001 -- mandated fail-open boundary; tracebacks can expose private replies
        return CheckResult(text, dropped=True, reason="check_error")


@dataclass(frozen=True)
class Calque:
    phrase: str
    before: str
    after: str


@lru_cache(maxsize=1)
def calque_catalog() -> tuple[Calque, ...]:
    data = json.loads((Path(__file__).parents[1] / "data" / "korean_calques.json").read_text(encoding="utf-8-sig"))
    return tuple(Calque(**entry) for entry in data)


def calque_spans(text: str) -> list[Span]:
    """Advisory only, at least two occurrences per 100 original Unicode characters."""
    spans = sorted({hit.span() for entry in calque_catalog()
                    for hit in re.finditer(re.escape(entry.phrase), text)})
    return spans if text and len(spans) * 100 >= CALQUE_HITS_PER_100 * len(text) else []


def register_checks(entries: Sequence[words.WordEntry]) -> None:
    """Install defaults for an unconfigured evaluator, refreshing an existing default snapshot."""
    from hermes_cli.korean import eval as evaluation

    if evaluation.CHECKS and "word-list" not in evaluation.CHECKS:
        return
    snapshot = tuple(entries)
    defaults = {
        "word-list": lambda text: [(e.start, e.end) for e in proposed_edits(text, snapshot) if e.word_changed],
        "particles": lambda text: [(e.start, e.end) for e in proposed_edits(text, snapshot) if e.particle_changed],
        "calques": calque_spans,
    }
    for name, check in defaults.items():
        evaluation.CHECKS.pop(name, None)
        evaluation.register(name, check)


@dataclass(frozen=True)
class Document:
    text: str = field(repr=False)
    kind: str
    writer_family: str


class Reader(Protocol):
    family: str

    def notes(self, text: str) -> tuple[str, ...]: ...


@dataclass(frozen=True)
class StubReader:
    family: str

    def notes(self, text: str) -> tuple[str, ...]:
        return ("Stub advice: ask the writer to review readability; no model was called.",)


@dataclass(frozen=True)
class ReaderResult:
    text: str = field(repr=False)
    notes: tuple[str, ...] = ()
    ran: bool = False


def read_report(document: Document, reader: Reader) -> ReaderResult:
    eligible = (document.kind == "report" and len(document.text) >= REPORT_MIN_CHARS
                and bool(document.writer_family.strip()) and bool(reader.family.strip())
                and document.writer_family.strip().casefold() != reader.family.strip().casefold())
    if not eligible:
        return ReaderResult(document.text)
    return ReaderResult(document.text, reader.notes(document.text), ran=True)
