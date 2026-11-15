"""The six model phases of the cycle, after GBrain's ``runCycle`` (lina#438): atoms, patterns, concept synthesis,
consolidate, contradictions and drift.

A phase reads the brain as it is in the store (``load``), turns it into requests (one prompt each, with a fixed
answer shape) and reads each answer into writes. Only ``atoms`` writes index rows (``brain.atoms``, short claims
derived from a page). Every other phase writes proposals: a new page, an addition to compiled truth, a merge, a
contradiction to resolve, a relation to close. Nothing here edits a page, a file or an edge.

A proposal carries the lowest grade of the material it is built from (lina#440): a page's compiled truth is of
unknown origin and so ``external``; a timeline entry and an atom carry their own grade.
"""

from __future__ import annotations

from collections import defaultdict
from dataclasses import dataclass, field
from datetime import date, datetime, timedelta

from hermes_cli.brain.dream.config import Settings
from hermes_cli.brain.dream.model import Invalid, Item, digest, field_list, numbers, prompt, text
from hermes_cli.brain.page import alias_key, slugify

TIMELINE_ITEMS = 30
PATTERN_ATOMS = 200
CONCEPT_BATCH = 100
TIERS = (10, 5, 2, 1)


@dataclass(frozen=True)
class PageRow:
    slug: str
    path: str
    title: str
    type: str
    dated: date | None
    compiled_truth: str


@dataclass(frozen=True)
class Atom:
    atom_id: int
    slug: str
    claim: str
    grade: str
    source_ref: str | None
    created_at: datetime


@dataclass
class Brain:
    pages: dict[str, PageRow]
    timeline: dict[str, list[Item]]
    atoms: list[Atom]
    open_edges: list[tuple]
    same_names: list[tuple[str, str]]
    names: set[str]
    now: datetime


@dataclass(frozen=True)
class Request:
    subject: str
    task: str
    shape: str
    items: tuple[Item, ...]
    context: tuple = ()

    @property
    def prompt(self) -> str:
        return prompt(self.task, self.shape, self.items)


@dataclass(frozen=True)
class Proposal:
    kind: str
    target: str
    payload: dict
    grade: str
    source_refs: tuple[str, ...]
    fingerprint: str


@dataclass(frozen=True)
class Atoms:
    slug: str
    claims: tuple[tuple[str, str, str], ...]


def load(conn) -> Brain:
    """The brain as the store holds it now; read once per phase, so a phase sees the atoms of the one before."""
    pages = {row[0]: PageRow(*row) for row in conn.execute(
        "SELECT slug, path, title, type, dated, compiled_truth FROM brain.pages ORDER BY slug")}
    timeline: dict[str, list[Item]] = defaultdict(list)
    for slug, path, line, day, grade, ref, entry in conn.execute(
            "SELECT page_slug, path, line, entry_date, grade, source_ref, text FROM brain.timeline "
            "ORDER BY page_slug, entry_date NULLS FIRST, line"):
        timeline[slug].append(Item(ref or f"{path}#L{line}", grade, day.isoformat() if day else None, entry))
    atoms = [Atom(*row) for row in conn.execute(
        "SELECT atom_id, page_slug, claim, grade, source_ref, created_at FROM brain.atoms ORDER BY atom_id")]
    open_edges = conn.execute(
        "SELECT from_slug, to_slug, type, source_ref, valid_from FROM brain.edges "
        "WHERE type <> 'mentions' AND valid_until IS NULL ORDER BY from_slug, type, to_slug, source_ref").fetchall()
    same_names = conn.execute(
        "SELECT DISTINCT a.slug, b.slug FROM brain.aliases AS a "
        "JOIN brain.aliases AS b ON b.alias_key = a.alias_key AND a.slug < b.slug "
        "JOIN brain.pages AS pa ON pa.slug = a.slug JOIN brain.pages AS pb ON pb.slug = b.slug "
        "WHERE pa.root_id = pb.root_id ORDER BY 1, 2").fetchall()
    names = {row[0] for row in conn.execute("SELECT alias_key FROM brain.aliases")}
    now = conn.execute("SELECT now()").fetchone()[0]
    return Brain(pages, dict(timeline), atoms, open_edges, [tuple(pair) for pair in same_names], names, now)


def lowest(grades) -> str:
    from plugins.memory.lina.grade import lowest as lowest_grade
    return lowest_grade(grades)


def page_item(page: PageRow, settings: Settings) -> Item:
    return Item(page.path, "external", page.dated.isoformat() if page.dated else None,
                f"{page.title}\n\n{page.compiled_truth[:settings.page_chars]}")


def atom_item(atom: Atom) -> Item:
    return Item(f"atom:{atom.atom_id}", atom.grade, atom.created_at.date().isoformat(), f"[{atom.slug}] {atom.claim}")


def cited(request: Request, picked: list[int]) -> tuple[str, tuple[str, ...]]:
    items = [request.items[n - 1] for n in picked]
    return lowest(item.grade for item in items), tuple(item.ref for item in items)


def fingerprint(phase: str, kind: str, target: str, key: str = "") -> str:
    return f"{phase}:{kind}:{target}:{digest(key)[:16]}"


@dataclass
class Phase:
    brain: Brain
    settings: Settings
    name = ""

    def requests(self) -> list[Request]:
        raise NotImplementedError

    def read(self, request: Request, answer: dict) -> list:
        raise NotImplementedError

    def finish(self) -> list:
        return []


class AtomsPhase(Phase):
    name = "atoms"
    TASK = ("Write one to three short, self-contained claims that these items state about the page's subject (who, "
            "what, when). Each claim cites the numbers of the items it rests on.")
    SHAPE = '{"atoms": [{"claim": "<at most 300 characters>", "items": [<item numbers>]}]}, at most 3 atoms'

    def requests(self) -> list[Request]:
        return [Request(slug, self.TASK, self.SHAPE,
                        (page_item(page, self.settings), *self.brain.timeline.get(slug, [])[-TIMELINE_ITEMS:]))
                for slug, page in self.brain.pages.items()]

    def read(self, request: Request, answer: dict) -> list:
        claims = []
        for entry in field_list(answer, "atoms", 3):
            claim = text(entry, "claim", 300)
            grade, refs = cited(request, numbers(entry, "items", len(request.items)))
            claims.append((claim, grade, " ".join(refs)))
        return [Atoms(request.subject, tuple(claims))]


class PatternsPhase(Phase):
    name = "patterns"
    TASK = ("Find themes that recur across claims of different pages. For each theme give a title and one summary "
            "paragraph, and cite the claims it rests on (from at least two different pages).")
    SHAPE = ('{"patterns": [{"title": "<at most 80 characters>", "summary": "<at most 600 characters>", '
             '"items": [<item numbers>]}]}, at most 5 patterns')

    def requests(self) -> list[Request]:
        since = self.brain.now - timedelta(days=self.settings.pattern_days)
        recent = [atom for atom in self.brain.atoms if atom.created_at >= since][-PATTERN_ATOMS:]
        if len({atom.slug for atom in recent}) < 2:
            return []
        return [Request("recent", self.TASK, self.SHAPE, tuple(map(atom_item, recent)), tuple(recent))]

    def read(self, request: Request, answer: dict) -> list:
        found = []
        for entry in field_list(answer, "patterns", 5):
            title, summary = text(entry, "title", 80), text(entry, "summary", 600)
            picked = numbers(entry, "items", len(request.items), least=2)
            atoms = [request.context[n - 1] for n in picked]
            if len({atom.slug for atom in atoms}) < 2:
                raise Invalid("a pattern must cite claims of at least two pages")
            if alias_key(title) in self.brain.names:
                continue
            grade, refs = cited(request, picked)
            target = slugify(title)
            found.append(Proposal("new_page", target, {
                "type": "concept", "title": title, "compiled_truth": summary,
                "atoms": [atom.atom_id for atom in atoms], "pages": sorted({atom.slug for atom in atoms}),
            }, grade, refs, fingerprint(self.name, "new_page", target)))
        return found


@dataclass
class ConceptsPhase(Phase):
    name = "concepts"
    TASK = ("Group the claims by the concept (a topic, method or term) each one is about. Name each concept in a "
            "few words. A claim belongs to at most one concept; leave out claims that fit none.")
    SHAPE = '{"concepts": [{"name": "<at most 80 characters>", "items": [<item numbers>]}]}, at most 30 concepts'
    groups: dict = field(default_factory=dict)

    def requests(self) -> list[Request]:
        atoms = self.brain.atoms
        batches = [atoms[start:start + CONCEPT_BATCH] for start in range(0, len(atoms), CONCEPT_BATCH)]
        return [Request(f"batch:{batch[0].atom_id}-{batch[-1].atom_id}", self.TASK, self.SHAPE,
                        tuple(map(atom_item, batch)), tuple(batch)) for batch in batches]

    def read(self, request: Request, answer: dict) -> list:
        seen: set[int] = set()
        found = []
        for entry in field_list(answer, "concepts", 30):
            name = text(entry, "name", 80)
            picked = numbers(entry, "items", len(request.items))
            if seen & set(picked):
                raise Invalid("a claim is in more than one concept")
            seen.update(picked)
            found.append((name, [request.context[n - 1] for n in picked]))
        for name, atoms in found:
            self.groups.setdefault(alias_key(name), (name, {}))[1].update({atom.atom_id: atom for atom in atoms})
        return []

    def finish(self) -> list:
        found = []
        for key, (name, atoms) in sorted(self.groups.items()):
            if len(atoms) < max(self.settings.concept_min_group, 1) or key in self.brain.names:
                continue
            members = [atoms[atom_id] for atom_id in sorted(atoms)]
            target = slugify(name)
            found.append(Proposal("new_page", target, {
                "type": "concept", "title": name, "tier": next(t for t in TIERS if len(members) >= t),
                "atoms": [atom.atom_id for atom in members], "claims": [atom.claim for atom in members],
            }, lowest(atom.grade for atom in members), tuple(f"atom:{atom.atom_id}" for atom in members),
                fingerprint(self.name, "new_page", target)))
        return found


class ConsolidatePhase(Phase):
    name = "consolidate"
    CLAIMS_TASK = ("These claims are all about one page's subject. Where several say the same thing, write the one "
                   "claim they amount to and cite them (at least two each).")
    CLAIMS_SHAPE = '{"claims": [{"claim": "<at most 300 characters>", "items": [<item numbers>]}]}, at most 3 claims'
    SAME_TASK = ("Items 1 and 2 are two pages that share a name. Say whether both are about the same person, thing "
                 "or topic, and if so which one the other should be merged into.")
    SAME_SHAPE = '{"same": true or false, "into": 1 or 2, "reason": "<at most 300 characters>"}'

    def requests(self) -> list[Request]:
        by_page: dict[str, list[Atom]] = defaultdict(list)
        for atom in self.brain.atoms:
            by_page[atom.slug].append(atom)
        settled = self.brain.now - self.settings.consolidate_min_age
        found = [Request(f"page:{slug}", self.CLAIMS_TASK, self.CLAIMS_SHAPE, tuple(map(atom_item, atoms)),
                         ("claims", slug, tuple(atoms)))
                 for slug, atoms in sorted(by_page.items())
                 if len(atoms) >= self.settings.consolidate_min_atoms and atoms[0].created_at <= settled]
        pages = self.brain.pages
        found += [Request(f"pair:{a}|{b}", self.SAME_TASK, self.SAME_SHAPE,
                          (page_item(pages[a], self.settings), page_item(pages[b], self.settings)), ("same", a, b))
                  for a, b in self.brain.same_names]
        return found

    def read(self, request: Request, answer: dict) -> list:
        if request.context[0] == "same":
            return self._same(request, answer)
        slug, atoms = request.context[1], request.context[2]
        found = []
        for entry in field_list(answer, "claims", 3):
            claim = text(entry, "claim", 300)
            picked = numbers(entry, "items", len(request.items), least=2)
            grade, refs = cited(request, picked)
            found.append(Proposal("compiled_truth", slug, {
                "add": claim, "atoms": [atoms[n - 1].atom_id for n in picked],
            }, grade, refs, fingerprint(self.name, "compiled_truth", slug, claim)))
        return found

    def _same(self, request: Request, answer: dict) -> list:
        same = answer.get("same")
        if not isinstance(same, bool):
            raise Invalid("same must be true or false")
        if not same:
            return []
        if answer.get("into") not in (1, 2) or isinstance(answer.get("into"), bool):
            raise Invalid("into must be 1 or 2")
        reason = text(answer, "reason", 300)
        pair = request.context[1:]
        into = pair[answer["into"] - 1]
        source = pair[2 - answer["into"]]
        grade, refs = cited(request, [1, 2])
        return [Proposal("merge", source, {"from": source, "into": into, "reason": reason}, grade, refs,
                         fingerprint(self.name, "merge", "|".join(pair)))]


class ContradictionsPhase(Phase):
    name = "contradictions"
    PAGE_TASK = ("These claims and dated entries are about one page's subject. Find pairs or groups that cannot all "
                 "be true at once, and say why in one sentence.")
    PAGE_SHAPE = ('{"contradictions": [{"items": [<at least two item numbers>], "note": "<at most 300 characters>"}]}, '
                  'at most 5 contradictions')
    EDGE_TASK = ("These open relations all start at one page and have one type. Find relations that cannot still "
                 "hold together with the others, and say why they should be closed.")
    EDGE_SHAPE = '{"close": [{"items": [<item number>], "reason": "<at most 300 characters>"}]}, at most 5 entries'

    def requests(self) -> list[Request]:
        found = []
        by_page: dict[str, list[Item]] = defaultdict(list)
        for atom in self.brain.atoms:
            by_page[atom.slug].append(atom_item(atom))
        for slug in self.brain.pages:
            items = by_page.get(slug, []) + [item for item in self.brain.timeline.get(slug, []) if item.date]
            if len(items) >= 2:
                found.append(Request(f"page:{slug}", self.PAGE_TASK, self.PAGE_SHAPE,
                                     tuple(items[-TIMELINE_ITEMS:]), ("page", slug)))
        groups: dict[tuple[str, str], list[tuple]] = defaultdict(list)
        for edge in self.brain.open_edges:
            groups[(edge[0], edge[2])].append(edge)
        for (start, kind), edges in sorted(groups.items()):
            if len(edges) >= 2:
                items = tuple(Item(edge[3], "external", edge[4].isoformat() if edge[4] else None,
                                   f"{edge[0]} {edge[2]} {edge[1]}") for edge in edges)
                found.append(Request(f"edges:{start}:{kind}", self.EDGE_TASK, self.EDGE_SHAPE, items,
                                     ("edges", tuple(edges))))
        return found

    def read(self, request: Request, answer: dict) -> list:
        found = []
        if request.context[0] == "edges":
            for entry in field_list(answer, "close", 5):
                picked = numbers(entry, "items", len(request.items))
                reason = text(entry, "reason", 300)
                for n in picked:
                    edge = request.context[1][n - 1]
                    found.append(Proposal("close_edge", f"{edge[0]}->{edge[1]}", {
                        "edge": {"from": edge[0], "to": edge[1], "type": edge[2], "source_ref": edge[3]},
                        "reason": reason,
                    }, *cited(request, [n]), fingerprint(self.name, "close_edge", f"{edge[0]}->{edge[1]}",
                                                         "|".join(edge[:4]))))
            return found
        slug = request.context[1]
        for entry in field_list(answer, "contradictions", 5):
            picked = numbers(entry, "items", len(request.items), least=2)
            note = text(entry, "note", 300)
            grade, refs = cited(request, picked)
            found.append(Proposal("contradiction", slug, {
                "items": list(refs), "texts": [request.items[n - 1].text for n in picked], "note": note,
            }, grade, refs, fingerprint(self.name, "contradiction", slug, "|".join(sorted(refs)))))
        return found


class DriftPhase(Phase):
    name = "drift"
    TASK = ("Item 1 is a page's summary; the other items are its dated timeline entries. Find statements in item 1 "
            "that later entries show are no longer true. Quote each statement exactly as item 1 writes it, cite "
            "the entries that show the change, and suggest the statement that holds now.")
    SHAPE = ('{"drift": [{"claim": "<exact quote from item 1>", "items": [<item numbers other than 1>], '
             '"suggested": "<at most 600 characters>"}]}, at most 3 entries')

    def requests(self) -> list[Request]:
        found = []
        for slug, page in self.brain.pages.items():
            dated = [item for item in self.brain.timeline.get(slug, []) if item.date]
            if page.compiled_truth.strip() and dated:
                found.append(Request(f"page:{slug}", self.TASK, self.SHAPE,
                                     (page_item(page, self.settings), *dated[-TIMELINE_ITEMS:]), ("page", slug)))
        return found

    def read(self, request: Request, answer: dict) -> list:
        slug = request.context[1]
        summary = " ".join(self.brain.pages[slug].compiled_truth.split())
        found = []
        for entry in field_list(answer, "drift", 3):
            claim = text(entry, "claim", 600)
            if " ".join(claim.split()) not in summary:
                raise Invalid("a drift claim must quote the page summary exactly")
            picked = numbers(entry, "items", len(request.items))
            if 1 in picked:
                raise Invalid("drift evidence must be timeline entries, not the summary")
            suggested = text(entry, "suggested", 600)
            grade, refs = cited(request, picked)
            found.append(Proposal("compiled_truth", slug, {"claim": claim, "suggested": suggested,
                                                           "evidence": list(refs)},
                                  grade, refs, fingerprint(self.name, "compiled_truth", slug, claim)))
        return found


PHASE_CLASSES = {phase.name: phase for phase in (AtomsPhase, PatternsPhase, ConceptsPhase, ConsolidatePhase,
                                                 ContradictionsPhase, DriftPhase)}
