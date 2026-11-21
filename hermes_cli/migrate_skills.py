"""Build a read-only inventory of explicitly selected legacy skill trees."""

from __future__ import annotations

import argparse
import hashlib
import json
import sqlite3
import sys
import time
from collections import Counter, defaultdict
from dataclasses import dataclass
from pathlib import Path

from agent.skill_utils import EXCLUDED_SKILL_DIRS, SKILL_SUPPORT_DIRS, parse_frontmatter
from hermes_cli.migrate_skill_calls import skill_calls


@dataclass(frozen=True)
class Tree:
    path: Path
    kind: str
    owners: tuple[str, ...]


def _files(root: Path, *, discovery: bool, output: Path | None):
    """Walk linked directories, failing on unreadable input or a link cycle."""
    pending = [(root, frozenset())]
    while pending:
        directory, ancestors = pending.pop()
        physical = directory.resolve(strict=True)
        if output and output.is_relative_to(physical):
            raise ValueError("manifest must be outside the input trees")
        if physical in ancestors:
            raise ValueError(f"directory cycle at {directory}")
        children = list(directory.iterdir())
        skip = set()
        if discovery:
            skip.update(EXCLUDED_SKILL_DIRS)
            if (directory / "SKILL.md").is_file():
                skip.update(SKILL_SUPPORT_DIRS)
        for child in children:
            if child.is_dir():
                if child.name not in skip:
                    pending.append((child, ancestors | {physical}))
            elif not discovery or child.name == "SKILL.md":
                if not child.is_file():
                    raise ValueError(f"expected a regular file at {child}")
                yield child


def _package(directory: Path, output: Path | None) -> tuple[str, str, tuple[str, ...]]:
    metadata, _ = parse_frontmatter((directory / "SKILL.md").read_text(encoding="utf-8-sig"))
    name = metadata.get("name") or directory.name
    if not isinstance(name, str):
        raise ValueError(f"expected a skill name at {directory}")
    description = metadata.get("description")
    prefix = tuple(description.casefold().split()[:3]) if isinstance(description, str) else ()
    digest = hashlib.sha256()
    files = _files(directory, discovery=False, output=output)
    for rel, file in sorted((f.relative_to(directory).as_posix(), f) for f in files):
        digest.update(rel.encode() + b"\0")
        with file.open("rb") as stream:
            digest.update(hashlib.file_digest(stream, "sha256").digest())
    return name, digest.hexdigest(), prefix


def _inputs(args: argparse.Namespace) -> tuple[dict[str, Path], list[Tree]]:
    homes = {}
    trees = []
    for spec in args.home:
        label, equal, value = spec.partition("=")
        if not equal or not label or not value:
            raise ValueError("--home requires LABEL=DIRECTORY")
        path = Path(value).resolve(strict=True)
        if not path.is_dir() or label in homes or path in homes.values():
            raise ValueError("each --home requires a distinct label and directory")
        homes[label] = path
        skills = path / "skills"
        if skills.exists() or skills.is_symlink():
            trees.append(Tree(skills, "home", (label,)))
    for path in args.shared:
        trees.append(Tree(path, "shared", tuple(sorted(homes))))
    for spec in args.home_tree:
        owner, equal, value = spec.partition("=")
        if not equal or not value or owner not in homes:
            raise ValueError("--home-tree requires an existing home label and a skill directory")
        trees.append(Tree(Path(value), "home", (owner,)))
    for path in args.bundle:
        trees.append(Tree(path, "bundle", ()))
    return homes, trees


def _inventory(args: argparse.Namespace):
    homes, trees = _inputs(args)
    output = args.manifest.resolve() if args.manifest else None
    if output and any(output.is_relative_to(path) for path in homes.values()):
        raise ValueError("manifest must be outside the input homes")
    exclusions = set()
    if args.exclude:
        exclusions = {s.strip() for s in args.exclude.read_text(encoding="utf-8-sig").splitlines() if s.strip()}
    now = time.time()
    totals: Counter[str] = Counter()
    file_calls: Counter[str] = Counter()
    for database in {(home / "state.db").resolve() for home in homes.values()}:
        calls, with_file = skill_calls(database, now)
        totals.update(calls)
        file_calls.update(with_file)

    # One linked package can be visible in several homes while occupying just one place.
    sources: dict[Path, set[Tree]] = defaultdict(set)
    for tree in trees:
        for md in _files(tree.path, discovery=True, output=output):
            sources[md.parent.resolve()].add(tree)
    groups = defaultdict(lambda: defaultdict(list))
    prefixes = defaultdict(set)
    by_name = defaultdict(set)
    for path, views in sorted(sources.items()):
        name, digest, prefix = _package(path, output)
        if len(prefix) == 3:
            prefixes[prefix].add(name)
            by_name[name].add(prefix)
        owners = sorted({owner for view in views for owner in view.owners})
        kinds = sorted({view.kind for view in views})
        groups[name][digest].append({"path": str(path), "kinds": kinds, "owners": owners})

    records = []
    for name in sorted(groups.keys() - exclusions):
        variants = groups[name]
        places = [loc for locations in variants.values() for loc in locations]
        visibility = {owner for loc in places for owner in loc["owners"]}
        overlap = sorted({other for prefix in by_name[name] for other in prefixes[prefix]} - {name})
        for digest, locations in sorted(variants.items()):
            records.append({
                "name": name, "layer": "common" if visibility == homes.keys() else "agent",
                "owners": sorted({owner for loc in locations for owner in loc["owners"]}),
                "sha256": digest, "copies_identical": len(variants) == 1, "locations": locations,
                "calls_30d": totals[name], "file_calls_30d": file_calls[name],
                "same_name_overlap": len(places) > 1, "weak_overlap": overlap,
                "archive_candidate": len(places) > 1 and totals[name] == 0,
            })
    return {
        "version": 1, "homes": sorted(homes), "window_end": now, "exclusions": sorted(exclusions),
        "counts": {"names": len(groups), "locations": len(sources), "records": len(records),
                   "excluded_names": len(groups.keys() & exclusions)},
        "records": records,
    }


def _command(args: argparse.Namespace) -> None:
    try:
        manifest = _inventory(args)
        text = json.dumps(manifest, ensure_ascii=False, indent=2) + "\n"
        if args.manifest:
            with args.manifest.open("x", encoding="utf-8") as stream:
                try:
                    stream.write(text)
                    stream.flush()
                except OSError:
                    stream.close()
                    args.manifest.unlink()
                    raise
        else:
            sys.stdout.write(text)
    except (OSError, ValueError, sqlite3.Error) as exc:
        print(f"skill inventory: {exc}", file=sys.stderr)
        raise SystemExit(1) from exc
    raise SystemExit(0)


def configure(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--home", action="append", required=True, metavar="LABEL=DIRECTORY",
                        help="Read this home's skills and state.db (repeatable)")
    parser.add_argument("--shared", type=Path, action="append", default=[], help="Skill tree visible to every home")
    parser.add_argument("--home-tree", action="append", default=[], metavar="LABEL=DIRECTORY",
                        help="Additional skill tree visible to this home (repeatable)")
    parser.add_argument("--bundle", type=Path, action="append", default=[], help="Install-only skill tree")
    parser.add_argument("--exclude", type=Path, help="Exact skill names to omit, one per line")
    parser.add_argument("--manifest", type=Path, help="Create this new JSON file (default: stdout)")
    parser.set_defaults(func=_command)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    configure(parser)
    _command(parser.parse_args())
