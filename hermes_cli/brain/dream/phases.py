"""The model phases of the cycle, after GBrain's ``runCycle`` (lina#438): atoms, patterns, concept synthesis,
consolidate, contradictions and drift; and promote (lina#441), which proposes ``external`` timeline lines for
compiled truth once the owner or a second independent source confirms them.

A phase reads the brain as it is in the store (``load``), turns it into requests (one prompt each, with a fixed
answer shape) and reads each answer into writes. Only ``atoms`` writes index rows (``brain.atoms``, short claims
derived from a page). Every other phase writes proposals: a new page, an addition to compiled truth, a merge, a
contradiction to resolve, a relation to close. Nothing here edits a page, a file or an edge.

A proposal carries the lowest grade of the material it is built from (lina#440): a page's compiled truth is of
unknown origin and so ``external``; a timeline entry and an atom carry their own grade.
"""

from __future__ import annotations

import re
from collections import defaultdict
from dataclasses import dataclass, field
from datetime import date, datetime, timedelta
from typing import Optional

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
    from hermes_state_source_visibility import visible_atom, visible_ref

    visible = visible_ref("source_ref")
    pages = {row[0]: PageRow(*row) for row in conn.execute(
        "SELECT slug, path, title, type, dated, compiled_truth FROM brain.pages ORDER BY slug")}
    timeline: dict[str, list[Item]] = defaultdict(list)
    for slug, path, line, day, grade, ref, entry in conn.execute(
            "SELECT page_slug, path, line, entry_date, grade, source_ref, text FROM brain.timeline "
            f"WHERE {visible} "
            "ORDER BY page_slug, entry_date NULLS FIRST, line"):
        timeline[slug].append(Item(ref or f"{path}#L{line}", grade, day.isoformat() if day else None, entry))
    atoms = [Atom(*row) for row in conn.execute(
        "SELECT atom_id, page_slug, claim, grade, source_ref, created_at FROM brain.atoms "
        f"WHERE {visible_atom('source_ref')} "
        "ORDER BY atom_id")]
    open_edges = conn.execute(
        "SELECT from_slug, to_slug, type, source_ref, valid_from FROM brain.edges "
        f"WHERE type <> 'mentions' AND valid_until IS NULL AND {visible} "
        "ORDER BY from_slug, type, to_slug, source_ref").fetchall()
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

    @classmethod
    def loaded(cls, conn, settings: Settings, agent_id: str) -> Phase:
        return cls(load(conn), settings)

    def requests(self) -> list[Request]:
        raise NotImplementedError

    def read(self, request: Request, answer: dict) -> list:
        raise NotImplementedError

    def finish(self) -> list:
        return []

    def remember(self, request: Request) -> Optional[object]:
        """What a later night needs to skip *request* again (JSON), right after its answer was read; None if nothing."""
        return None

    def restore(self, request: Request, state: object) -> None:
        """Take back what :meth:`remember` returned, for a request this night skips as already answered."""


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
    taken: dict = field(default_factory=dict)

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
        self.taken[request.subject] = [[name, [atom.atom_id for atom in atoms]] for name, atoms in found]
        self._group(request, self.taken[request.subject])
        return []

    def _group(self, request: Request, groups: list) -> None:
        by_id = {atom.atom_id: atom for atom in request.context}
        for name, ids in groups:
            members = {atom_id: by_id[atom_id] for atom_id in ids if atom_id in by_id}
            self.groups.setdefault(alias_key(name), (name, {}))[1].update(members)

    def remember(self, request: Request) -> Optional[object]:
        return self.taken.get(request.subject)

    def restore(self, request: Request, state: object) -> None:
        self._group(request, state)

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


@dataclass(frozen=True)
class Evidence:
    """What may confirm an external line: an owner message (``owner``) or an external line of another source
    (``external``), with the source family and raw text that decide whether it is independent."""
    item: Item
    family: str | None
    raw: str | None


def family(ref: str, conversations: dict[str, str]) -> str:
    """The source a reference belongs to: a stored external message's conversation (one thread is one source), else
    the reference up to its last ``:`` (``mail:4711`` is of ``mail``)."""
    if ref in conversations:
        return f"conversation:{conversations[ref]}"
    return ref.rsplit(":", 1)[0] if ":" in ref else ref


@dataclass
class PromotePhase(Phase):
    """Per page with ``external`` lines that cite a source: ask which of them an owner message (speaker and grade
    ``owner``, of this agent, naming the page) or an external line of another, independent source states again. Each
    confirmed line becomes a ``promote`` proposal; nothing is promoted until a person accepts it."""
    name = "promote"
    TASK = ("Items of grade external are facts read from outside sources; items of grade owner are the owner's own "
            "messages. For each external item that an owner item, or an external item from another source, states "
            "again (the same person and the same fact), cite it and the items that state it again.")
    SHAPE = ('{"promote": [{"item": <number of an external item>, "confirmed_by": [<item numbers>]}]}, '
             'at most 5 entries')
    OWNER_MESSAGES = 10
    OWNER_CHARS = 600
    evidence: dict = field(default_factory=dict)

    @classmethod
    def loaded(cls, conn, settings: Settings, agent_id: str) -> Phase:
        phase = cls(load(conn), settings)
        lines: dict[str, list[tuple]] = defaultdict(list)
        from hermes_state_source_visibility import visible_ref

        for slug, day, ref, entry in conn.execute(
                "SELECT page_slug, entry_date, source_ref, text FROM brain.timeline WHERE grade = 'external' "
                f"AND {visible_ref('source_ref')} "
                "AND source_ref IS NOT NULL AND source_ref <> '' ORDER BY page_slug, entry_date NULLS FIRST, line"):
            lines[slug].append((day, ref, entry))
        refs = sorted({ref for found in lines.values() for _, ref, _ in found})
        raw = {ref: (conversation, content) for ref, conversation, content in conn.execute(
            "SELECT 'msg:' || uid, conversation_id, content FROM memory.messages WHERE source = 'messages' "
            "AND 'msg:' || uid = ANY(%s)", (refs,))}
        conversations = {ref: conversation for ref, (conversation, _) in raw.items() if conversation}
        for slug, found in sorted(lines.items()):
            external = [Evidence(Item(ref, "external", day.isoformat() if day else None, entry),
                                 family(ref, conversations), (raw.get(ref) or (None, None))[1])
                        for day, ref, entry in found[-TIMELINE_ITEMS:]]
            owner = [Evidence(Item(f"msg:{uid}", "owner", ts.date().isoformat() if ts else None,
                                   content[:cls.OWNER_CHARS]), None, None)
                     for uid, content, ts in _owner_messages(conn, slug, agent_id, cls.OWNER_MESSAGES)]
            if external and len(external) + len(owner) >= 2:
                phase.evidence[slug] = (*external, *owner)
        return phase

    def requests(self) -> list[Request]:
        return [Request(f"page:{slug}", self.TASK, self.SHAPE, tuple(entry.item for entry in found), ("page", slug))
                for slug, found in sorted(self.evidence.items())]

    def read(self, request: Request, answer: dict) -> list:
        from hermes_cli.brain.extract import copies
        slug = request.context[1]
        found = self.evidence[slug]
        proposals = []
        for entry in field_list(answer, "promote", 5):
            picked = entry.get("item")
            if (not isinstance(picked, int) or isinstance(picked, bool) or not 1 <= picked <= len(found)
                    or found[picked - 1].item.grade != "external"):
                raise Invalid("item must be the number of an external item")
            line = found[picked - 1]
            confirming = numbers(entry, "confirmed_by", len(found))
            if picked in confirming:
                raise Invalid("an item cannot confirm itself")
            basis = set()
            for n in confirming:
                other = found[n - 1]
                if other.item.grade == "owner":
                    basis.add("owner")
                elif other.family == line.family or (other.raw and line.raw and (
                        other.raw == line.raw or copies(other.raw, line.raw))):
                    raise Invalid("a confirming external item must come from another source and not copy it")
                else:
                    basis.add("second_source")
            refs = (line.item.ref, *(found[n - 1].item.ref for n in confirming))
            proposals.append(Proposal("promote", slug, {
                "line": {"date": line.item.date, "source_ref": line.item.ref, "text": line.item.text},
                "add": line.item.text, "basis": sorted(basis), "confirmed_by": list(refs[1:]),
            }, lowest(found[n - 1].item.grade for n in (picked, *confirming)), refs,
                fingerprint(self.name, "promote", slug, line.item.ref)))
        return proposals


def _owner_messages(conn, slug: str, agent_id: str, most: int) -> list[tuple]:
    """The newest owner messages of *agent_id* that name page *slug* by one of its names (two characters or more)."""
    names = [row[0] for row in conn.execute("SELECT alias FROM brain.aliases WHERE slug = %s", (slug,))
             if len(row[0].strip()) >= 2]
    if not names:
        return []
    patterns = ["%" + re.sub(r"([\\%_])", r"\\\1", name.strip()) + "%" for name in names]
    # An edited message keeps its earlier versions under the same base uid (``uid#digest``): the newest version of
    # each is chosen first, and only then do the name match, the forgotten flag and the limit apply.
    return conn.execute(
        "SELECT uid, content, ts FROM ("
        " SELECT DISTINCT ON (split_part(uid, '#', 1)) message_id, uid, content, ts, forgotten_at"
        " FROM memory.messages WHERE agent_id = %s AND speaker = 'owner' AND grade = 'owner'"
        " ORDER BY split_part(uid, '#', 1), message_id DESC"
        ") newest WHERE forgotten_at IS NULL AND content ILIKE ANY(%s)"
        " ORDER BY ts DESC NULLS LAST, message_id DESC LIMIT %s",
        (agent_id, patterns, most)).fetchall()


PHASE_CLASSES = {phase.name: phase for phase in (AtomsPhase, PatternsPhase, ConceptsPhase, ConsolidatePhase,
                                                 ContradictionsPhase, DriftPhase, PromotePhase)}
