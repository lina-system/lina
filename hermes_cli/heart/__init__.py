"""The ``lina heart`` command facade."""

from __future__ import annotations


def main(argv: list[str] | None = None) -> None:
    from hermes_cli.heart.parser import execute, parser

    execute(parser().parse_args(argv))
