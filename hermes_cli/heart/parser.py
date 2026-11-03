"""Argument parsing shared by standalone and registered command surfaces."""

from __future__ import annotations

import argparse
import sqlite3
import sys


def _fn(module: str, name: str):
    """``hermes_cli.heart.<module>.<name>``, imported when the command runs: building the parser must not import the
    journal, which needs the POSIX-only work record (every ``lina`` command builds this parser, on every OS)."""
    def call(args):
        from importlib import import_module
        return getattr(import_module(f"hermes_cli.heart.{module}"), name)(args)
    return call


def build(subparsers) -> None:
    write = subparsers.add_parser("write", help="Append one evidence-backed journal entry")
    for flag in ("event", "feeling", "note", "messages"):
        write.add_argument("--" + flag, required=True)
    write.add_argument("--strength", required=True, type=int, choices=(1, 2, 3))
    write.add_argument("--via", choices=("turn", "review"), default="turn")
    write.set_defaults(heart_fn=_fn("journal", "write"))
    listing = subparsers.add_parser("list", help="Read only this agent's journal")
    listing.add_argument("--since")
    listing.add_argument("--json", action="store_true")
    listing.set_defaults(heart_fn=_fn("journal", "listing"))
    since = subparsers.add_parser("since", help="Read entries since an ISO timestamp")
    since.add_argument("since")
    since.set_defaults(heart_fn=_fn("journal", "listing"), json=False)
    candidates = subparsers.add_parser("candidates", help="Owner messages and subsequent replies")
    candidates.add_argument("--date")
    candidates.add_argument("--limit", type=int, default=40)
    candidates.add_argument("--json", action="store_true")
    candidates.set_defaults(heart_fn=_fn("evidence", "candidates"))
    review = subparsers.add_parser("review-input", help="Journal, self-note and recent edits for daily review")
    review.add_argument("--date")
    review.set_defaults(heart_fn=_fn("journal", "review_input"))
    apply = subparsers.add_parser("self-apply", help="Apply a checked self-note candidate")
    apply.add_argument("--file", required=True)
    apply.set_defaults(heart_fn=_fn("selfnote", "apply"))
    undo = subparsers.add_parser("undo", help="Restore the latest private self-note backup")
    undo.set_defaults(heart_fn=_fn("selfnote", "undo"))
    week = subparsers.add_parser("weekly", help="Write a private weekly page with per-agent counts and self-note changes")
    week.add_argument("--week-ending")
    week.add_argument("--out-dir")
    week.add_argument("--insight-stats")
    week.set_defaults(heart_fn=_fn("weekly", "weekly"))


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(prog="lina heart", description="Evidence-backed journal and self-note editor")
    build(result.add_subparsers(dest="heart_command", required=True))
    return result


def execute(args: argparse.Namespace) -> None:
    from pydantic import ValidationError

    from hermes_cli.heart import storage
    from hermes_cli.heart.locale import text
    try:
        args.heart_fn(args)
    except sqlite3.Error as error:
        print("heart: " + text("database", error=str(error)), file=sys.stderr)
        raise SystemExit(2) from None
    except (storage.HeartError, OSError, ValidationError, ValueError) as error:
        print(f"heart: {error}", file=sys.stderr)
        raise SystemExit(2) from None
