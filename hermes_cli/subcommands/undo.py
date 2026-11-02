"""Parser builder for ``lina undo``; the lead owns root registration."""

from __future__ import annotations

import argparse


def _cmd_undo(args: argparse.Namespace) -> None:
    from hermes_cli.undo.cli import execute

    raise SystemExit(execute(args))


def build_undo_parser(subparsers) -> None:
    from hermes_cli.undo.parser import build

    parser = subparsers.add_parser("undo", help="Undo recorded changes using checkpoints and object history",
                                   description="Private checkpoint copies and a reference-only change journal")
    build(parser)
    parser.set_defaults(func=_cmd_undo)
