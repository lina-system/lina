"""Parser builder for ``lina words``; registration is owned by the caller."""

from __future__ import annotations


def build_words_parser(subparsers) -> None:
    from hermes_cli.korean.cli import build, execute

    parser = subparsers.add_parser("words", help="The owner's shared word list: words to avoid and what to use")
    build(parser.add_subparsers(dest="words_command", required=True))
    parser.set_defaults(func=execute)
