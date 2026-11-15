"""``lina brain``: import a wiki directory into the brain, rebuild its index, merge two pages, print the mapping,
run upkeep and the nightly cycle, and decide the cycle's proposals."""

from __future__ import annotations

import argparse
import sys
import time
from pathlib import Path


def build(subparsers) -> None:
    from hermes_cli.brain.upkeep import run as run_upkeep

    importing = subparsers.add_parser(
        "import", help="Index a wiki directory into the brain; its files are read, never changed",
        description="Read every markdown file under DIR into schema brain of the LINA store: pages, aliases, links "
                    "as edges, timeline entries. Importing an unchanged directory again changes nothing.")
    reindex = subparsers.add_parser(
        "reindex", help="Drop a directory's brain rows and rebuild them from its files",
        description="Delete the pages, aliases, edges and timeline entries of DIR and write them again from the "
                    "files. Recorded merges and atoms are kept.")
    for parser in (importing, reindex):
        parser.add_argument("dir", help="The wiki directory")
        parser.add_argument("--mapping", metavar="FILE",
                            help="Folder-to-type mapping (YAML, see `lina brain mapping`); default: the one this "
                                 "directory was imported with, else the built-in one")
        parser.add_argument("--json", action="store_true", help="Print the full report, every skipped file included")
    importing.set_defaults(brain_fn=run_import, rebuild=False)
    reindex.set_defaults(brain_fn=run_import, rebuild=True)
    merge = subparsers.add_parser("merge", help="Fold one page into another: aliases carry over, links re-point")
    merge.add_argument("from_slug", metavar="FROM")
    merge.add_argument("into_slug", metavar="INTO")
    merge.add_argument("--reason")
    merge.set_defaults(brain_fn=run_merge)
    mapping = subparsers.add_parser("mapping", help="Print the built-in folder-to-type mapping as YAML")
    mapping.set_defaults(brain_fn=run_mapping)
    upkeep = subparsers.add_parser("upkeep", help="Find links, repair indexed backlinks, report lint and orphans")
    upkeep.add_argument("--root", metavar="DIR", help="One imported directory; default: all imported directories")
    upkeep.add_argument("--json", action="store_true", help="Print the deterministic report as JSON")
    upkeep.set_defaults(brain_fn=run_upkeep)
    _build_dream(subparsers)


def _build_dream(subparsers) -> None:
    from hermes_cli.brain.dream.config import PHASES
    from hermes_cli.brain.dream.cycle import run as run_dream
    from hermes_cli.brain.dream.proposals import run as run_proposals

    dream = subparsers.add_parser(
        "dream", help="Run one nightly cycle: upkeep, orphans, then the model phases within their budgets",
        description="Deterministic phases (upkeep, orphans) first, then atoms, patterns, concepts, consolidate, "
                    "contradictions and drift, each within brain.dream.budget.<phase>. Changes to compiled truth, "
                    "merges and relation closes become proposals; no page file is written.")
    dream.add_argument("--plan", action="store_true", help="Say what would run and cost; call no model, write nothing")
    dream.add_argument("--phase", choices=PHASES, help="Run this one phase only")
    dream.add_argument("--metrics", action="store_true",
                       help="Print the counters of finished cycles (orphan rate, new pages linked, cost) instead")
    dream.add_argument("--json", action="store_true", help="Print the full report as JSON")
    dream.set_defaults(brain_fn=run_dream)
    proposals = subparsers.add_parser("proposals", help="List, accept or reject the cycle's proposals")
    actions = proposals.add_subparsers(dest="proposals_command", required=True)
    listing = actions.add_parser("list", help="This agent's proposals, oldest first")
    listing.add_argument("--status", default="proposed", choices=("proposed", "accepted", "rejected", "applied", "all"))
    listing.add_argument("--phase")
    listing.add_argument("--json", action="store_true")
    rates = actions.add_parser("rates", help="Acceptance per phase")
    rates.add_argument("--json", action="store_true")
    accept = actions.add_parser("accept", help="Accept a proposal; a merge or a relation close is applied")
    accept.add_argument("--confirm-external", action="store_true",
                        help="Accept a proposal built from external material (its source is confirmed)")
    reject = actions.add_parser("reject", help="Reject a proposal; the cycle does not propose it again")
    for parser in (accept, reject):
        parser.add_argument("id", type=int)
        parser.add_argument("--by", default="owner", help="Who decided (default: owner)")
    proposals.set_defaults(brain_fn=run_proposals)


def execute(args: argparse.Namespace) -> int:
    from hermes_cli.brain.resolver import MappingError
    from hermes_cli.brain.store import BrainStoreError
    from hermes_state_store import StoreUnavailableError
    try:
        return args.brain_fn(args)
    except (MappingError, BrainStoreError, StoreUnavailableError) as error:
        print(f"brain: {error}", file=sys.stderr)
        return 1


def _mapping(args, conn, root: str):
    from hermes_cli.brain.resolver import DEFAULT_MAPPING, load_mapping, mapping_from
    from hermes_cli.brain.store import root_of
    if args.mapping:
        return load_mapping(Path(args.mapping))
    known = root_of(conn, root)
    return known[1] if known else mapping_from(DEFAULT_MAPPING)


def _counts(counts: dict) -> str:
    return ", ".join(f"{name} {value}" for name, value in counts.items()) or "none"


def _changes_line(changes) -> str:
    if not changes.total():
        return "Changes: none."
    parts = []
    for table in ("roots", "pages", "aliases", "edges", "timeline"):
        signs = [f"{sign}{counts[table]}" for sign, counts in (("+", changes.added), ("~", changes.changed),
                                                               ("-", changes.removed)) if counts[table]]
        if signs:
            parts.append(f"{table} {' '.join(signs)}")
    return f"Changes: {', '.join(parts)}."


def _print_report(summary: dict, changes, seconds: float, *, rebuilt: bool) -> None:
    links = summary["links"]
    print(f"{'Rebuilt' if rebuilt else 'Imported'} {summary['root']}: {summary['files_in']} files in, "
          f"{summary['pages_out']} pages out, {summary['merged']} merged, {summary['skipped']} skipped.")
    print(f"Skipped: {_counts(summary['skipped_by_reason'])}.")
    print(f"Links: {links['found']} found, {links['resolved']} resolved ({links['by_particle']} after a particle), "
          f"{links['unresolved']} unresolved, {links['ambiguous']} ambiguous, {links['to_itself']} to the page "
          f"itself; {summary['edges']} edges.")
    print(f"Orphans: {summary['orphans']} of {summary['pages_out']} pages have no edge in or out.")
    print(f"Types: {_counts(summary['pages_by_type'])} (decided by {_counts(summary['type_from'])}).")
    if summary["unmapped_folders"]:
        print(f"Folders no mapping key matched (typed {summary['default_type']}): "
              f"{_counts(summary['unmapped_folders'])}.")
    if summary["pages_with_ledger_keys_in_file"]:
        print(f"{summary['pages_with_ledger_keys_in_file']} files hold status or deadline keys; they stay in the "
              "files and are not indexed (the work ledger owns them).")
    if summary["frontmatter_unreadable"]:
        print(f"{summary['frontmatter_unreadable']} files have frontmatter that is not YAML; indexed without it.")
    print(_changes_line(changes) + (" (every row written again)" if rebuilt else ""))
    print(f"Took {seconds:.1f} s.")


def run_import(args) -> int:
    from hermes_cli.brain.page import nfc
    from hermes_cli.brain.scan import scan
    from hermes_cli.brain.store import dumps, merges_of, owner_connection, root_of, sync
    directory = Path(args.dir)
    if not directory.is_dir():
        print(f"brain: {directory} is not a directory", file=sys.stderr)
        return 1
    started = time.monotonic()
    root = nfc(str(directory.resolve()))
    with owner_connection() as conn:
        mapping = _mapping(args, conn, root)
        known = root_of(conn, root)
        index = scan(directory, mapping, merges_of(conn, known[0]) if known else ())
        changes = sync(conn, index, rebuild=args.rebuild)
    seconds = time.monotonic() - started
    summary = {**index.summary(), "default_type": mapping.default}
    if args.json:
        print(dumps({**summary, "changes": changes.as_json(), "seconds": round(seconds, 2),
                     "skipped_files": [{"path": path, "reason": reason} for path, reason in index.skipped],
                     "orphan_slugs": index.orphans()}))
    else:
        _print_report(summary, changes, seconds, rebuilt=args.rebuild)
    return 0


def run_merge(args) -> int:
    from hermes_cli.brain.page import nfc
    from hermes_cli.brain.store import merge_pages, owner_connection
    from_slug, into_slug = nfc(args.from_slug), nfc(args.into_slug)
    with owner_connection() as conn:
        changes = merge_pages(conn, from_slug, into_slug, args.reason)
    print(f"Merged {from_slug} into {into_slug}. {_changes_line(changes)}")
    return 0


def run_mapping(args) -> int:
    from hermes_cli.brain.resolver import default_mapping_yaml
    print(default_mapping_yaml(), end="")
    return 0
