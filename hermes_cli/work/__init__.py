"""``lina work``: the work record (area -> goal -> to-do, with links to issues, cards and worker runs) in LINA's
store, and ``lina work run``, the hand-off of to-dos to ``lina worker`` (lina#36).

Facade: ``main`` runs one command. Topics live in siblings: ``env`` (config and people), ``store`` and
``store_sqlite``/``store_pg`` (the record in the ``work`` schema), ``ledger`` (the backend chosen in config),
``evaluate``/``state`` (an item's state, recomputed when it is written), ``view`` (board rows), ``writing`` (the
dashboard's writing rules), ``cmd_*`` (the verbs), ``run`` (hand-offs and the outbox), ``cards`` (goal and work cards),
``cmd_store`` (import, migrate, export).
"""

from __future__ import annotations

import argparse


def build_parser(prog: str = "work") -> argparse.ArgumentParser:
    from hermes_cli.work.parser import build
    ap = argparse.ArgumentParser(prog=prog, description="The work record: area -> goal -> to-do -> issue")
    build(ap.add_subparsers(dest="cmd", required=True))
    return ap


def execute(a) -> None:
    """One parsed command: resume hand-offs a crash left, run it, recompute what it wrote, close the store."""
    from hermes_cli.work import env, ledger, state
    from hermes_cli.work import run as runs
    env.current()
    try:
        if not getattr(a, "no_resume", False):
            runs.resume()
        a.fn(a)
        state.recompute()
    finally:
        ledger.close()


def main(argv: list[str] | None = None) -> None:
    execute(build_parser().parse_args(argv))
