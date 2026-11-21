"""Argument parsing and aggregate-only output for ``lina sources``."""

from __future__ import annotations

import argparse
import json
from pathlib import Path

from .defaults import CALL_LIMITS


def build_sources_parser(subparsers) -> None:
    sources = subparsers.add_parser("sources", help="Manually read configured external sources")
    actions = sources.add_subparsers(dest="source_action", required=True)
    sources.set_defaults(func=sources_command)
    for name, description in (
        ("collect", "Read messages into the configured spool"), ("probe", "Check access with one read"),
        ("status", "Show aggregate coverage and call budgets"), ("resume", "Clear a manual source stop"),
    ):
        parser = actions.add_parser(name, help=description)
        parser.add_argument("--state-dir", type=Path, help="Use this collector state directory")
        if name != "status":
            parser.add_argument("kind", choices=CALL_LIMITS)
        if name in ("collect", "probe"):
            parser.add_argument("--spool", type=Path, help="Override the configured spool (collection only)")
        if name == "collect":
            parser.add_argument("--since", help="Narrow the window with an ISO timestamp including timezone")
            parser.add_argument("--max-calls", type=int, help="Lower this run's call budget")
            parser.add_argument("--dry-run", action="store_true", help="Read and count without advancing coverage")


def sources_command(args: argparse.Namespace) -> None:
    from .runner import RunOptions, resume, run, status
    from .state import timestamp

    try:
        if args.source_action == "status":
            print(json.dumps({kind: status(kind, args.state_dir) for kind in CALL_LIMITS}))
            return
        if args.source_action == "resume":
            result = resume(args.kind, args.state_dir)
        else:
            since = getattr(args, "since", None)
            options = RunOptions(
                state_dir=args.state_dir, spool=args.spool, since=timestamp(since) if since else None,
                max_calls=getattr(args, "max_calls", None), dry_run=getattr(args, "dry_run", False),
                probe=args.source_action == "probe",
            )
            result = run(args.kind, options)
    except (ValueError, OSError):
        print("Source command failed: invalid settings, state, or path.")
        raise SystemExit(1) from None
    print(json.dumps({"source": args.kind, "outcome": result.outcome, "calls": result.calls,
                      "messages": result.messages, "error": result.error}))
    if result.outcome in ("error", "stopped"):
        raise SystemExit(1)
