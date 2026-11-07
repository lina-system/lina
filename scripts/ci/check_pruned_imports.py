#!/usr/bin/env python3
"""Fail when kept code imports a module that only the pruned upstream files provided.

LINA deletes the upstream paths ``prune-list.txt`` names (docs/forking.md). Deleting a file is
safe only while no kept file still imports it; this check finds such links with an AST scan.

The deleted modules are the ``.py`` files of the upstream revision (``--upstream``, default
``origin/upstream``) that this checkout does not track, minus every module name a tracked file
still provides (a kept ``pkg/x.py`` keeps ``pkg`` importable). Every tracked ``.py`` file is
scanned for:

* ``import a.b`` and ``from a.b import c`` (``a.b`` and ``a.b.c`` are both checked);
* relative imports, resolved against the file's package;
* ``importlib.import_module("a.b")`` and ``__import__("a.b")`` with a string constant.

A file in ``EXCEPTIONS`` is still scanned and its links are listed, but they do not fail.

    python scripts/ci/check_pruned_imports.py                 # against origin/upstream
    python scripts/ci/check_pruned_imports.py --upstream REF  # against any upstream revision

Run it after deleting upstream paths and after every upstream intake (``scripts/sync-upstream``),
on the merged checkout before the merge is committed.

Exit codes: 0 = no link outside the exceptions, 1 = at least one, 2 = git or usage error.
"""

from __future__ import annotations

import argparse
import ast
import subprocess
import sys
from collections.abc import Iterable, Iterator
from dataclasses import dataclass
from pathlib import Path

DEFAULT_UPSTREAM = "origin/upstream"

EXCEPTIONS: dict[str, str] = {
    "plugins/context_engine/lcm-x/bench/instruments/reliability/probe.py":
        "LCM-X subtree bench file kept as upstream ships it; its acp_adapter imports are lazy, "
        "inside functions LINA code and CI never call (CI runs only the subtree's tests/).",
}

_IMPORT_FUNCS = frozenset({"import_module", "__import__"})


@dataclass(frozen=True)
class Link:
    path: str
    line: int
    module: str


def git(repo: Path, *args: str) -> str:
    proc = subprocess.run(["git", *args], cwd=repo, capture_output=True, text=True, encoding="utf-8",
                          errors="replace", stdin=subprocess.DEVNULL, timeout=300, check=False)
    if proc.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)} failed: {proc.stderr.strip()}")
    return proc.stdout


def module_name(path: str) -> str:
    parts = path[:-len(".py")].split("/")
    if parts[-1] == "__init__":
        parts.pop()
    return ".".join(parts)


def _with_parents(names: Iterable[str]) -> set[str]:
    found: set[str] = set()
    for name in names:
        parts = name.split(".")
        found.update(".".join(parts[:depth]) for depth in range(1, len(parts) + 1))
    return found


def deleted_modules(upstream_py: Iterable[str], kept_py: Iterable[str]) -> set[str]:
    kept = set(kept_py)
    still_provided = _with_parents(module_name(p) for p in kept)
    return {module_name(p) for p in upstream_py if p not in kept} - still_provided


def _package(path: str) -> list[str]:
    parts = path[:-len(".py")].split("/")
    return parts[:-1]


def _resolve_relative(path: str, level: int, module: str | None) -> str | None:
    package = _package(path)
    if level - 1 > len(package):
        return None
    base = package[:len(package) - (level - 1)]
    if module:
        base = [*base, *module.split(".")]
    return ".".join(base) or None


def _string_import(call: ast.Call) -> str | None:
    func = call.func
    name = func.attr if isinstance(func, ast.Attribute) else func.id if isinstance(func, ast.Name) else None
    if name not in _IMPORT_FUNCS or not call.args:
        return None
    first = call.args[0]
    if isinstance(first, ast.Constant) and isinstance(first.value, str) and first.value:
        return first.value
    return None


def imported_names(path: str, tree: ast.AST) -> Iterator[tuple[int, str]]:
    """(line, candidate module name) for every import in ``tree``."""
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                yield node.lineno, alias.name
        elif isinstance(node, ast.ImportFrom):
            base = node.module if node.level == 0 else _resolve_relative(path, node.level, node.module)
            if base is None:
                continue
            yield node.lineno, base
            for alias in node.names:
                if alias.name != "*":
                    yield node.lineno, f"{base}.{alias.name}"
        elif isinstance(node, ast.Call):
            target = _string_import(node)
            if target and not target.startswith("."):
                yield node.lineno, target


def find_links(repo: Path, kept_py: Iterable[str], deleted: set[str]) -> tuple[list[Link], list[str]]:
    """The links into ``deleted`` and the kept files that could not be parsed."""
    links: list[Link] = []
    unparsed: list[str] = []
    for path in kept_py:
        try:
            source = (repo / path).read_bytes()
            tree = ast.parse(source, filename=path)
        except (OSError, SyntaxError, ValueError):
            unparsed.append(path)
            continue
        seen: set[tuple[int, str]] = set()
        for line, name in imported_names(path, tree):
            if name in deleted and (line, name) not in seen:
                seen.add((line, name))
                links.append(Link(path, line, name))
    return sorted(links, key=lambda k: (k.path, k.line, k.module)), unparsed


def _py(paths: Iterable[str]) -> list[str]:
    return [p for p in paths if p.endswith(".py")]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--upstream", default=DEFAULT_UPSTREAM,
                        help=f"upstream revision the deleted files come from (default: {DEFAULT_UPSTREAM})")
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[2])
    args = parser.parse_args(argv)
    try:
        kept = _py(dict.fromkeys(p for p in git(args.repo, "ls-files", "-z").split("\0") if p))
        upstream = _py(p for p in git(args.repo, "ls-tree", "-r", "-z", "--name-only", args.upstream).split("\0") if p)
    except RuntimeError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    present = [p for p in kept if (args.repo / p).is_file()]
    deleted = deleted_modules(upstream, kept)
    links, unparsed = find_links(args.repo, present, deleted)
    excepted = [k for k in links if k.path in EXCEPTIONS]
    failing = [k for k in links if k.path not in EXCEPTIONS]

    print(f"Scanned {len(present)} kept .py files against {len(deleted)} module names "
          f"that only deleted {args.upstream} files provided.")
    for path in unparsed:
        print(f"warning: not parsed (syntax error), not scanned: {path}")
    for path in sorted({k.path for k in excepted}):
        print(f"excepted {path}: {EXCEPTIONS[path]}")
        for k in excepted:
            if k.path == path:
                print(f"    {k.path}:{k.line}: {k.module}")
    for k in failing:
        print(f"::error file={k.path},line={k.line}::imports {k.module}, which only a deleted file provided")
    print(f"links: {len(failing)} (excepted: {len(excepted)})")
    return 1 if failing else 0


if __name__ == "__main__":
    raise SystemExit(main())
