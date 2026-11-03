"""Parser builder for ``lina heart``; registration is owned by the caller."""

from __future__ import annotations


def build_heart_parser(subparsers) -> None:
    """Attach the heart command without changing the command registry."""
    from hermes_cli.heart.parser import build, execute

    parser = subparsers.add_parser("heart", help="Evidence-backed reaction journal, self-note edits and weekly review")
    build(parser.add_subparsers(dest="heart_command", required=True))
    parser.set_defaults(func=execute)
