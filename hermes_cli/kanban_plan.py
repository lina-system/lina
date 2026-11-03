"""``hermes kanban plan <file>``: lay out a goal card and its work cards from one plan file.

A main session splits a goal into work cards itself (the automatic decomposer is not used) and
writes them as one plan. This module validates the whole plan first, so a refusal writes nothing,
then creates every card and link in ONE board transaction. Each card's ``idempotency_key`` is
derived from the plan's ``key`` (``<key>`` for the goal card, ``<key>/<card key>`` for a work
card), so re-running a plan returns the ids it created the first time.

Links: every work card is a link-parent of the goal card, so the goal card waits until every work
card is done; ``after: [k]`` makes card ``k`` a link-parent of the card that names it.
"""

from __future__ import annotations

import argparse
import json
import re
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable, Optional

from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc

_KEY_RE = re.compile(r"^[a-z0-9][a-z0-9._-]{0,79}$")
_ISSUE_RE = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#[0-9]+$")
_CHECK_RE = re.compile(r"^\[ \] \S")
_LEDGER_GOAL_RE = re.compile(r"^G[0-9]+$")
_LEDGER_TODO_RE = re.compile(r"^T[0-9]+$")
_NEW_TODO_KEYS = ("title", "why", "done", "check")

_PLAN_KEYS = {"key", "goal", "cards", "triage", "decompose"}
_GOAL_KEYS = {"title", "assignee", "body", "ledger_goal", "goal_mode", "gates", "max_turns",
              "max_runtime", "workspace", "triage", "decompose"}
# (plan field, body heading, required)
_SECTIONS = (
    ("outcome", "Outcome", True),
    ("verification", "Verification", True),
    ("constraints", "Constraints", False),
    ("boundaries", "Boundaries", False),
    ("stop_when", "Stop when", True),
    ("worker", "Worker", True),
)
_CARD_KEYS = {"key", "title", "assignee", "todos", "ledger_todos", "gates", "max_turns",
              "max_runtime", "workspace", "after", "triage", "decompose"} | {s[0] for s in _SECTIONS}


class PlanError(ValueError):
    """The plan is refused as a whole; nothing was written."""


@dataclass
class NewTodo:
    """A record to-do the plan creates under its ledger goal (``work add 할 일``'s required fields)."""
    title: str
    why: str
    done: str
    check: str


@dataclass
class WorkCard:
    key: str
    title: str
    assignee: str
    todos: list[str]
    sections: dict[str, str]
    gates: list[Any]
    max_turns: int
    max_runtime: int
    workspace: tuple[Optional[str], Optional[str]] = (None, None)
    after: list[str] = field(default_factory=list)
    ledger_todos: list[str | NewTodo] = field(default_factory=list)  # "T<n>" ids and new to-dos


@dataclass
class GoalCard:
    title: str
    assignee: Optional[str]
    body: str
    goal_mode: bool
    gates: list[Any]
    max_turns: Optional[int]
    max_runtime: Optional[int]
    workspace: tuple[Optional[str], Optional[str]] = (None, None)
    ledger_goal: Optional[str] = None


@dataclass
class Plan:
    key: str
    goal: GoalCard
    cards: list[WorkCard]  # in creation order: every card after the cards it waits on

    def idempotency_key(self, card: Optional[WorkCard] = None) -> str:
        return self.key if card is None else f"{self.key}/{card.key}"

    def links(self) -> list[tuple[str, str]]:
        """``(parent, child)`` pairs by card key; ``"goal"`` stands for the goal card."""
        pairs = [(dep, c.key) for c in self.cards for dep in c.after]
        return pairs + [(c.key, "goal") for c in self.cards]



def load_plan_file(path: str) -> Any:
    """Raw plan data from a YAML or JSON file (JSON is YAML, but ``.json`` gets JSON errors)."""
    try:
        text = Path(path).read_text(encoding="utf-8-sig")
    except OSError as exc:
        raise PlanError(f"cannot read plan file: {exc}") from exc
    try:
        if path.lower().endswith(".json"):
            return json.loads(text)
        from hermes_yaml import safe_load
        return safe_load(text)
    except Exception as exc:
        raise PlanError(f"plan file is not valid {'JSON' if path.lower().endswith('.json') else 'YAML'}: {exc}") from exc


def _text(value: Any, where: str, name: str, *, required: bool = True) -> Optional[str]:
    if value is None or (isinstance(value, str) and not value.strip()):
        if required:
            raise PlanError(f"{where}: {name} is required")
        return None
    if not isinstance(value, (str, int, float)) or isinstance(value, bool):
        raise PlanError(f"{where}: {name} must be text")
    return str(value).strip()


def _mapping(value: Any, where: str, allowed: set[str]) -> dict:
    if not isinstance(value, dict):
        raise PlanError(f"{where} must be a mapping")
    unknown = sorted(str(k) for k in value if k not in allowed)
    if unknown:
        raise PlanError(f"{where}: unknown field(s) {', '.join(unknown)}")
    if value.get("triage") or value.get("decompose"):
        raise PlanError(f"{where} is marked for triage/decomposition; a plan lays out its cards "
                        "itself and the automatic decomposer is not used")
    return value


def _list(value: Any, where: str, name: str) -> list:
    if value is None:
        return []
    if not isinstance(value, list):
        raise PlanError(f"{where}: {name} must be a list")
    return value


def _positive_int(value: Any, where: str, name: str, *, required: bool) -> Optional[int]:
    if value is None:
        if required:
            raise PlanError(f"{where}: {name} is required")
        return None
    if isinstance(value, bool) or not isinstance(value, int) or value < 1:
        raise PlanError(f"{where}: {name} must be a positive integer")
    return value


def _duration(value: Any, where: str, *, required: bool) -> Optional[int]:
    from hermes_cli.kanban import _parse_duration
    if value is None:
        if required:
            raise PlanError(f"{where}: max_runtime is required")
        return None
    try:
        seconds = _parse_duration(value)
    except ValueError as exc:
        raise PlanError(f"{where}: max_runtime: {exc}") from exc
    if not seconds or seconds < 1:
        raise PlanError(f"{where}: max_runtime must be positive")
    return seconds


def _workspace(value: Any, where: str) -> tuple[Optional[str], Optional[str]]:
    from hermes_cli.kanban import _parse_workspace_flag
    if value is None:
        return (None, None)
    try:
        return _parse_workspace_flag(_text(value, where, "workspace"))
    except argparse.ArgumentTypeError as exc:
        raise PlanError(f"{where}: {exc}".replace("--workspace", "workspace")) from exc


def _gates(value: Any, where: str, *, required: bool) -> list[Any]:
    from hermes_cli.kanban_goal_gates import normalize_gates
    gates = _list(value, where, "gates")
    if required and not gates:
        raise PlanError(f"{where} has no gate; every work card needs at least one deterministic gate")
    try:
        normalize_gates(gates, goal_mode=True)
    except (ValueError, TypeError) as exc:
        raise PlanError(f"{where}: {exc}") from exc
    return gates


def _assignee(value: Any, where: str, is_profile: Optional[Callable[[str], bool]], *,
              required: bool) -> Optional[str]:
    name = _text(value, where, "assignee", required=required)
    if name is None:
        return None
    if is_profile is not None and not is_profile(name):
        raise PlanError(f"{where}: assignee {name!r} is not a Hermes profile "
                        "(see `hermes kanban assignees`)")
    return name


def _todo_identity(todo: str) -> str:
    """What makes two to-dos the same: an issue ref ignoring case, a checklist item ignoring
    case and spacing."""
    return " ".join(todo.lower().split())


def _todos(value: Any, where: str) -> list[str]:
    todos = []
    for item in _list(value, where, "todos"):
        todo = _text(item, where, "to-do")
        todo = todo[2:].strip() if todo.startswith("- ") else todo
        if not (_ISSUE_RE.match(todo) or _CHECK_RE.match(todo)):
            raise PlanError(f"{where}: to-do {todo!r} must be `owner/repo#n` or `[ ] text`")
        todos.append(todo)
    if not todos:
        raise PlanError(f"{where}: todos needs at least one `owner/repo#n` or `[ ] text`")
    return todos


def _ledger_todo(item: Any, where: str) -> str | NewTodo:
    if isinstance(item, dict):
        unknown = sorted(str(k) for k in item if k not in _NEW_TODO_KEYS)
        if unknown:
            raise PlanError(f"{where}: ledger_todos: unknown field(s) {', '.join(unknown)}")
        return NewTodo(*(_text(item.get(k), where, f"ledger_todos {k}") for k in _NEW_TODO_KEYS))
    todo_id = (_text(item, where, "ledger_todos entry") or "").upper()
    if not _LEDGER_TODO_RE.match(todo_id):
        raise PlanError(f"{where}: ledger_todos entry {item!r} must be a to-do id `T<n>` or a mapping "
                        "{title, why, done, check}")
    return todo_id


def _parse_card(raw: Any, index: int, is_profile) -> WorkCard:
    where = f"card #{index}"
    if isinstance(raw, dict) and isinstance(raw.get("key"), str):
        where = f"card {raw['key']!r}"
    raw = _mapping(raw, where, _CARD_KEYS)
    key = _text(raw.get("key"), where, "key")
    if key == "goal":
        raise PlanError(f"{where}: key 'goal' is reserved for the goal card")
    if not _KEY_RE.match(key):
        raise PlanError(f"{where}: key must be lowercase letters, digits, '.', '_' or '-'")
    sections = {}
    for name, heading, required in _SECTIONS:
        sections[heading] = _text(raw.get(name), where, name, required=required) or "None."
    return WorkCard(
        key=key,
        title=_text(raw.get("title"), where, "title"),
        assignee=_assignee(raw.get("assignee"), where, is_profile, required=True),
        todos=_todos(raw.get("todos"), where),
        sections=sections,
        gates=_gates(raw.get("gates"), where, required=True),
        max_turns=_positive_int(raw.get("max_turns"), where, "max_turns", required=True),
        max_runtime=_duration(raw.get("max_runtime"), where, required=True),
        workspace=_workspace(raw.get("workspace"), where),
        after=[_text(a, where, "after") for a in _list(raw.get("after"), where, "after")],
        ledger_todos=[_ledger_todo(item, where)
                      for item in _list(raw.get("ledger_todos"), where, "ledger_todos")],
    )


def _parse_goal(raw: Any, plan_key: str, is_profile) -> GoalCard:
    where = "goal"
    raw = _mapping(raw, where, _GOAL_KEYS)
    goal_mode = bool(raw.get("goal_mode", False))
    gates = _gates(raw.get("gates"), where, required=False) if goal_mode else []
    if raw.get("gates") and not goal_mode:
        raise PlanError("goal: gates only run on a goal-mode card; set goal_mode: true")
    body = _text(raw.get("body"), where, "body", required=False) or ""
    ledger = _text(raw.get("ledger_goal"), where, "ledger_goal", required=False)
    ledger = ledger.upper() if ledger else None
    if ledger and not _LEDGER_GOAL_RE.match(ledger):
        raise PlanError(f"goal: ledger_goal {ledger!r} must be a goal id `G<n>`")
    trailer = [f"Plan: {plan_key}"] + ([f"Ledger goal: {ledger}"] if ledger else [])
    return GoalCard(
        title=_text(raw.get("title"), where, "title"),
        assignee=_assignee(raw.get("assignee"), where, is_profile, required=False),
        body="\n\n".join(p for p in (body, "\n".join(trailer)) if p),
        goal_mode=goal_mode,
        gates=gates,
        max_turns=_positive_int(raw.get("max_turns"), where, "max_turns", required=False),
        max_runtime=_duration(raw.get("max_runtime"), where, required=False),
        workspace=_workspace(raw.get("workspace"), where),
        ledger_goal=ledger,
    )


def _check_unique(cards: list[WorkCard]) -> None:
    seen_keys: set[str] = set()
    owner: dict[str, str] = {}
    for card in cards:
        if card.key in seen_keys:
            raise PlanError(f"card {card.key!r} appears twice in the plan")
        seen_keys.add(card.key)
        for todo in card.todos:
            ident = _todo_identity(todo)
            if ident in owner:
                raise PlanError(f"card {card.key!r}: to-do {todo!r} is already in card "
                                f"{owner[ident]!r}; a to-do belongs to one card")
            owner[ident] = card.key
        for todo in card.ledger_todos:
            if not isinstance(todo, str):
                continue
            if todo in owner:
                raise PlanError(f"card {card.key!r}: ledger to-do {todo} is already in card "
                                f"{owner[todo]!r}; a to-do belongs to one card")
            owner[todo] = card.key


def _ordered(cards: list[WorkCard]) -> list[WorkCard]:
    """Cards with every card after the cards it waits on; refuses unknown ``after`` and cycles."""
    by_key = {c.key: c for c in cards}
    for card in cards:
        for dep in card.after:
            if dep == card.key:
                raise PlanError(f"card {card.key!r}: after names the card itself")
            if dep not in by_key:
                raise PlanError(f"card {card.key!r}: after names unknown card {dep!r}")
    ordered: list[WorkCard] = []
    state: dict[str, str] = {}

    def visit(card: WorkCard, path: list[str]) -> None:
        if state.get(card.key) == "done":
            return
        if state.get(card.key) == "open":
            cycle = " -> ".join(path[path.index(card.key):] + [card.key])
            raise PlanError(f"card {card.key!r}: after makes a cycle ({cycle})")
        state[card.key] = "open"
        for dep in card.after:
            visit(by_key[dep], path + [card.key])
        state[card.key] = "done"
        ordered.append(card)

    for card in cards:
        visit(card, [])
    return ordered


def parse_plan(data: Any, *, is_profile: Optional[Callable[[str], bool]] = None) -> Plan:
    """Validate raw plan data into a :class:`Plan`; raises :class:`PlanError` naming the card."""
    data = _mapping(data, "plan", _PLAN_KEYS)
    key = _text(data.get("key"), "plan", "key")
    if not _KEY_RE.match(key):
        raise PlanError("plan: key must be lowercase letters, digits, '.', '_' or '-'")
    goal = _parse_goal(data.get("goal"), key, is_profile)
    raw_cards = _list(data.get("cards"), "plan", "cards")
    if not raw_cards:
        raise PlanError("plan: cards needs at least one work card")
    cards = [_parse_card(raw, i, is_profile) for i, raw in enumerate(raw_cards, start=1)]
    _check_unique(cards)
    if not goal.ledger_goal and any(c.ledger_todos for c in cards):
        with_todos = next(c.key for c in cards if c.ledger_todos)
        raise PlanError(f"card {with_todos!r}: ledger_todos needs the goal's ledger_goal (the record goal "
                        "the to-dos belong to)")
    return Plan(key=key, goal=goal, cards=_ordered(cards))


def profile_check() -> Optional[Callable[[str], bool]]:
    """The dispatcher's own "is this assignee a spawnable profile" predicate."""
    from hermes_cli.kanban_db_dispatch import _profile_exists_fn
    return _profile_exists_fn()



def work_card_body(card: WorkCard, goal_id: str, goal_title: str) -> str:
    """The work card body: Goal, To-dos, then the ``/goal`` completion-contract fields."""
    parts = [f"## Goal\n\n{goal_id} — {goal_title}",
             "## To-dos\n\n" + "\n".join(f"- {t}" for t in card.todos)]
    parts += [f"## {heading}\n\n{card.sections[heading]}" for _, heading, _ in _SECTIONS]
    return "\n\n".join(parts) + "\n"



def _existing(conn, plan: Plan) -> dict[str, str]:
    """``{idempotency_key: task id}`` for this plan's cards already on the board."""
    prefix = plan.idempotency_key() + "/"
    rows = conn.execute(
        "SELECT id, idempotency_key FROM tasks WHERE (idempotency_key = ? "
        "OR substr(idempotency_key, 1, ?) = ?) AND status != 'archived' ORDER BY created_at",
        (plan.idempotency_key(), len(prefix), prefix),
    ).fetchall()
    return {r["idempotency_key"]: r["id"] for r in rows}


def _check_already_laid_out(plan: Plan, existing: dict[str, str]) -> bool:
    """True when the whole plan is already on the board; refuses a partial match."""
    if not existing:
        return False
    expected = {plan.idempotency_key(), *(plan.idempotency_key(c) for c in plan.cards)}
    if set(existing) == expected:
        return True
    have = sorted(k.split("/", 1)[1] if "/" in k else "goal" for k in existing)
    raise PlanError(f"plan {plan.key!r} is partly on the board already (cards: {', '.join(have)}); "
                    "a laid-out plan cannot gain or lose cards — use a new plan key")


def apply_plan(conn, plan: Plan, *, created_by: str) -> tuple[dict[str, str], bool]:
    """Create the goal card, work cards and links in one transaction.

    Returns ``({"goal": id, <card key>: id, ...}, created)``; ``created`` is False when the plan
    was already laid out and the existing ids are returned unchanged.
    """
    with kb.write_txn(conn):
        existing = _existing(conn, plan)
        if _check_already_laid_out(plan, existing):
            ids = {"goal": existing[plan.idempotency_key()]}
            ids.update({c.key: existing[plan.idempotency_key(c)] for c in plan.cards})
            return ids, False
        goal = plan.goal
        ids = {"goal": kb.create_task(
            conn, title=goal.title, body=goal.body, assignee=goal.assignee, created_by=created_by,
            workspace_kind=goal.workspace[0], workspace_path=goal.workspace[1],
            idempotency_key=plan.idempotency_key(), goal_mode=goal.goal_mode,
            goal_max_turns=goal.max_turns, max_runtime_seconds=goal.max_runtime,
            goal_gates=goal.gates or None,
        )}
        for card in plan.cards:
            ids[card.key] = kb.create_task(
                conn, title=card.title, body=work_card_body(card, ids["goal"], goal.title),
                assignee=card.assignee, created_by=created_by,
                workspace_kind=card.workspace[0], workspace_path=card.workspace[1],
                parents=[ids[dep] for dep in card.after],
                idempotency_key=plan.idempotency_key(card), goal_mode=True,
                goal_max_turns=card.max_turns, max_runtime_seconds=card.max_runtime,
                goal_gates=card.gates,
            )
        for card in plan.cards:
            kb.link_tasks(conn, ids[card.key], ids["goal"])
    return ids, True



def _describe(plan: Plan, ids: Optional[dict[str, str]], statuses: dict[str, str],
              record=None) -> dict:
    """*record* is the work-record step's :class:`hermes_cli.kanban_plan_ledger.Record` (or its dry-run
    preview), None when the plan names no ledger goal."""
    def ref(key: str) -> Optional[str]:
        return ids.get(key) if ids else None

    def ledger(card: WorkCard) -> dict:
        if record is None:
            return {"ledger_todos": []}
        out = {"ledger_todos": record.todos.get(card.key, [])}
        if ids is None:
            out["ledger_todos_new"] = record.new.get(card.key, [])
        else:
            out["ledger_todos_created"] = record.created.get(card.key, [])
            out["ledger_todos_linked"] = [i for i in record.attached.get(card.key, [])
                                           if i not in out["ledger_todos_created"]]
        return out

    goal = plan.goal
    desc = {
        "key": plan.key,
        "goal": {"id": ref("goal"), "idempotency_key": plan.idempotency_key(), "title": goal.title,
                 "assignee": goal.assignee, "goal_mode": goal.goal_mode, "gates": goal.gates,
                 "ledger_goal": goal.ledger_goal, "status": statuses.get("goal")},
        "cards": [{"key": c.key, "id": ref(c.key), "idempotency_key": plan.idempotency_key(c),
                   "title": c.title, "assignee": c.assignee, "gates": c.gates,
                   "max_turns": c.max_turns, "max_runtime_seconds": c.max_runtime,
                   "after": c.after, "todos": c.todos, **ledger(c), "status": statuses.get(c.key)}
                  for c in plan.cards],
        "links": [{"parent": p, "child": ch, "parent_id": ref(p), "child_id": ref(ch)}
                  for p, ch in plan.links()],
    }
    desc["notices"] = ([{"agent": n.agent, "card": n.card, "line": n.line} for n in record.notices]
                       if record is not None else [])
    return desc


def _print_ledger(card: dict, dry_run: bool) -> None:
    if dry_run:
        for todo_id in card["ledger_todos"]:
            print(f"        to-do: link {todo_id}")
        for title in card.get("ledger_todos_new", []):
            print(f"        to-do: create {title!r}")
        return
    if card["ledger_todos"]:
        created, linked = set(card["ledger_todos_created"]), set(card["ledger_todos_linked"])
        print("        to-dos: " + ", ".join(
            f"{i} (new)" if i in created else f"{i} (linked)" if i in linked else i
            for i in card["ledger_todos"]))


def _print_text(desc: dict, header: str) -> None:
    def label(key: str, task_id: Optional[str]) -> str:
        return f"{key} ({task_id})" if task_id else key

    goal = desc["goal"]
    print(header)
    status = f"  [{goal['status']}]" if goal["status"] else ""
    print(f"goal  {label('goal', goal['id'])}{status}  @{goal['assignee'] or '-'}  {goal['title']}")
    if goal["ledger_goal"]:
        print(f"        record goal: {goal['ledger_goal']}")
    for gate in goal["gates"]:
        print(f"        gate: {gate if isinstance(gate, str) else gate.get('command')}")
    for card in desc["cards"]:
        status = f"  [{card['status']}]" if card["status"] else ""
        print(f"card  {label(card['key'], card['id'])}{status}  @{card['assignee']}  {card['title']}"
              f"  (max_turns={card['max_turns']}, max_runtime={card['max_runtime_seconds']}s)")
        for gate in card["gates"]:
            print(f"        gate: {gate if isinstance(gate, str) else gate.get('command')}")
        _print_ledger(card, desc["dry_run"])
    for link in desc["links"]:
        print(f"link  {label(link['parent'], link['parent_id'])} -> {label(link['child'], link['child_id'])}")
    for notice in desc["notices"]:
        if desc["dry_run"]:
            print(f"notice  @{notice['agent']}  card {notice['card']}: its card id and to-do ids")
        else:
            print(f"notice  @{notice['agent']}  {notice['line']}")


def _laid_out(ids: dict[str, str]) -> str:
    return ", ".join(f"{key}={task_id}" for key, task_id in ids.items())


def cli_command(args: argparse.Namespace) -> int:
    from hermes_cli import kanban_plan_ledger as record_side
    from hermes_cli.kanban import _profile_author
    from hermes_cli.kanban_output import _err, _print_json

    runner = _profile_author()
    try:
        plan = parse_plan(load_plan_file(args.plan_file), is_profile=profile_check())
        preview = record_side.check(plan, runner=runner) if plan.goal.ledger_goal else None
    except PlanError as exc:
        return _err(f"kanban plan: refused, nothing written: {exc}")
    if args.dry_run:
        desc = _describe(plan, None, {}, preview) | {"dry_run": True, "created": False}
        header = (f"Plan {plan.key}: dry run, nothing written — 1 goal card, "
                  f"{len(plan.cards)} work card(s), {len(plan.links())} link(s)")
    else:
        with kbc.connect_closing() as conn:
            try:
                ids, created = apply_plan(conn, plan, created_by=runner)
            except PlanError as exc:
                return _err(f"kanban plan: refused, nothing written: {exc}")
            statuses = {k: kb.get_task(conn, tid).status for k, tid in ids.items()}
        record = None
        if plan.goal.ledger_goal:
            try:
                record = record_side.write(plan, ids, board=kb.get_current_board(), runner=runner)
            except record_side.RecordStepError as exc:
                return _err(f"kanban plan: the board is written ({_laid_out(ids)}), but the work record step "
                            f"failed at {exc}; run the same plan again to finish the record step")
        desc = _describe(plan, ids, statuses, record) | {"dry_run": False, "created": created}
        header = (f"Plan {plan.key}: created goal {ids['goal']} and {len(plan.cards)} work card(s)"
                  if created else f"Plan {plan.key}: already laid out; existing cards unchanged")
    if args.json:
        _print_json(desc)
    else:
        _print_text(desc, header)
    return 0
