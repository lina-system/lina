"""Argument parsing shared by standalone and registered command surfaces."""

from __future__ import annotations

import argparse
import sqlite3
import sys

TARGET_KINDS = ("topic", "task_type", "tone", "tool", "method", "media", "other")
ROUTES = ("reaction", "outcome", "exposure", "consistency")
DEFAULT_CANDIDATE_LIMIT = 40


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
    write.add_argument("--pref-target", help="The taste this entry bears on (short, no names)")
    write.add_argument("--pref-kind", choices=TARGET_KINDS)
    write.add_argument("--pref-dir", choices=("like", "dislike", "ignored"))
    write.add_argument("--pref-route", choices=ROUTES, help="A reaction needs an owner message; the others do not")
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
    candidates.add_argument("--limit", type=int, default=DEFAULT_CANDIDATE_LIMIT)
    candidates.add_argument("--json", action="store_true")
    candidates.set_defaults(heart_fn=_fn("evidence", "candidates"))
    review = subparsers.add_parser("review-input", help="Conversations, journal, self-note and recent edits for daily review")
    review.add_argument("--date")
    review.set_defaults(heart_fn=_fn("journal", "review_input"))
    refine = subparsers.add_parser("refine", help="Apply the daily taste review to this agent's journal")
    refine.add_argument("--date")
    refine.set_defaults(heart_fn=_fn("refiner", "daily"))
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
    carry = subparsers.add_parser("import", help="Carry a Hermes agent's self note, trial files and journal here")
    carry.add_argument("--from", dest="source", required=True, help="The Hermes home or profile directory")
    carry.add_argument("--dry-run", action="store_true")
    carry.set_defaults(heart_fn=_fn("carry", "command"))


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(prog="lina heart", description="Evidence-backed journal and self-note editor")
    build(result.add_subparsers(dest="heart_command", required=True))
    return result


def execute(args: argparse.Namespace) -> None:
    from pydantic import ValidationError

    from hermes_cli.heart import storage
    from hermes_cli.heart.locale import text
    from hermes_state_store import StoreUnavailableError
    try:
        args.heart_fn(args)
    except sqlite3.Error as error:
        print("heart: " + text("database", error=str(error)), file=sys.stderr)
        raise SystemExit(2) from None
    except StoreUnavailableError as error:
        print("heart: " + text("store", error=str(error)), file=sys.stderr)
        raise SystemExit(2) from None
    except (storage.HeartError, OSError, ValidationError, ValueError) as error:
        print(f"heart: {error}", file=sys.stderr)
        raise SystemExit(2) from None
