#!/usr/bin/env python3
"""Fail when a tracked file matches ``prune-list.txt``: upstream paths LINA does not carry.

LINA keeps the whole Hermes history and takes upstream in by merge (docs/forking.md). Every
merge brings back files LINA deleted and adds new files under deleted directories, so the list
of what LINA does not carry lives in the repository and every intake applies it
(``scripts/sync-upstream``). This check is the backstop: CI fails while any tracked file
still matches the list.

List format (one rule per line, repo-relative, ``/`` separators):

* ``path``: that file, or everything under that directory (a directory prefix, never a
  substring: ``docker`` matches ``docker/x`` but not ``docker-compose.yml``);
* a rule containing ``*``, ``?`` or ``[``: an fnmatch glob on the whole path (``*`` also
  crosses ``/``);
* ``!rule``: keep what the rule matches, even when an earlier rule prunes it. The last
  matching rule wins, as in ``.gitignore``;
* ``#`` starts a comment line; blank lines are ignored.

    python scripts/ci/check_prune_list.py           # the tracked files of this checkout
    python scripts/ci/check_prune_list.py --rev REV # the tree of any revision

Exit codes: 0 = no tracked file matches, 1 = at least one does, 2 = list, git or usage error.
"""

from __future__ import annotations

import argparse
import fnmatch
import subprocess
import sys
from collections.abc import Iterable
from dataclasses import dataclass
from pathlib import Path

LIST_NAME = "prune-list.txt"
_GLOB_CHARS = frozenset("*?[")
_SHOWN_PER_RULE = 5


class PruneListError(ValueError):
    pass


@dataclass(frozen=True)
class Rule:
    pattern: str
    keep: bool
    line: int

    def matches(self, path: str) -> bool:
        if _GLOB_CHARS.intersection(self.pattern):
            return fnmatch.fnmatchcase(path, self.pattern)
        return path == self.pattern or path.startswith(self.pattern + "/")


def parse(text: str) -> list[Rule]:
    rules: list[Rule] = []
    for number, raw in enumerate(text.splitlines(), start=1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        keep = line.startswith("!")
        pattern = line[1:] if keep else line
        pattern = pattern.rstrip("/")
        if not pattern or pattern.startswith("/") or "\\" in pattern or ".." in pattern.split("/"):
            raise PruneListError(f"{LIST_NAME}:{number}: not a repo-relative path: {raw!r}")
        rules.append(Rule(pattern, keep, number))
    return rules


def load(repo: Path) -> list[Rule]:
    return parse((repo / LIST_NAME).read_text(encoding="utf-8"))


def deciding_rule(rules: Iterable[Rule], path: str) -> Rule | None:
    """The last rule matching ``path``, or None when no rule does."""
    found = None
    for rule in rules:
        if rule.matches(path):
            found = rule
    return found


def is_pruned(rules: Iterable[Rule], path: str) -> bool:
    rule = deciding_rule(rules, path)
    return rule is not None and not rule.keep


def git(repo: Path, *args: str) -> str:
    proc = subprocess.run(["git", *args], cwd=repo, capture_output=True, text=True,
                          encoding="utf-8", stdin=subprocess.DEVNULL, check=False)
    if proc.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)} failed: {proc.stderr.strip()}")
    return proc.stdout


def tracked_files(repo: Path, rev: str | None = None) -> list[str]:
    if rev is None:
        out = git(repo, "ls-files", "-z")
    else:
        out = git(repo, "ls-tree", "-r", "-z", "--name-only", rev)
    # An unmerged path is listed once per stage; keep each path once, in order.
    return list(dict.fromkeys(p for p in out.split("\0") if p))


def matches_by_rule(rules: list[Rule], paths: Iterable[str]) -> dict[Rule, list[str]]:
    found: dict[Rule, list[str]] = {}
    for path in paths:
        rule = deciding_rule(rules, path)
        if rule is not None and not rule.keep:
            found.setdefault(rule, []).append(path)
    return found


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--rev", help="check the tree of this revision instead of the index")
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[2])
    args = parser.parse_args(argv)
    try:
        rules = load(args.repo)
        found = matches_by_rule(rules, tracked_files(args.repo, args.rev))
    except (OSError, PruneListError, RuntimeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    if not found:
        print(f"No tracked file matches {LIST_NAME} ({len(rules)} rules).")
        return 0
    total = sum(len(paths) for paths in found.values())
    print(f"::error::{total} tracked files match {LIST_NAME}; LINA does not carry them. "
          f"Remove them with `git rm` (scripts/sync-upstream does it on every upstream intake).")
    for rule in sorted(found, key=lambda r: r.line):
        paths = found[rule]
        print(f"{LIST_NAME}:{rule.line} {rule.pattern}: {len(paths)} files")
        for path in paths[:_SHOWN_PER_RULE]:
            print(f"    {path}")
        if len(paths) > _SHOWN_PER_RULE:
            print(f"    ... and {len(paths) - _SHOWN_PER_RULE} more")
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
