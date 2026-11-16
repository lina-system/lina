"""Ledger goals as generated brain ``project`` pages (lina#438): ``lina brain projects sync`` and ``show``.

The work ledger (``lina work``) stays the only authority for status, deadlines and task state. A project page is
generated from a goal: slug ``goal-<id>``, the goal's title, its why and done text, and its task list. Where the
page would say a status or a deadline it holds a placeholder (``{{ledger:G1:status}}``) that ``show`` resolves
from the ledger's own state views (``v_goal``, ``v_todo``) at read time, so a status change never edits a page and
the index never holds a copy that could go stale.

Pages are files under ``projects/`` of the brain work folder (``brain.work_dir`` in the root ``config.yaml``,
default ``<LINA root>/brain``), indexed as one more brain root with the importer's own scanner, so ``lina brain
import`` of that folder agrees with them. The folder may not lie inside an imported wiki or hold one. Links run
both ways: from the project page to every page the goal's text names (``[[links]]`` and plain names, #443
matching) and back, stored as ``mentions`` edges whose ``source_ref`` is ``ledger:<goal id>``; no wiki file is
written. A goal gone from the ledger keeps its page and links, marked ``archived: true``.
"""

from __future__ import annotations

import argparse
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path

from hermes_cli.brain import links
from hermes_cli.brain.page import alias_key, new_page, nfc, parse, render, slugify
from hermes_cli.brain.resolver import DEFAULT_MAPPING, mapping_from
from hermes_cli.brain.scan import _Resolver, scan
from hermes_cli.brain.store import (
    LEDGER_REF,
    BrainStoreError,
    dumps,
    owner_connection,
    root_of,
    snapshot,
)
from hermes_cli.brain.store import (
    sync as sync_index,
)

FOLDER = "projects"
SLUG_PREFIX = "goal-"
GONE = "gone from the ledger"
_PLACEHOLDER = re.compile(r"\{\{ledger:([^:{}\s]+):(status|deadline)\}\}")


@dataclass(frozen=True)
class Task:
    id: str
    title: str
    why: str = ""
    done: str = ""


@dataclass(frozen=True)
class Goal:
    id: str
    title: str
    why: str = ""
    done: str = ""
    tasks: tuple[Task, ...] = ()

    @property
    def slug(self) -> str:
        return slugify(SLUG_PREFIX + self.id)

    def text(self) -> str:
        """What the goal says in its own words: the text its links are read from."""
        parts = [self.title, self.why, self.done]
        for task in self.tasks:
            parts += [task.title, task.why, task.done]
        return "\n".join(part for part in parts if part)


@dataclass
class Ledger:
    """One read of the ledger: its goals with their tasks, and the status and deadline of every goal and task as the
    ledger's state views give them now."""
    goals: dict[str, Goal] = field(default_factory=dict)
    status: dict[str, str] = field(default_factory=dict)
    deadline: dict[str, str | None] = field(default_factory=dict)


def _line(value) -> str:
    return " ".join(str(value or "").split())


def read_ledger(root: Path) -> Ledger:
    """The ledger of the LINA root *root*, read through the work record's own reader and views."""
    from hermes_cli.work import env, ledger
    from hermes_cli.work.kinds import TODO, kind_now
    env.bind(env.load(root))
    try:
        recs, order = ledger.read_all()
        db = ledger.db()
        found = Ledger()
        for view in ("v_goal", "v_todo"):
            for item, status, due in db.execute(f"SELECT id, status, due_date FROM {view}").fetchall():
                found.status[item] = status
                found.deadline[item] = str(due) if due not in (None, "") else None
    finally:
        ledger.close()
        env.reset()
    tasks: dict[str, list[Task]] = {}
    for item in order:
        r = recs[item]
        if kind_now(r.get("kind")) == TODO and r.get("parent"):
            tasks.setdefault(r["parent"], []).append(
                Task(item, _line(r.get("title")) or item, _line(r.get("why")), _line(r.get("done"))))
    for item in order:
        r = recs[item]
        if r.get("kind") == "목표":
            found.goals[item] = Goal(item, _line(r.get("title")) or item, _line(r.get("why")), _line(r.get("done")),
                                     tuple(tasks.get(item, ())))
    return found


def work_dir(root: Path) -> Path:
    """``brain.work_dir`` of the root config (``~`` expanded, relative to the root), else ``<root>/brain``."""
    from hermes_cli.work import env
    value = env.load(root).section("brain").get("work_dir")
    if not value:
        return root / "brain"
    path = Path(str(value)).expanduser()
    return path if path.is_absolute() else root / path


def _unlinked(text: str) -> str:
    """*text* with each ``[[link]]`` as its plain label: the page's links are edges read from the ledger, and a
    written link in the page would name a page of another root that the work folder's own lint cannot resolve."""
    return links._WIKI.sub(lambda match: match.group(1).split("|")[-1].split("#", 1)[0].strip(), text)


def render_goal(goal: Goal) -> str:
    """The page of *goal* as generated: no status, deadline or task state, only placeholders for them."""
    body = [f"Ledger goal {goal.id}. Status: {{{{ledger:{goal.id}:status}}}}. "
            f"Deadline: {{{{ledger:{goal.id}:deadline}}}}."]
    if goal.why:
        body += ["", f"Why: {_unlinked(goal.why)}"]
    if goal.done:
        body += ["", f"Done when: {_unlinked(goal.done)}"]
    body += ["", "## Tasks", ""]
    body += [f"- {task.id} {_unlinked(task.title)}: {{{{ledger:{task.id}:status}}}}"
             for task in goal.tasks] or ["- none"]
    page = new_page("project", _unlinked(goal.title), "\n".join(body), aliases=(goal.id,),
                    extra={"ledger_goal": goal.id})
    return render(page)


def _with_timeline(generated: str, existing: str | None) -> str:
    """*generated* followed by the timeline the existing file holds: a timeline is only ever appended to."""
    page = parse(existing or "")
    if not page.divider:
        return generated
    timeline = page.divider + "".join(entry.raw for entry in page.entries)
    return generated + ("" if generated.endswith("\n\n") else "\n") + timeline


def _archived(text: str) -> str:
    import hermes_yaml
    page = parse(text)
    if page.frontmatter.get("archived") is True:
        return text
    data = {**page.frontmatter, "archived": True}
    head = f"---\n{hermes_yaml.safe_dump(data, sort_keys=False)}---\n"
    return head + page.compiled + page.divider + "".join(entry.raw for entry in page.entries)


def resolve(text: str, ledger: Ledger) -> str:
    """*text* with every ledger placeholder replaced by what the ledger says now."""
    def value(match: re.Match) -> str:
        item, what = match.groups()
        if item not in ledger.status:
            return GONE
        return ledger.status[item] if what == "status" else ledger.deadline.get(item) or "none"
    return _PLACEHOLDER.sub(value, text)


def _inside(path: str, parent: str) -> bool:
    return path == parent or path.startswith(parent.rstrip("/") + "/")


def _check_work_dir(conn, work: str) -> None:
    for root_id, path in conn.execute("SELECT root_id, path FROM brain.roots ORDER BY path").fetchall():
        if path == work:
            (hand,) = conn.execute("SELECT count(*) FROM brain.pages WHERE root_id = %s "
                                   "AND NOT frontmatter ? 'ledger_goal'", (root_id,)).fetchone()
            if hand:
                raise BrainStoreError(f"the brain work folder {work} is an imported wiki ({hand} pages not generated "
                                      "from the ledger); set brain.work_dir to a folder of its own")
        elif _inside(work, path) or _inside(path, work):
            raise BrainStoreError(f"the brain work folder {work} and the imported directory {path} overlap; set "
                                  "brain.work_dir to a folder outside every imported wiki")


def _write_files(folder: Path, ledger: Ledger) -> None:
    """Each goal's page as generated; a page whose goal is gone from the ledger is marked archived. Only files that
    differ are written."""
    folder.mkdir(parents=True, exist_ok=True)
    held = {}
    for path in sorted(folder.glob("*.md")):
        goal = parse(path.read_text(encoding="utf-8")).frontmatter.get("ledger_goal")
        if isinstance(goal, str):
            held[goal] = path
    for goal_id in sorted(ledger.goals):
        goal = ledger.goals[goal_id]
        path = folder / f"{goal.slug}.md"
        existing = path.read_text(encoding="utf-8") if path.exists() else None
        wanted = _with_timeline(render_goal(goal), existing)
        if wanted != existing:
            path.write_text(wanted, encoding="utf-8")
    for goal_id, path in sorted(held.items()):
        if goal_id not in ledger.goals:
            existing = path.read_text(encoding="utf-8")
            wanted = _archived(existing)
            if wanted != existing:
                path.write_text(wanted, encoding="utf-8")


@dataclass(frozen=True)
class _PageRef:
    slug: str
    path: str


def _targets(conn, root_id: int) -> tuple[_Resolver, set[str]]:
    """The #443 resolver over every page outside the project root, and the names it knows. Project pages are left
    out, so a goal's own title never shadows a shorter page name inside it."""
    pages = [_PageRef(slug, path) for slug, path in conn.execute(
        "SELECT slug, path FROM brain.pages WHERE root_id <> %s ORDER BY slug", (root_id,)).fetchall()]
    keys: dict[str, int] = {}
    for page in pages:
        keys[alias_key(page.path[:-3])] = keys.get(alias_key(page.path[:-3]), 0) + 1
    unique = [page for page in pages if keys[alias_key(page.path[:-3])] == 1]
    aliases = {tuple(row) for row in conn.execute(
        "SELECT a.alias_key, a.slug, a.alias, a.origin FROM brain.aliases AS a JOIN brain.pages AS p USING (slug) "
        "WHERE p.root_id <> %s", (root_id,)).fetchall()}
    return _Resolver(unique, aliases), {alias for _, _, alias, _ in aliases}


def _named(goal: Goal, resolver: _Resolver, names: set[str]) -> set[str]:
    text = goal.text()
    found = set()
    for link in [*links.extract(text, ""), *links.mentions(text, names)]:
        slug, _ = resolver.resolve(link)
        if slug:
            found.add(slug)
    return found


def _sync_edges(conn, root_id: int, live: dict[str, Goal]) -> tuple[set, set]:
    """Make the ledger edges of every live goal equal to the links its text names now; (added, removed)."""
    resolver, names = _targets(conn, root_id)
    desired = set()
    for goal in live.values():
        ref = LEDGER_REF + goal.id
        for slug in _named(goal, resolver, names):
            desired |= {(goal.slug, slug, "mentions", ref), (slug, goal.slug, "mentions", ref)}
    refs = sorted(LEDGER_REF + goal_id for goal_id in live)
    held = {tuple(row) for row in conn.execute(
        "SELECT from_slug, to_slug, type, source_ref FROM brain.edges WHERE source_ref = ANY(%s)", (refs,))}
    added, removed = desired - held, held - desired
    with conn.cursor() as cur:
        cur.executemany("DELETE FROM brain.edges WHERE from_slug = %s AND to_slug = %s AND type = %s "
                        "AND source_ref = %s", sorted(removed))
        cur.executemany("INSERT INTO brain.edges (from_slug, to_slug, type, source_ref) VALUES (%s, %s, %s, %s)",
                        sorted(added))
    return added, removed


def sync(conn, root: Path, ledger: Ledger) -> dict:
    """Write the project pages of *ledger*'s goals, index them and their links both ways; the report says what
    changed. Deterministic and idempotent: an unchanged ledger changes nothing."""
    folder_root = Path(work_dir(root))
    work = nfc(str(folder_root.resolve()))
    _check_work_dir(conn, work)
    _write_files(folder_root / FOLDER, ledger)
    known = root_of(conn, work)
    before = snapshot(conn, known[0])["pages"] if known else {}
    index = scan(folder_root, known[1] if known else mapping_from(DEFAULT_MAPPING))
    with conn.transaction():
        sync_index(conn, index)
        root_id = root_of(conn, work)[0]
        live = {goal.id: goal for goal in ledger.goals.values() if goal.slug in index.pages}
        added, removed = _sync_edges(conn, root_id, live)
    created, updated, archived = [], [], []
    for slug in sorted(index.pages):
        row = index.pages[slug].row()
        if slug not in before:
            created.append(slug)
        elif row[6].get("archived") is True and before[slug][6].get("archived") is not True:
            archived.append(slug)
        elif before[slug] != row:
            updated.append(slug)
    pages = {slug for slug, record in index.pages.items() if "ledger_goal" in record.frontmatter}
    return {
        "work_dir": work,
        "goals": len(ledger.goals),
        "pages": len(pages),
        "pages_created": created,
        "pages_updated": updated,
        "pages_archived": archived,
        "links_added_from_goals": sum(edge[0] in pages for edge in added),
        "links_added_to_goals": sum(edge[1] in pages for edge in added),
        "links_removed": len(removed),
        "skipped_files": [{"path": rel, "reason": reason} for rel, reason in sorted(index.skipped)],
    }


def show(conn, key: str, ledger: Ledger) -> dict:
    """A project page as read now: its text with the ledger's current status, deadline and task state filled in,
    and its links in both directions."""
    row = conn.execute(
        "SELECT slug, title, frontmatter, compiled_truth FROM brain.pages WHERE type = 'project' "
        "AND frontmatter ? 'ledger_goal' AND (slug = %s OR frontmatter->>'ledger_goal' = %s) ORDER BY slug LIMIT 1",
        (nfc(key).lower(), key.strip().upper())).fetchone()
    if row is None:
        raise BrainStoreError(f"no project page for {key}; run `lina brain projects sync`")
    slug, title, frontmatter, compiled = row
    goal_id = frontmatter["ledger_goal"]

    def edges(sql: str) -> list[dict]:
        return [{"slug": other, "source_ref": ref} for other, ref in conn.execute(sql, (slug,)).fetchall()]
    tasks = [item for item, what in _PLACEHOLDER.findall(compiled) if what == "status" and item != goal_id]
    return {
        "slug": slug,
        "goal": goal_id,
        "title": title,
        "archived": frontmatter.get("archived") is True,
        "status": ledger.status.get(goal_id, GONE),
        "deadline": ledger.deadline.get(goal_id) if goal_id in ledger.status else GONE,
        "tasks": [{"id": item, "status": ledger.status.get(item, GONE)} for item in tasks],
        "text": resolve(compiled, ledger).strip("\n"),
        "links_to": edges("SELECT to_slug, source_ref FROM brain.edges WHERE from_slug = %s ORDER BY 1, 2"),
        "linked_from": edges("SELECT from_slug, source_ref FROM brain.edges WHERE to_slug = %s ORDER BY 1, 2"),
    }


def _root() -> Path:
    from hermes_cli.lina_deps import lina_root
    return lina_root()


def run_sync(args: argparse.Namespace) -> int:
    root = _root()
    ledger = read_ledger(root)
    with owner_connection() as conn:
        report = sync(conn, root, ledger)
    if args.json:
        print(dumps(report))
        return 0
    changed = report["pages_created"] or report["pages_updated"] or report["pages_archived"] or any(
        report[key] for key in ("links_added_from_goals", "links_added_to_goals", "links_removed"))
    print(f"Projects {report['work_dir']}: {report['goals']} goals, {report['pages']} pages.")
    print(f"Pages: {len(report['pages_created'])} created, {len(report['pages_updated'])} updated, "
          f"{len(report['pages_archived'])} archived (goal gone from the ledger).")
    print(f"Links added: {report['links_added_from_goals']} from goals to pages, {report['links_added_to_goals']} "
          f"back to goals; {report['links_removed']} removed.")
    for skipped in report["skipped_files"]:
        print(f"{skipped['path']}: skipped: {skipped['reason']}", file=sys.stderr)
    print("Changes: none." if not changed else "Changes: see above.")
    return 0


def run_show(args: argparse.Namespace) -> int:
    root = _root()
    ledger = read_ledger(root)
    with owner_connection() as conn:
        page = show(conn, args.goal, ledger)
    if args.json:
        print(dumps(page))
        return 0
    print(f"{page['slug']}: {page['title']} (ledger goal {page['goal']}{', archived' if page['archived'] else ''})")
    print(page["text"])
    print("Links to: " + (", ".join(edge["slug"] for edge in page["links_to"]) or "none"))
    print("Linked from: " + (", ".join(edge["slug"] for edge in page["linked_from"]) or "none"))
    return 0
