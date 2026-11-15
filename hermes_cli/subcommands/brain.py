"""Parser builder for ``lina brain``; registration is owned by the caller."""

from __future__ import annotations


def build_brain_parser(subparsers) -> None:
    from hermes_cli.brain.cli import build, execute

    parser = subparsers.add_parser("brain", help="The brain: typed wiki pages and their links in the LINA store")
    build(parser.add_subparsers(dest="brain_command", required=True))
    parser.set_defaults(func=execute)
