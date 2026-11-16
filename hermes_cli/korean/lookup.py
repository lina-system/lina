"""What the owner called a concept in his own messages (lina#379), so an agent uses his word instead of coining one.

Only the owner's own rows are read (``memory.messages.speaker = 'owner'``, lina#324): an agent's reply that used a
word never makes it the owner's. A result names terms, how many of his messages used each and their uids; it never
carries the text of a message.

Matching goes through Kiwi's morphological analysis (``kiwipiepy``, the ``korean`` extra), so endings and particles
do not hide a match: 봤어, 봤다 and 보고 are all the predicate 보다, and 취향 목록을 holds the nouns 취향 목록. A
concept is read as units, a run of nouns (written together or with spaces between; its key is the nouns joined, so
취향목록 and 취향 목록 are one key and stay two terms in his spelling) or one predicate (its key is the dictionary
form). It is found in a message where the same units follow one another with only particles, endings and suffixes
between; a run of nouns may sit inside a longer one (취향 목록 in 취향 목록 정리). An English concept ("taste slot")
is found as words in his messages; the result is then the Korean noun runs of the same messages, most used first,
then nearest to the English words.

A concept he never named gives no terms and the ``HINT``. ``is_coined`` tells whether a compound is neither his
word nor a noun of Kiwi's built-in dictionary (``reference_words``).
"""

from __future__ import annotations

import functools
import re
import unicodedata
from collections.abc import Iterable, Sequence
from dataclasses import dataclass, field
from typing import Any, Optional

HINT = "describe it in plain words; do not coin a name"
# The newest owner messages read per lookup, after the store's substring prefilter.
SCAN_ROWS = 5000
MAX_TERMS = 10
UIDS_PER_TERM = 20

_NOUN_TAGS = frozenset({"NNG", "NNP", "NR", "XR", "SH"})
_PREDICATE_TAGS = frozenset({"VV", "VA", "VX"})
_HANGUL = re.compile(r"[가-힣]")
_LATIN = re.compile(r"[A-Za-z]")


class KoreanUnavailable(RuntimeError):
    """Kiwi is not installed here and could not be installed."""


@dataclass(frozen=True)
class OwnerTerm:
    term: str
    count: int  # owner messages that used it
    uids: tuple[str, ...] = field(default=())  # newest first, at most UIDS_PER_TERM

    def as_dict(self) -> dict[str, Any]:
        return {"term": self.term, "count": self.count, "uids": list(self.uids)}


@dataclass(frozen=True)
class _Atom:
    noun: bool  # a noun, else a predicate
    key: str  # a noun's form, a predicate's dictionary form
    start: int
    end: int
    link: str  # to the atom before: "run" (nouns together or spaced), "glue" (only particles, endings, suffixes
    #            between) or "break" (anything else, or the start of the text)


@functools.cache
def analyzer():
    """The process's one Kiwi, installed through PM's ``korean`` extra on first use."""
    import pm
    from pm.package import InstallError
    try:
        pm.ensure_import("korean")
    except InstallError as exc:
        raise KoreanUnavailable(f"Korean analysis needs Kiwi: {exc}; run `{pm.install_hint('korean')}`") from exc
    from kiwipiepy import Kiwi
    return Kiwi()


def _tag(token: Any) -> str:
    return str(token.tag).split("-")[0]  # VV-R, VA-I: the irregular-conjugation marks


def _atoms(tokens: Sequence[Any]) -> list[_Atom]:
    atoms: list[_Atom] = []
    link = "break"
    for token in tokens:
        tag = _tag(token)
        if tag in _NOUN_TAGS:
            atoms.append(_Atom(True, token.form, token.start, token.end, link))
            link = "run"
        elif tag in _PREDICATE_TAGS:
            atoms.append(_Atom(False, token.lemma, token.start, token.end, link))
            link = "glue"
        elif tag[0] in "JE" or tag.startswith("XS") or tag in ("VCP", "VCN"):
            link = "break" if link == "break" else "glue"
        else:
            link = "break"
    return atoms


def _units(atoms: Sequence[_Atom]) -> list[tuple[bool, str]]:
    """``(noun, key)`` per unit: a run of nouns joined, or one predicate."""
    units: list[tuple[bool, str]] = []
    for atom in atoms:
        if atom.noun and atom.link == "run" and units and units[-1][0]:
            units[-1] = (True, units[-1][1] + atom.key)
        else:
            units.append((atom.noun, atom.key))
    return units


def _analyse(text: str) -> list[_Atom]:
    return _atoms(analyzer().tokenize(text))


def stems(text: str) -> list[str]:
    """The keys of *text*'s units in order: 봤어, 봤다 and 보고 are each ``["보다"]``."""
    return [key for _noun, key in _units(_analyse(_normal(text)))]


def _concept_units(concept: str) -> list[tuple[bool, str]]:
    """A concept's units. One in dictionary form (보다, 만들다) is read as a predicate when Kiwi offers that reading,
    though its first reading may be another word (보다 the adverb)."""
    readings = [_units(_atoms(tokens)) for tokens, _score in analyzer().analyze(concept, top_n=4)]
    if concept.endswith("다"):
        for units in readings:
            if units and not units[-1][0]:
                return units
    return readings[0] if readings else []


def _match(atoms: Sequence[_Atom], at: int, units: Sequence[tuple[bool, str]]) -> Optional[int]:
    """The index past the atoms from *at* that spell *units*, or None."""
    pos = at
    for index, (noun, key) in enumerate(units):
        if pos >= len(atoms) or (index and atoms[pos].link == "break") or atoms[pos].noun != noun:
            return None
        if not noun:
            if atoms[pos].key != key:
                return None
            pos += 1
            continue
        spelled = atoms[pos].key
        pos += 1
        while len(spelled) < len(key) and pos < len(atoms) and atoms[pos].noun and atoms[pos].link == "run":
            spelled += atoms[pos].key
            pos += 1
        if spelled != key:
            return None
    return pos


def _normal(text: str) -> str:
    return " ".join(unicodedata.normalize("NFC", text).split())


def _sources() -> list[str]:
    from hermes_cli.external_messages import enabled
    return ["agent", "messages"] if enabled() else ["agent"]


def _ranked(found: dict[str, list[str]], nearness: Optional[dict[str, float]] = None) -> list[OwnerTerm]:
    terms = sorted(found, key=lambda term: (-len(found[term]), (nearness or {}).get(term, 0.0)))
    return [OwnerTerm(term, len(found[term]), tuple(found[term][:UIDS_PER_TERM])) for term in terms[:MAX_TERMS]]


def owner_terms(store: Any, concept: str) -> list[OwnerTerm]:
    """The terms the owner used for *concept* in the messages *store* (this agent's ``MessageStore``) may read, most
    used first. Empty when he never named it: then describe it in plain words (``HINT``)."""
    concept = _normal(concept)
    if not concept:
        raise ValueError("owner_terms needs a concept")
    if _LATIN.search(concept) and not _HANGUL.search(concept):
        return _english(store, concept)
    units = _concept_units(concept)
    if not units:
        return []
    pieces = [atom.key for atom in _analyse(concept) if atom.noun]
    found: dict[str, list[str]] = {}
    for uid, text in store.owner_messages(pieces, sources=_sources(), limit=SCAN_ROWS):
        atoms = _analyse(text)
        for at in range(len(atoms)):
            end = _match(atoms, at, units)
            if end is None:
                continue
            last = atoms[end - 1]
            term = _normal(text[atoms[at].start:last.end] if last.noun else text[atoms[at].start:last.start] + last.key)
            if uid not in found.setdefault(term, []):
                found[term].append(uid)
    return _ranked(found)


def _noun_runs(atoms: Sequence[_Atom], *, spaced: bool = True) -> list[tuple[int, int, int]]:
    """``(start, end, nouns)`` of each run of nouns; with ``spaced=False`` a space ends a run."""
    runs: list[list[int]] = []
    previous: Optional[_Atom] = None
    for atom in atoms:
        if atom.noun:
            if previous is not None and previous.noun and atom.link == "run" and (spaced or atom.start == previous.end):
                runs[-1][1:] = [atom.end, runs[-1][2] + 1]
            else:
                runs.append([atom.start, atom.end, 1])
        previous = atom
    return [(start, end, nouns) for start, end, nouns in runs]


def _english(store: Any, concept: str) -> list[OwnerTerm]:
    words = concept.split()
    pattern = re.compile(r"(?<![A-Za-z])" + r"\s+".join(map(re.escape, words)) + r"(?![A-Za-z])", re.IGNORECASE)
    found: dict[str, list[str]] = {}
    distance: dict[str, list[int]] = {}
    for uid, text in store.owner_messages(words, sources=_sources(), limit=SCAN_ROWS):
        mentions = [m.start() for m in pattern.finditer(text)]
        if not mentions:
            continue
        for start, end, _nouns in _noun_runs(_analyse(text)):
            term = _normal(text[start:end])
            if not _HANGUL.search(term) or uid in found.setdefault(term, []):
                continue
            found[term].append(uid)
            distance.setdefault(term, []).append(min(abs(start - at) for at in mentions))
    return _ranked(found, {term: sum(d) / len(d) for term, d in distance.items()})


def preferred_term(terms: Sequence[OwnerTerm]) -> Optional[str]:
    """The word a reply uses for the concept: the owner's most used term, or None (then describe it, ``HINT``)."""
    return terms[0].term if terms else None


def lookup(store: Any, concept: str) -> dict[str, Any]:
    """``owner_terms`` as the tool answers it: the terms, the one to use, and the hint when there is none."""
    terms = owner_terms(store, concept)
    found: dict[str, Any] = {"concept": _normal(concept), "terms": [term.as_dict() for term in terms],
                             "use": preferred_term(terms)}
    return found if terms else {**found, "hint": HINT}


def compounds(text: str) -> list[str]:
    """The compound nouns of *text*: two or more nouns written as one word (취향칸), or one noun Kiwi does not
    know."""
    normal = _normal(text)
    tokens = analyzer().tokenize(normal)
    unknown = {(t.start, t.end) for t in tokens if _tag(t) in _NOUN_TAGS and t.oov}
    return [normal[start:end] for start, end, nouns in _noun_runs(_atoms(tokens), spaced=False)
            if nouns > 1 or (start, end) in unknown]


@functools.cache
def reference_words() -> frozenset[str]:
    """The nouns of Kiwi's built-in dictionary: its model's morpheme list, built from the Sejong corpus."""
    kiwi = analyzer()
    words: set[str] = set()
    index = 0
    while True:
        try:
            morpheme = kiwi.morpheme(index)
        except ValueError:  # past the last morpheme
            return frozenset(words)
        if _tag(morpheme) in _NOUN_TAGS:
            words.add(morpheme.form)
        index += 1


def is_coined(compound: str, owner_terms: Iterable[str], reference_words: Iterable[str]) -> bool:
    """Whether *compound* is neither one of *owner_terms* (his spellings, spaces ignored) nor in *reference_words*."""
    key = "".join(_normal(compound).split())
    if not key:
        return False
    return key not in {"".join(_normal(term).split()) for term in owner_terms} and key not in set(reference_words)
