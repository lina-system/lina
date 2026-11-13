"""Undo command dispatch and content-free diagnostics."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
import time

from pydantic import ValidationError

from hermes_cli.undo import journal, pending, snapshot
from hermes_cli.undo.apply import apply
from hermes_cli.undo.env import load
from hermes_cli.undo.models import Entry, FileRef, UndoError, when
from hermes_cli.undo.parser import build
from hermes_cli.undo.storage import Store


def flagged(row: Entry) -> str:
    mark = "!" if any(isinstance(r, FileRef) and r.shrunk for r in row.objects) else ""
    return mark + row.model_dump_json()


def display(store: Store, args: argparse.Namespace) -> None:
    with store.locked():
        rows = journal.read(store)
        unfinished = pending.unfinished(store, args.bot, rows) if args.undo_command == "last" else []
    if args.undo_command == "last":
        duration = re.fullmatch(r"([1-9][0-9]*)([mhd])", args.within)
        if not duration:
            raise UndoError("--within must be a positive duration such as 24h")
        seconds = int(duration[1]) * {"m": 60, "h": 3600, "d": 86400}[duration[2]]
        now = time.time()
        rows = [r for r in rows if r.bot == args.bot and now - seconds <= when(r.at) <= now]
        row = rows[-1] if rows else None
    else:
        row = next((r for r in rows if r.change_id == args.change_id), None)
        if row is None:
            raise UndoError("unknown change_id")
    print(flagged(row) if row else "No recent changes")
    for line in unfinished:
        print(line)


def execute(args: argparse.Namespace) -> int:
    try:
        store = Store()
        handlers = {
            "begin": lambda: print(pending.begin(store, load(store), args)),
            "end": lambda: print(pending.end(store, args.change_id)),
            "log": lambda: log(store, args),
            "apply": lambda: apply(store, load(store), args.change_id),
            "last": lambda: display(store, args),
            "show": lambda: display(store, args),
            "snapshot": lambda: (snapshot.dry_run if args.dry_run else snapshot.run)(store, load(store)),
        }
        handlers[args.undo_command]()
        return 0
    except UndoError as error:
        print("undo: " + str(error), file=sys.stderr)
    except (OSError, ValueError, KeyError, TypeError, ValidationError, subprocess.TimeoutExpired):
        print("undo: cannot read or write valid undo data", file=sys.stderr)
    return 2


def log(store: Store, args: argparse.Namespace) -> None:
    at, change_id = journal.identity()
    row = Entry.model_validate({"at": at, "change_id": args.change_id or change_id, "bot": args.bot,
                                "what": args.what, "objects": json.loads(args.objects), "kind": args.kind,
                                "until": args.until, "how": args.how})
    print(journal.append(store, row).change_id)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="lina undo", description="Checkpoint-backed file undo and change references")
    build(parser)
    return execute(parser.parse_args(argv))
