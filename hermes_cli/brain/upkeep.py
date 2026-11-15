"""Deterministic brain upkeep: files are read outside transactions; only missing indexed edges are written."""

from __future__ import annotations

import argparse
from dataclasses import asdict, dataclass
from pathlib import Path

from hermes_cli.brain import links
from hermes_cli.brain.lint import Finding, lint
from hermes_cli.brain.page import nfc, parse
from hermes_cli.brain.resolver import mapping_from
from hermes_cli.brain.scan import Index, _Resolver, apply_merges, scan
from hermes_cli.brain.store import BrainStoreError, dumps, merges_of, owner_connection

Edge = tuple[str, str, str, str]


@dataclass(frozen=True)
class Analysis:
    written: set[Edge]
    inferred: set[Edge]
    findings: list[Finding]
    orphans: list[str]


def analyze(index: Index, merges: tuple[tuple[str, str], ...] = ()) -> Analysis:
    """Add plain mentions to the scanned graph and replay the importer's recorded merges."""
    findings = lint(index)
    written = set(index.edges)
    resolver = _Resolver(list(index.pages.values()), index.aliases)
    names = {alias for _, _, alias, _ in index.aliases}
    inferred: set[Edge] = set()
    for record in index.pages.values():
        page = parse(record.body)
        body = record.body[len(page.frontmatter_text):]
        for link in links.mentions(body, names):
            target, _ = resolver.resolve(link)
            if target and target != record.slug:
                inferred.add((record.slug, target, "mentions", record.path))
    index.edges.update(inferred)
    apply_merges(index, list(merges))

    def moved(edges: set[Edge]) -> set[Edge]:
        def destination(slug: str) -> str:
            while slug in index.redirects:
                slug = index.redirects[slug]
            return slug
        return {(destination(a), destination(b), kind, ref) for a, b, kind, ref in edges
                if destination(a) != destination(b)}

    return Analysis(moved(written), moved(inferred) - moved(written), findings, index.orphans())


def _edge_rows(edges: set[Edge]) -> list[dict[str, str]]:
    return [{"from": a, "to": b, "type": kind, "source_ref": ref} for a, b, kind, ref in sorted(edges)]


def _insert(conn, root_id: int, edges: set[Edge]) -> set[Edge]:
    """One short transaction per edge; RETURNING counts only this run's actual inserts.

    Root predicates protect against pages disappearing or being reimported under another root after the scan.
    No cycle/advisory lock is acquired and no file is read while a transaction is open.
    """
    added: set[Edge] = set()
    for edge in sorted(edges):
        with conn.transaction():
            row = conn.execute(
                "INSERT INTO brain.edges (from_slug, to_slug, type, source_ref) "
                "SELECT a.slug, b.slug, %s, %s FROM brain.pages a, brain.pages b "
                "WHERE a.slug = %s AND b.slug = %s AND a.root_id = %s AND b.root_id = %s "
                "ON CONFLICT DO NOTHING RETURNING from_slug, to_slug, type, source_ref",
                (edge[2], edge[3], edge[0], edge[1], root_id, root_id)).fetchone()
            if row:
                added.add(tuple(row))
    return added


def _root_report(conn, root: tuple) -> dict:
    root_id, path, mapping = root
    directory = Path(path)
    if not directory.is_dir():
        raise BrainStoreError(f"imported root is not a directory: {path}")
    merges = merges_of(conn, root_id)
    indexed = dict(conn.execute("SELECT slug, path FROM brain.pages WHERE root_id = %s", (root_id,)).fetchall())
    old = {tuple(row) for row in conn.execute(
        "SELECT e.from_slug, e.to_slug, e.type, e.source_ref FROM brain.edges e "
        "JOIN brain.pages a ON a.slug = e.from_slug JOIN brain.pages b ON b.slug = e.to_slug "
        "WHERE a.root_id = %s OR b.root_id = %s", (root_id, root_id))}
    index = scan(directory, mapping_from(mapping))
    analysis = analyze(index, merges)
    eligible = {slug for slug, record in index.pages.items() if indexed.get(slug) == record.path}
    desired = {edge for edge in index.edges if edge[0] in eligible and edge[1] in eligible}
    added = _insert(conn, root_id, desired - old)
    old_pairs = {(a, b) for a, b, _, _ in old}
    pairs = {(a, b) for a, b, _, _ in index.edges}
    fixed = {edge for edge in added if (edge[1], edge[0]) in old_pairs}
    missing = {(b, a) for a, b in pairs if (b, a) not in pairs}
    linked = {slug for edge in index.edges | old for slug in edge[:2]}
    return {
        "root": path,
        "new_links": len(added),
        "fixed_backlinks": len(fixed),
        "written_links": _edge_rows(analysis.written),
        "inferred_mentions": _edge_rows(analysis.inferred),
        "added_links": _edge_rows(added),
        "backlinks_fixed": _edge_rows(fixed),
        "missing_backlinks": [{"from": a, "to": b, "path": index.pages[a].path} for a, b in sorted(missing)],
        "lint": [asdict(finding) for finding in analysis.findings],
        "orphans": sorted(set(index.pages) - linked),
        "unindexed_pages": sorted(record.path for slug, record in index.pages.items() if slug not in eligible),
        "skipped_files": [{"path": rel, "reason": reason} for rel, reason in sorted(index.skipped)],
    }


def run(args: argparse.Namespace) -> int:
    """Run once for one imported root or all imported roots in path order."""
    with owner_connection() as conn:
        conn.autocommit = True
        if args.root:
            path = nfc(str(Path(args.root).resolve()))
            roots = conn.execute("SELECT root_id, path, mapping FROM brain.roots WHERE path = %s", (path,)).fetchall()
            if not roots:
                raise BrainStoreError("root is not imported; run `lina brain import DIR` first")
        else:
            roots = conn.execute("SELECT root_id, path, mapping FROM brain.roots ORDER BY path").fetchall()
        reports = [_root_report(conn, root) for root in roots]
    if args.json:
        result = reports[0] if args.root else {
            "roots": reports,
            "new_links": sum(report["new_links"] for report in reports),
            "fixed_backlinks": sum(report["fixed_backlinks"] for report in reports),
        }
        print(dumps(result))
    else:
        for report in reports:
            print(f"Upkeep {report['root']}: {report['new_links']} new links, "
                  f"{report['fixed_backlinks']} fixed backlinks.")
            print(f"Links: {len(report['written_links'])} written, {len(report['inferred_mentions'])} inferred.")
            for finding in report["lint"]:
                print(f"{finding['path']}: {finding['rule']}: {finding['detail']}")
            for missing in report["missing_backlinks"]:
                print(f"{missing['path']}: missing-backlink: {missing['from']} -> {missing['to']}")
            for slug in report["orphans"]:
                print(f"Orphan: {slug}")
            for path in report["unindexed_pages"]:
                print(f"{path}: not-indexed: run `lina brain import DIR`")
        if not reports:
            print("No imported brain roots.")
    return 0
