"""``lina words``: the owner path that writes the shared word list (lina#378)."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from hermes_cli.korean.words import REASONS


def build(subparsers) -> None:
    add = subparsers.add_parser("add", help="Add one correction: the word to avoid and the word to use")
    add.add_argument("--avoid", required=True)
    add.add_argument("--use", required=True)
    add.add_argument("--reason", required=True, choices=REASONS)
    add.add_argument("--source", help="memory.messages uid of the correction")
    add.add_argument("--flagged", help="The flagged sentence (kept in the store for the measurement run only)")
    add.add_argument("--fixed", help="The fixed sentence (with --flagged)")
    add.set_defaults(words_fn=_add)
    retire = subparsers.add_parser("retire", help="Retire an entry; the next read leaves it out")
    retire.add_argument("--avoid", required=True)
    retire.add_argument("--use", help="Only the entry to this word (default: every active entry for --avoid)")
    retire.add_argument("--source", help="memory.messages uid of the sentence that retired it")
    retire.set_defaults(words_fn=_retire)
    listing = subparsers.add_parser("list", help="Show the active entries (never the sentence pairs by default)")
    listing.add_argument("--all", action="store_true", help="Also the retired entries")
    listing.add_argument("--json", action="store_true")
    listing.add_argument("--with-pairs", action="store_true",
                         help="Local only: also print each entry's flagged and fixed sentences")
    listing.set_defaults(words_fn=_list)
    seed = subparsers.add_parser("import", help="Import a TSV seed: avoid<TAB>use<TAB>reason per line")
    seed.add_argument("path", type=Path)
    seed.set_defaults(words_fn=_import)


def _line(entry) -> str:
    retired = f" (retired {entry.retired_at:%Y-%m-%d})" if entry.retired_at else ""
    return f"#{entry.entry_id} {entry.avoid} -> {entry.use} [{entry.reason}]{retired}"


def _add(args, conn) -> int:
    from hermes_cli.korean.words import WordPair, add
    if (args.flagged is None) != (args.fixed is None):
        print("words: --flagged and --fixed go together", file=sys.stderr)
        return 1
    pair = WordPair(args.flagged, args.fixed, args.source) if args.flagged is not None else None
    entry, created = add(conn, args.avoid, args.use, args.reason, source_uid=args.source, pair=pair)
    print(("added " if created else "already in the list ") + _line(entry))
    return 0


def _retire(args, conn) -> int:
    from hermes_cli.korean.words import retire
    retired = retire(conn, args.avoid, use=args.use, source_uid=args.source)
    if not retired:
        print("words: no active entry for that word", file=sys.stderr)
        return 1
    for entry in retired:
        print("retired " + _line(entry))
    return 0


def _list(args, conn) -> int:
    from hermes_cli.korean.words import entries, pairs
    found = entries(conn, include_retired=args.all)
    sentences = pairs(conn) if args.with_pairs else {}
    if args.json:
        rows = []
        for entry in found:
            row = entry.as_dict()
            if args.with_pairs:
                row["pairs"] = [{"flagged": p.flagged, "fixed": p.fixed, "source": p.source_uid}
                                for p in sentences.get(entry.entry_id, [])]
            rows.append(row)
        print(json.dumps(rows, ensure_ascii=False, indent=2))
        return 0
    for entry in found:
        print(_line(entry))
        for pair in sentences.get(entry.entry_id, []):
            print(f"    {pair.flagged}  =>  {pair.fixed}")
    print(f"{len(found)} entries")
    return 0


def _import(args, conn) -> int:
    from hermes_cli.korean.words import import_seed
    with args.path.open(encoding="utf-8-sig", newline="") as handle:
        result = import_seed(conn, handle)
    for number, why in result.problems:
        print(f"line {number}: not imported: {why}", file=sys.stderr)
    print(f"{result.lines} lines: {result.added} added, {result.present} already in the list, "
          f"{len(result.problems)} not imported")
    return 1 if result.problems else 0


def execute(args: argparse.Namespace) -> None:
    from hermes_cli.korean.words import WordListError, owner_connection
    from hermes_state_store import StoreUnavailableError
    try:
        with owner_connection() as conn:
            code = args.words_fn(args, conn)
    except WordListError as error:
        print(f"words: {error}", file=sys.stderr)
        raise SystemExit(1) from None
    except (StoreUnavailableError, OSError) as error:
        print(f"words: {error}", file=sys.stderr)
        raise SystemExit(2) from None
    raise SystemExit(code)
