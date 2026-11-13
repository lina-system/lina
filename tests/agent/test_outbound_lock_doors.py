"""Every egress door reaches ``agent.outbound_lock.check_outbound`` (lina#638).

The doors are found by grep, not listed: each call of a low-level sender (a platform's standalone
sender, a Bot Chat admission into another agent, a Desktop relay envelope, a peer HTTP turn, the
webhook ``gh pr comment``, an agent-addressed notice command) must sit in a function that calls the
check, or in a private helper whose every caller in its module does (and nothing outside its module
names it), or carry ``# outbound-checked: <path>::<function>`` (on the call, the line above it or
the enclosing def) naming a function that calls it. ``# outbound-exempt: <reason>`` marks a call
that is not egress (an inbound turn into this agent). Every class with an adapter ``send*`` method
must reach ``BasePlatformAdapter``, whose ``__init_subclass__`` routes those methods through
the check, or be marked exempt on its ``class`` line (a wrapper delegating to an adapter).
"""

from __future__ import annotations

import ast
import re
from functools import cache
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCAN_DIRS = ("agent", "cron", "gateway", "hermes_cli", "plugins", "tools", "tui_gateway")
EGRESS_CALLS = {
    "platform standalone sender": re.compile(
        r"(?<!def )\b(_send_telegram|_plugin_standalone_sender|_send_matrix_via_adapter|send_message_handler)\("),
    "Bot Chat admission into another agent": re.compile(r"(?<!def )\bdeliver_to_live_owner\("),
    "Desktop relay envelope": re.compile(r"(?<!def )\benqueue_envelope\("),
    "peer HTTP turn": re.compile(r"\b_request\(\s*f\"[^\"]*/(chat|v1/runs)\""),
    "webhook github_comment": re.compile(r"\"gh\", \"pr\", \"comment\""),
    "agent-addressed notice command": re.compile(r"\.format\(agent=job\["),
}
PRAGMA = re.compile(r"#\s*outbound-checked:\s*(\S+)::(\w+(?:\.\w+)?)")
EXEMPT = re.compile(r"#\s*outbound-exempt:\s*\S")


@cache
def _sources() -> dict[str, str]:
    files = {}
    for top in SCAN_DIRS:
        for path in (ROOT / top).rglob("*.py"):
            rel = path.relative_to(ROOT).as_posix()
            if "/tests/" not in f"/{rel}" and "node_modules" not in rel:
                files[rel] = path.read_text(encoding="utf-8")
    return files


@cache
def _tree(rel: str) -> ast.Module:
    return ast.parse(_sources()[rel])


def _units(rel: str) -> list[tuple[str, ast.AST]]:
    """Top-level functions, class methods (``Class.method``) and top-level assignments."""
    units = []
    for node in _tree(rel).body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            units.append((node.name, node))
        elif isinstance(node, ast.ClassDef):
            units += [(f"{node.name}.{item.name}", item) for item in node.body
                      if isinstance(item, (ast.FunctionDef, ast.AsyncFunctionDef))]
        elif isinstance(node, (ast.Assign, ast.AnnAssign)):
            targets = node.targets if isinstance(node, ast.Assign) else [node.target]
            units += [(t.id, node) for t in targets if isinstance(t, ast.Name)]
    return units


def _names_used(node: ast.AST) -> set[str]:
    return {n.id if isinstance(n, ast.Name) else n.attr
            for n in ast.walk(node) if isinstance(n, (ast.Name, ast.Attribute))}


def _called(node: ast.AST) -> set[str]:
    calls = set()
    for n in ast.walk(node):
        if isinstance(n, ast.Call):
            func = n.func
            calls.add(func.id if isinstance(func, ast.Name) else getattr(func, "attr", ""))
    return calls


@cache
def _checkers() -> frozenset[str]:
    """``check_outbound`` and every function that calls it directly (``_outbound_refused``, ...)."""
    names = {"check_outbound"}
    for rel in _sources():
        if "check_outbound" in _sources()[rel]:
            names |= {name.rsplit(".", 1)[-1] for name, node in _units(rel) if "check_outbound" in _called(node)}
    return frozenset(names)


def _checks(node: ast.AST) -> bool:
    return bool(_called(node) & _checkers())


def _unit_at(rel: str, line: int) -> tuple[str, ast.AST] | None:
    return next(((name, node) for name, node in _units(rel)
                 if node.lineno <= line <= (node.end_lineno or node.lineno)), None)


@cache
def _named_outside(rel: str, leaf: str) -> bool:
    word = re.compile(rf"\b{re.escape(leaf)}\b")
    return any(word.search(text) for other, text in _sources().items() if other != rel)


def _covered(rel: str, name: str, node: ast.AST, seen: frozenset = frozenset()) -> bool:
    if _checks(node):
        return True
    leaf = name.rsplit(".", 1)[-1]
    if not leaf.startswith("_") or name in seen or _named_outside(rel, leaf):
        return False
    callers = [(n, u) for n, u in _units(rel) if n != name and leaf in _names_used(u)]
    return bool(callers) and all(_covered(rel, n, u, seen | {name}) for n, u in callers)


def _pragma_ok(text: str) -> bool:
    if EXEMPT.search(text):
        return True
    match = PRAGMA.search(text)
    if not match or match.group(1) not in _sources():
        return False
    unit = dict(_units(match.group(1))).get(match.group(2))
    return unit is not None and _checks(unit)


def _prose_lines(rel: str) -> set[int]:
    """Lines inside docstrings or whole-line comments: a sender named in prose is not a call."""
    lines = {i for i, line in enumerate(_sources()[rel].splitlines(), 1) if line.lstrip().startswith("#")}
    for node in ast.walk(_tree(rel)):
        if isinstance(node, ast.Expr) and isinstance(node.value, ast.Constant) and isinstance(node.value.value, str):
            lines |= set(range(node.lineno, (node.end_lineno or node.lineno) + 1))
    return lines


def _egress_hits() -> list[tuple[str, int, str]]:
    hits = []
    for rel, text in _sources().items():
        for kind, pattern in EGRESS_CALLS.items():
            hits += [(rel, text.count("\n", 0, m.start()) + 1, kind) for m in pattern.finditer(text)]
    return [hit for hit in hits if hit[1] not in _prose_lines(hit[0])]


def test_the_grep_finds_every_kind_of_door():
    """A pattern that stops matching would make the coverage test below pass vacuously."""
    assert {kind for _, _, kind in _egress_hits()} == set(EGRESS_CALLS)


def test_every_egress_call_reaches_the_outbound_check():
    uncovered = []
    for rel, line, kind in _egress_hits():
        lines = _sources()[rel].splitlines()
        unit = _unit_at(rel, line)
        pragma_text = "\n".join(lines[max(line - 2, 0):line] + ([lines[unit[1].lineno - 1]] if unit else []))
        if _pragma_ok(pragma_text) or (unit is not None and _covered(rel, *unit)):
            continue
        uncovered.append(f"{rel}:{line} ({kind}) in {unit[0] if unit else '<module>'}")
    assert not uncovered, ("egress doors that do not reach agent.outbound_lock.check_outbound:\n  "
                           + "\n  ".join(uncovered))


def _classes() -> dict[str, list[tuple[list[str], set[str]]]]:
    """class name -> [(base names, methods with a ``chat_id`` parameter)] over the scanned tree."""
    from agent.outbound_lock import ADAPTER_SEND_METHODS
    classes: dict[str, list] = {}
    for rel in _sources():
        for node in ast.walk(_tree(rel)):
            if not isinstance(node, ast.ClassDef):
                continue
            bases = [b.id if isinstance(b, ast.Name) else getattr(b, "attr", "") for b in node.bases]
            if EXEMPT.search(_sources()[rel].splitlines()[node.lineno - 1]):
                continue
            sends = {item.name for item in node.body
                     if isinstance(item, ast.AsyncFunctionDef) and item.name in ADAPTER_SEND_METHODS
                     and any(arg.arg == "chat_id" for arg in item.args.args + item.args.kwonlyargs)}
            classes.setdefault(node.name, []).append((bases, sends))
    return classes


def test_every_adapter_send_method_is_guarded_by_the_base_class():
    classes = _classes()
    adapters = {"BasePlatformAdapter"}
    while True:
        more = {name for name, defs in classes.items()
                if name not in adapters and any(set(bases) & adapters for bases, _ in defs)}
        if not more:
            break
        adapters |= more

    def reaches_adapter(name: str, seen: frozenset = frozenset()) -> bool:
        if name in adapters:
            return True
        users = [user for user, defs in classes.items() if any(name in bases for bases, _ in defs)]
        return bool(users) and all(reaches_adapter(u, seen | {name}) for u in users if u not in seen)

    loose = sorted(name for name, defs in classes.items()
                   if any(sends for _, sends in defs) and not reaches_adapter(name))
    assert len(adapters) > 5
    assert not loose, f"classes with adapter send methods outside BasePlatformAdapter: {loose}"
