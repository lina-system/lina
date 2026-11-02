"""``hermes work`` (``lina work``) subcommand: the work record and its hand-offs to ``lina worker`` (lina#36)."""

from __future__ import annotations


def _cmd_work(args) -> None:
    from hermes_cli.work import execute
    execute(args)


def build_work_parser(subparsers) -> None:
    """Attach the ``work`` subcommand to ``subparsers``."""
    from hermes_cli.work.parser import build
    parser = subparsers.add_parser(
        "work", help="The work record: areas, goals, to-dos, their checks, and hand-offs to lina worker",
        description="Goals and to-dos with the owner's words, a done condition and a read-only check; an item closes "
                    "only on its own check or a confirmation. `lina work run` hands to-dos to `lina worker` with the "
                    "record and the hand-off written together.")
    build(parser.add_subparsers(dest="cmd", required=True))
    parser.set_defaults(func=_cmd_work)
